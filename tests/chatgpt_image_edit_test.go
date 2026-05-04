package tests

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"

	"gpt-image-web/internal/upstream/chatgpt"
)

func TestChatGPTEditImageUploadsReferenceAndReturnsB64(t *testing.T) {
	imageData := tinyPNG(t)
	editedData := []byte("edited-image-bytes")
	doer := &imageEditDoer{t: t, imageData: imageData, editedData: editedData}
	client := chatgpt.NewClient("token", chatgpt.WithHTTPClient(doer))

	results, err := client.EditImage(context.Background(), chatgpt.ImageRequest{
		Prompt:         "make it brighter",
		Model:          "gpt-image-2",
		ResponseFormat: "b64_json",
		Images: []chatgpt.ImageInput{{
			Name:        "source.png",
			ContentType: "image/png",
			Data:        imageData,
		}},
	})
	if err != nil {
		t.Fatalf("EditImage returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("unexpected result count: %d", len(results))
	}
	want := base64.StdEncoding.EncodeToString(editedData)
	if results[0].B64JSON != want {
		t.Fatalf("unexpected b64 result: %q", results[0].B64JSON)
	}
	if !doer.sawUploadPut || !doer.sawUploadedConfirm || !doer.sawReference {
		t.Fatalf("missing upload flow: put=%v confirm=%v reference=%v", doer.sawUploadPut, doer.sawUploadedConfirm, doer.sawReference)
	}
}

type imageEditDoer struct {
	t                  *testing.T
	imageData          []byte
	editedData         []byte
	sawUploadPut       bool
	sawUploadedConfirm bool
	sawReference       bool
}

func (d *imageEditDoer) Do(req *fhttp.Request) (*fhttp.Response, error) {
	if req.URL.Host == "upload.local" {
		return d.handleBlobUpload(req)
	}
	if req.URL.Path != "/" && req.URL.Host != "chatgpt.com" && req.URL.Host != "" {
		d.t.Fatalf("unexpected host/path: %s %s", req.URL.Host, req.URL.Path)
	}
	switch req.URL.Path {
	case "/":
		return response(200, `<html data-build="fallback"><script src="/_next/static/chunks/c/abc/_build.js"></script></html>`), nil
	case "/backend-api/sentinel/chat-requirements":
		return response(200, `{"token":"requirements-token"}`), nil
	case "/backend-api/files":
		var payload map[string]any
		decodeBody(d.t, req, &payload)
		if payload["file_name"] != "source.png" || int(payload["file_size"].(float64)) != len(d.imageData) {
			d.t.Fatalf("unexpected file metadata: %#v", payload)
		}
		if int(payload["width"].(float64)) != 2 || int(payload["height"].(float64)) != 1 {
			d.t.Fatalf("unexpected dimensions: %#v", payload)
		}
		return response(200, `{"file_id":"file-reference","upload_url":"https://upload.local/blob"}`), nil
	case "/backend-api/files/file-reference/uploaded":
		d.sawUploadedConfirm = true
		return response(200, `{}`), nil
	case "/backend-api/f/conversation/prepare":
		return response(200, `{"conduit_token":"conduit-token"}`), nil
	case "/backend-api/f/conversation":
		if req.Header.Get("X-Conduit-Token") != "conduit-token" {
			d.t.Fatalf("missing conduit token: %q", req.Header.Get("X-Conduit-Token"))
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			d.t.Fatalf("read conversation body: %v", err)
		}
		if !strings.Contains(string(body), "file-service://file-reference") {
			d.t.Fatalf("conversation body missing image reference: %s", string(body))
		}
		d.sawReference = true
		return response(200, `data: {"conversation_id":"conv-1","message":{"author":{"role":"tool"},"metadata":{"async_task_type":"image_gen"},"content":{"content_type":"multimodal_text","parts":[{"asset_pointer":"file-service://file-output"}]}}}
data: [DONE]
`), nil
	case "/backend-api/files/file-output/download":
		return response(200, `{"download_url":"https://chatgpt.com/backend-api/estuary/content/output.png"}`), nil
	case "/backend-api/estuary/content/output.png":
		if req.Header.Get("Authorization") != "Bearer token" {
			d.t.Fatalf("missing download authorization: %q", req.Header.Get("Authorization"))
		}
		return binaryResponse(200, d.editedData), nil
	default:
		d.t.Fatalf("unexpected request path: %s", req.URL.Path)
		return response(404, `{}`), nil
	}
}

func (d *imageEditDoer) handleBlobUpload(req *fhttp.Request) (*fhttp.Response, error) {
	if req.Method != fhttp.MethodPut {
		d.t.Fatalf("unexpected upload method: %s", req.Method)
	}
	if req.Header.Get("x-ms-blob-type") != "BlockBlob" {
		d.t.Fatalf("missing blob header: %q", req.Header.Get("x-ms-blob-type"))
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		d.t.Fatalf("read upload body: %v", err)
	}
	if !bytes.Equal(body, d.imageData) {
		d.t.Fatalf("upload body mismatch")
	}
	d.sawUploadPut = true
	return response(201, ``), nil
}

func decodeBody(t *testing.T, req *fhttp.Request, target any) {
	t.Helper()
	if err := json.NewDecoder(req.Body).Decode(target); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}

func binaryResponse(status int, body []byte) *fhttp.Response {
	return &fhttp.Response{
		StatusCode: status,
		Header:     make(fhttp.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
