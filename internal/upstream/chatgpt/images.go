package chatgpt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	stdhttp "net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

type ImageRequest struct {
	Prompt         string
	Model          string
	Size           string
	ResponseFormat string
	PollTimeout    time.Duration
	Images         []ImageInput
}

type ImageInput struct {
	Name        string
	ContentType string
	Data        []byte
}

type ImageResult struct {
	B64JSON       string `json:"b64_json,omitempty"`
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type conversationState struct {
	Text         string
	Conversation string
	FileIDs      []string
	SedimentIDs  []string
	Blocked      bool
	ToolInvoked  *bool
	TurnUseCase  string
}

func (c *Client) GenerateImage(ctx context.Context, request ImageRequest) ([]ImageResult, error) {
	return c.runImage(ctx, request, nil)
}

func (c *Client) EditImage(ctx context.Context, request ImageRequest) ([]ImageResult, error) {
	if len(request.Images) == 0 {
		return nil, errors.New("image is required")
	}
	references := make([]uploadedImage, 0, len(request.Images))
	for index, input := range request.Images {
		reference, err := c.uploadImage(ctx, input, index+1)
		if err != nil {
			return nil, err
		}
		references = append(references, reference)
	}
	return c.runImage(ctx, request, references)
}

func (c *Client) runImage(ctx context.Context, request ImageRequest, references []uploadedImage) ([]ImageResult, error) {
	if c.accessToken == "" {
		return nil, errors.New("access token is required for image endpoints")
	}
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.Prompt == "" {
		return nil, errors.New("prompt is required")
	}
	if request.Model == "" {
		request.Model = "gpt-image-2"
	}
	if request.ResponseFormat == "" {
		request.ResponseFormat = "b64_json"
	}
	if request.PollTimeout <= 0 {
		request.PollTimeout = 120 * time.Second
	}
	if err := c.bootstrap(ctx); err != nil {
		return nil, err
	}
	requirements, err := c.chatRequirements(ctx)
	if err != nil {
		return nil, err
	}
	prompt := buildImagePrompt(request.Prompt, request.Size)
	conduitToken, err := c.prepareImageConversation(ctx, prompt, requirements, request.Model)
	if err != nil {
		return nil, err
	}
	if len(references) > 0 {
		traceImageSet(ctx, "reference_file_ids", referenceFileIDs(references))
	}
	state, err := c.startImageConversation(ctx, prompt, requirements, conduitToken, request.Model, references)
	if err != nil {
		return nil, err
	}
	if state.Text != "" && len(state.FileIDs) == 0 && len(state.SedimentIDs) == 0 {
		isText := state.ToolInvoked != nil && !*state.ToolInvoked || state.TurnUseCase == "text"
		if state.Blocked || isText || IsImagePromptAdjustText(state.Text) {
			return nil, &ImagePromptAdjustError{Text: state.Text}
		}
	}
	fileIDs, sedimentIDs := state.FileIDs, state.SedimentIDs
	traceImageSet(ctx, "raw_result_file_ids_sse", fileIDs)
	traceImageSet(ctx, "raw_result_sediment_ids_sse", sedimentIDs)
	fileIDs = filterReferenceCandidateIDs(fileIDs, references)
	sedimentIDs = filterReferenceCandidateIDs(sedimentIDs, references)
	if state.Conversation != "" && len(fileIDs) == 0 && len(sedimentIDs) == 0 {
		fileIDs, sedimentIDs = c.pollImageResults(ctx, state.Conversation, request.PollTimeout)
		fileIDs = filterReferenceCandidateIDs(fileIDs, references)
		sedimentIDs = filterReferenceCandidateIDs(sedimentIDs, references)
	}
	traceImageSet(ctx, "result_file_ids", fileIDs)
	traceImageSet(ctx, "result_sediment_ids", sedimentIDs)
	urls, err := c.resolveImageURLs(ctx, state.Conversation, fileIDs, sedimentIDs)
	if err != nil {
		return nil, err
	}
	traceImageSet(ctx, "resolved_urls", urls)
	if len(urls) == 0 {
		if state.Text != "" {
			return nil, &ImagePromptAdjustError{Text: state.Text}
		}
		return nil, errors.New("image generation returned no image")
	}
	results := make([]ImageResult, 0, len(urls))
	for _, imageURL := range urls {
		result := ImageResult{RevisedPrompt: request.Prompt}
		if request.ResponseFormat == "url" {
			result.URL = imageURL
		} else {
			data, err := c.downloadBytes(ctx, imageURL)
			if err != nil {
				return nil, err
			}
			result.B64JSON = base64.StdEncoding.EncodeToString(data)
		}
		results = append(results, result)
	}
	return results, nil
}

type uploadedImage struct {
	FileID   string
	FileName string
	FileSize int
	MIMEType string
	Width    int
	Height   int
}

func (c *Client) uploadImage(ctx context.Context, input ImageInput, index int) (uploadedImage, error) {
	if len(input.Data) == 0 {
		return uploadedImage{}, errors.New("image data is empty")
	}
	mimeType, width, height, ext, err := detectImageMetadata(input.Data, input.ContentType)
	if err != nil {
		return uploadedImage{}, err
	}
	fileName := cleanImageFileName(input.Name, index, ext)
	path := "/backend-api/files"
	data, err := c.postJSON(ctx, path, map[string]any{
		"file_name": fileName,
		"file_size": len(input.Data),
		"use_case":  "multimodal",
		"width":     width,
		"height":    height,
	}, map[string]string{"Accept": "application/json"})
	if err != nil {
		return uploadedImage{}, err
	}
	traceImageAppend(ctx, "reference_uploads_raw", map[string]any{
		"file_name": fileName,
		"response":  data,
	}, 8)
	fileID := stringValue(data["file_id"], "")
	uploadURL := stringValue(data["upload_url"], "")
	if fileID == "" || uploadURL == "" {
		return uploadedImage{}, errors.New("missing image upload metadata")
	}
	time.Sleep(500 * time.Millisecond)
	if err := c.putUpload(ctx, uploadURL, mimeType, input.Data); err != nil {
		return uploadedImage{}, err
	}
	if _, err := c.postJSON(ctx, "/backend-api/files/"+fileID+"/uploaded", map[string]any{}, map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}); err != nil {
		return uploadedImage{}, err
	}
	return uploadedImage{
		FileID:   fileID,
		FileName: fileName,
		FileSize: len(input.Data),
		MIMEType: mimeType,
		Width:    width,
		Height:   height,
	}, nil
}

func detectImageMetadata(data []byte, providedType string) (string, int, int, string, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		detected := strings.TrimSpace(stdhttp.DetectContentType(data))
		if strings.HasPrefix(strings.ToLower(detected), "image/") {
			return "", 0, 0, "", fmt.Errorf("unsupported image format: please use PNG, JPG, or GIF (detected %s)", detected)
		}
		return "", 0, 0, "", fmt.Errorf("unsupported image format: please use PNG, JPG, or GIF")
	}
	mimeType := strings.TrimSpace(providedType)
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		mimeType = ""
	}
	ext := format
	switch format {
	case "jpeg":
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		ext = "jpg"
	case "png":
		if mimeType == "" {
			mimeType = "image/png"
		}
	case "gif":
		if mimeType == "" {
			mimeType = "image/gif"
		}
	default:
		if mimeType == "" {
			mimeType = stdhttp.DetectContentType(data)
		}
	}
	return mimeType, config.Width, config.Height, ext, nil
}

func cleanImageFileName(name string, index int, ext string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = fmt.Sprintf("image_%d.%s", index, ext)
	}
	if filepath.Ext(name) == "" && ext != "" {
		name += "." + ext
	}
	return name
}

func (c *Client) putUpload(ctx context.Context, uploadURL string, mimeType string, data []byte) error {
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	req.Header.Set("x-ms-version", "2020-04-08")
	req.Header.Set("Origin", c.baseURL)
	req.Header.Set("Referer", c.baseURL+"/")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("image upload failed: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *Client) prepareImageConversation(ctx context.Context, prompt string, requirements ChatRequirements, model string) (string, error) {
	path := "/backend-api/f/conversation/prepare"
	payload := map[string]any{
		"action":                "next",
		"fork_from_shared_post": false,
		"parent_message_id":     newUUID(),
		"model":                 imageModelSlug(model),
		"client_prepare_state":  "success",
		"timezone_offset_min":   -480,
		"timezone":              "Asia/Shanghai",
		"conversation_mode":     map[string]any{"kind": "primary_assistant"},
		"system_hints":          []string{"picture_v2"},
		"partial_query": map[string]any{
			"id":      newUUID(),
			"author":  map[string]any{"role": "user"},
			"content": map[string]any{"content_type": "text", "parts": []string{prompt}},
		},
		"supports_buffering":       true,
		"supported_encodings":      []string{"v1"},
		"client_contextual_info":   map[string]any{"app_name": "chatgpt.com"},
		"paragen_cot_summary_mode": "allow",
	}
	data, err := c.postImageJSON(ctx, path, payload, requirements, "", "*/*")
	if err != nil {
		return "", err
	}
	traceImageSet(ctx, "prepare_raw", data)
	token := stringValue(data["conduit_token"], "")
	if token == "" {
		return "", errors.New("missing conduit token")
	}
	return token, nil
}

func (c *Client) startImageConversation(ctx context.Context, prompt string, requirements ChatRequirements, conduitToken string, model string, references []uploadedImage) (conversationState, error) {
	path := "/backend-api/f/conversation"
	content, metadata := imageConversationContent(prompt, references)
	payload := map[string]any{
		"action": "next",
		"messages": []map[string]any{{
			"id":          newUUID(),
			"author":      map[string]any{"role": "user"},
			"create_time": float64(time.Now().UnixMilli()) / 1000,
			"content":     content,
			"metadata":    metadata,
		}},
		"parent_message_id":        newUUID(),
		"model":                    imageModelSlug(model),
		"client_prepare_state":     "sent",
		"timezone_offset_min":      -480,
		"timezone":                 "Asia/Shanghai",
		"conversation_mode":        map[string]any{"kind": "primary_assistant"},
		"enable_message_followups": true,
		"system_hints":             []string{"picture_v2"},
		"supports_buffering":       true,
		"supported_encodings":      []string{"v1"},
		"client_contextual_info": map[string]any{
			"is_dark_mode":       false,
			"time_since_loaded":  1200,
			"page_height":        1072,
			"page_width":         1724,
			"pixel_ratio":        1.2,
			"screen_height":      1440,
			"screen_width":       2560,
			"app_name":           "chatgpt.com",
			"supports_buffering": true,
		},
		"paragen_cot_summary_display_override": "allow",
		"force_parallel_switch":                "auto",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return conversationState{}, err
	}
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return conversationState{}, err
	}
	c.applyImageHeaders(req, path, requirements, conduitToken, "text/event-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return conversationState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == fhttp.StatusUnauthorized {
		io.Copy(io.Discard, resp.Body)
		traceImageSet(ctx, "conversation_start_error", map[string]any{
			"status_code": resp.StatusCode,
			"body":        "unauthorized",
		})
		return conversationState{}, ErrInvalidAccessToken
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		traceImageSet(ctx, "conversation_start_error", map[string]any{
			"status_code": resp.StatusCode,
			"body":        string(payload),
		})
		return conversationState{}, fmt.Errorf("%s failed: HTTP %d %s", path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	traceImageSet(ctx, "conversation_start_status", resp.StatusCode)
	return parseImageSSE(ctx, resp.Body)
}

func imageConversationContent(prompt string, references []uploadedImage) (map[string]any, map[string]any) {
	metadata := map[string]any{
		"developer_mode_connector_ids": []any{},
		"selected_github_repos":        []any{},
		"selected_all_github_repos":    false,
		"system_hints":                 []string{"picture_v2"},
		"serialization_metadata":       map[string]any{"custom_symbol_offsets": []any{}},
	}
	if len(references) == 0 {
		return map[string]any{"content_type": "text", "parts": []string{prompt}}, metadata
	}
	parts := make([]any, 0, len(references)+1)
	attachments := make([]map[string]any, 0, len(references))
	for _, reference := range references {
		parts = append(parts, map[string]any{
			"content_type":  "image_asset_pointer",
			"asset_pointer": "file-service://" + reference.FileID,
			"width":         reference.Width,
			"height":        reference.Height,
			"size_bytes":    reference.FileSize,
		})
		attachments = append(attachments, map[string]any{
			"id":       reference.FileID,
			"mimeType": reference.MIMEType,
			"name":     reference.FileName,
			"size":     reference.FileSize,
			"width":    reference.Width,
			"height":   reference.Height,
		})
	}
	parts = append(parts, prompt)
	metadata["attachments"] = attachments
	return map[string]any{"content_type": "multimodal_text", "parts": parts}, metadata
}

func (c *Client) postImageJSON(ctx context.Context, path string, payload any, requirements ChatRequirements, conduitToken string, accept string) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.applyImageHeaders(req, path, requirements, conduitToken, accept)
	return c.doJSON(req)
}

func (c *Client) applyImageHeaders(req *fhttp.Request, path string, requirements ChatRequirements, conduitToken string, accept string) {
	if accept == "" {
		accept = "*/*"
	}
	headers := map[string]string{
		"Accept":       accept,
		"Content-Type": "application/json",
		"OpenAI-Sentinel-Chat-Requirements-Token": requirements.Token,
	}
	if requirements.ProofToken != "" {
		headers["OpenAI-Sentinel-Proof-Token"] = requirements.ProofToken
	}
	if conduitToken != "" {
		headers["X-Conduit-Token"] = conduitToken
	}
	if accept == "text/event-stream" {
		headers["X-Oai-Turn-Trace-Id"] = newUUID()
	}
	c.applyHeaders(req, path, headers)
}

func parseImageSSE(ctx context.Context, reader io.Reader) (conversationState, error) {
	state := conversationState{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	currentText := ""
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			traceImageSet(ctx, "sse_state", map[string]any{
				"conversation_id": state.Conversation,
				"file_ids":        state.FileIDs,
				"sediment_ids":    state.SedimentIDs,
				"blocked":         state.Blocked,
				"turn_use_case":   state.TurnUseCase,
				"text":            state.Text,
			})
			return state, nil
		}
		traceImageAppend(ctx, "sse_raw_events", payload, 16)
		updateImageState(ctx, &state, payload)
		nextText, ok := assistantTextFromPayload(payload, currentText)
		if !ok || nextText == currentText {
			continue
		}
		currentText = nextText
		state.Text = nextText
	}
	traceImageSet(ctx, "sse_state", map[string]any{
		"conversation_id": state.Conversation,
		"file_ids":        state.FileIDs,
		"sediment_ids":    state.SedimentIDs,
		"blocked":         state.Blocked,
		"turn_use_case":   state.TurnUseCase,
		"text":            state.Text,
	})
	return state, scanner.Err()
}

func updateImageState(ctx context.Context, state *conversationState, payload string) {
	addImageIDs(payload, &state.FileIDs, &state.SedimentIDs)
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return
	}
	traceImageAppend(ctx, "sse_raw_json", event, 12)
	if state.Conversation == "" {
		state.Conversation = stringValue(event["conversation_id"], "")
	}
	if value, ok := event["v"].(map[string]any); ok && state.Conversation == "" {
		state.Conversation = stringValue(value["conversation_id"], "")
	}
	if eventType := stringValue(event["type"], ""); eventType == "moderation" {
		if moderation, ok := event["moderation_response"].(map[string]any); ok && boolValue(moderation["blocked"]) {
			state.Blocked = true
		}
	}
	if eventType := stringValue(event["type"], ""); eventType == "server_ste_metadata" {
		if metadata, ok := event["metadata"].(map[string]any); ok {
			if value, ok := metadata["tool_invoked"].(bool); ok {
				state.ToolInvoked = &value
			}
			state.TurnUseCase = stringValue(metadata["turn_use_case"], state.TurnUseCase)
		}
	}
}

func (c *Client) pollImageResults(ctx context.Context, conversationID string, timeout time.Duration) ([]string, []string) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conversation, err := c.getConversation(ctx, conversationID)
		if err != nil {
			time.Sleep(4 * time.Second)
			continue
		}
		traceImageSet(ctx, "poll_conversation_raw", traceJSONString(conversation, 4000))
		fileIDs, sedimentIDs := extractImageToolRecords(conversation)
		traceImageSet(ctx, "poll_extracted_file_ids", fileIDs)
		traceImageSet(ctx, "poll_extracted_sediment_ids", sedimentIDs)
		if len(fileIDs) > 0 || len(sedimentIDs) > 0 {
			return fileIDs, sedimentIDs
		}
		time.Sleep(4 * time.Second)
	}
	return nil, nil
}

func (c *Client) getConversation(ctx context.Context, conversationID string) (map[string]any, error) {
	return c.getJSON(ctx, "/backend-api/conversation/"+conversationID, map[string]string{"Accept": "application/json"})
}

func extractImageToolRecords(conversation map[string]any) ([]string, []string) {
	mapping, _ := conversation["mapping"].(map[string]any)
	fileIDs := make([]string, 0)
	sedimentIDs := make([]string, 0)
	for _, rawNode := range mapping {
		node, _ := rawNode.(map[string]any)
		message, _ := node["message"].(map[string]any)
		author, _ := message["author"].(map[string]any)
		metadata, _ := message["metadata"].(map[string]any)
		content, _ := message["content"].(map[string]any)
		if stringValue(author["role"], "") != "tool" || stringValue(metadata["async_task_type"], "") != "image_gen" {
			continue
		}
		for _, part := range sliceValue(content["parts"]) {
			switch typed := part.(type) {
			case string:
				addImageIDs(typed, &fileIDs, &sedimentIDs)
			case map[string]any:
				addImageIDs(stringValue(typed["asset_pointer"], ""), &fileIDs, &sedimentIDs)
			}
		}
	}
	return fileIDs, sedimentIDs
}

func (c *Client) resolveImageURLs(ctx context.Context, conversationID string, fileIDs []string, sedimentIDs []string) ([]string, error) {
	urls := make([]string, 0, len(fileIDs)+len(sedimentIDs))
	for _, fileID := range uniqueStrings(fileIDs) {
		if fileID == "file_upload" {
			continue
		}
		data, err := c.getJSON(ctx, "/backend-api/files/"+fileID+"/download", map[string]string{"Accept": "application/json"})
		if err == nil {
			traceImageAppend(ctx, "download_raw", map[string]any{
				"source":   "file",
				"id":       fileID,
				"response": data,
			}, 16)
			if url := stringValue(data["download_url"], ""); url != "" {
				urls = append(urls, url)
			} else if url := stringValue(data["url"], ""); url != "" {
				urls = append(urls, url)
			}
		}
	}
	if len(urls) > 0 || conversationID == "" {
		return urls, nil
	}
	for _, sedimentID := range uniqueStrings(sedimentIDs) {
		data, err := c.getJSON(ctx, "/backend-api/conversation/"+conversationID+"/attachment/"+sedimentID+"/download", map[string]string{"Accept": "application/json"})
		if err != nil {
			continue
		}
		traceImageAppend(ctx, "download_raw", map[string]any{
			"source":   "attachment",
			"id":       sedimentID,
			"response": data,
		}, 16)
		if url := stringValue(data["download_url"], ""); url != "" {
			urls = append(urls, url)
		} else if url := stringValue(data["url"], ""); url != "" {
			urls = append(urls, url)
		}
	}
	return urls, nil
}

func (c *Client) downloadBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	c.applyDownloadHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("image download failed: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

func (c *Client) applyDownloadHeaders(req *fhttp.Request) {
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8,en-US;q=0.7")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Referer", c.baseURL+"/")
	req.Header.Set("Sec-Ch-Ua", `"Microsoft Edge";v="143", "Chromium";v="143", "Not A(Brand";v="24"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "image")
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	req.Header.Set("Sec-Fetch-Site", downloadFetchSite(c.baseURL, req.URL.String()))
	if c.accessToken != "" && hostWithoutPort(c.baseURL) == hostWithoutPort(req.URL.String()) {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}
}

func downloadFetchSite(baseURL string, targetURL string) string {
	baseHost := hostWithoutPort(baseURL)
	targetHost := hostWithoutPort(targetURL)
	if baseHost == "" || targetHost == "" {
		return "cross-site"
	}
	if targetHost == baseHost {
		return "same-origin"
	}
	if strings.HasSuffix(targetHost, "."+baseHost) || strings.HasSuffix(baseHost, "."+targetHost) {
		return "same-site"
	}
	return "cross-site"
}

func hostWithoutPort(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func buildImagePrompt(prompt string, size string) string {
	size = strings.TrimSpace(size)
	if size == "" || strings.EqualFold(size, "auto") || strings.EqualFold(size, "default") {
		return prompt
	}
	hints := map[string]string{
		"1:1":  "输出为 1:1 正方形构图，主体居中，适合正方形画幅。",
		"16:9": "输出为 16:9 横屏构图，适合宽画幅展示。",
		"9:16": "输出为 9:16 竖屏构图，适合竖版画幅展示。",
		"4:3":  "输出为 4:3 比例，兼顾宽度与高度，适合展示画面细节。",
		"3:4":  "输出为 3:4 比例，纵向构图，适合人物肖像或竖向场景。",
	}
	if hint, ok := hints[size]; ok {
		return strings.TrimSpace(prompt) + "\n\n" + hint
	}
	return strings.TrimSpace(prompt) + "\n\n输出图片，宽高比为 " + size + "。"
}

func imageModelSlug(model string) string {
	switch strings.TrimSpace(model) {
	case "gpt-image-2":
		return "gpt-5-3"
	case "codex-gpt-image-2":
		return "codex-gpt-image-2"
	case "":
		return "auto"
	default:
		return "auto"
	}
}

var (
	fileServicePattern = regexp.MustCompile(`file-service://([A-Za-z0-9_-]+)`)
	fileIDPattern      = regexp.MustCompile(`file[-_][A-Za-z0-9_-]+`)
	sedimentIDPattern  = regexp.MustCompile(`sediment://([A-Za-z0-9_-]+)`)
)

func addImageIDs(text string, fileIDs *[]string, sedimentIDs *[]string) {
	for _, match := range fileServicePattern.FindAllStringSubmatch(text, -1) {
		if len(match) == 2 {
			addUnique(fileIDs, match[1])
		}
	}
	for _, fileID := range fileIDPattern.FindAllString(text, -1) {
		if fileID == "file-service" {
			continue
		}
		addUnique(fileIDs, fileID)
	}
	for _, match := range sedimentIDPattern.FindAllStringSubmatch(text, -1) {
		if len(match) == 2 {
			addUnique(sedimentIDs, match[1])
		}
	}
}

func addUnique(items *[]string, value string) {
	if value == "" {
		return
	}
	for _, item := range *items {
		if item == value {
			return
		}
	}
	*items = append(*items, value)
}

func uniqueStrings(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		addUnique(&out, item)
	}
	return out
}

func referenceFileIDs(references []uploadedImage) []string {
	ids := make([]string, 0, len(references))
	for _, reference := range references {
		if strings.TrimSpace(reference.FileID) == "" {
			continue
		}
		ids = append(ids, strings.TrimSpace(reference.FileID))
	}
	return ids
}

func filterReferenceCandidateIDs(ids []string, references []uploadedImage) []string {
	if len(ids) == 0 || len(references) == 0 {
		return uniqueStrings(ids)
	}
	referenceIDs := referenceFileIDs(references)
	if len(referenceIDs) == 0 {
		return uniqueStrings(ids)
	}
	filtered := make([]string, 0, len(ids))
	for _, id := range uniqueStrings(ids) {
		if isReferenceCandidateID(id, referenceIDs) {
			continue
		}
		filtered = append(filtered, id)
	}
	return filtered
}

func isReferenceCandidateID(candidate string, referenceIDs []string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	for _, referenceID := range referenceIDs {
		referenceID = strings.TrimSpace(referenceID)
		if referenceID == "" {
			continue
		}
		if candidate == referenceID {
			return true
		}
		if strings.HasPrefix(referenceID, candidate) || strings.HasPrefix(candidate, referenceID) {
			return true
		}
	}
	return false
}
