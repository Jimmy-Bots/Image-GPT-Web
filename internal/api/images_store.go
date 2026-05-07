package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type imagesDeleteRequest struct {
	Paths []string `json:"paths"`
}

const imageMetaSuffix = ".meta.json"

type storedImageMeta struct {
	Prompt        string    `json:"prompt,omitempty"`
	RevisedPrompt string    `json:"revised_prompt,omitempty"`
	ArchivedAt    time.Time `json:"archived_at,omitempty"`
	OwnerID       string    `json:"owner_id,omitempty"`
}

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query")))
	sortMode := strings.TrimSpace(r.URL.Query().Get("sort"))
	dateScope := strings.TrimSpace(r.URL.Query().Get("date_scope"))
	items, err := s.listStoredImages(r, query, sortMode, dateScope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "image_list_failed", err.Error())
		return
	}
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 24)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 24
	}
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items[start:end],
		"total":     len(items),
		"page":      page,
		"page_size": pageSize,
	})
}

func (s *Server) handleDeleteImages(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var req imagesDeleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	removed := 0
	for _, rel := range compactStrings(req.Paths) {
		path, ok := safeJoin(s.cfg.ImagesDir, rel)
		if !ok {
			continue
		}
		if err := os.Remove(path); err == nil {
			_ = os.Remove(imageMetaPath(path))
			removed++
		}
	}
	s.addAuditLog(r, identity, "image", "删除归档图片", map[string]any{
		"requested": len(compactStrings(req.Paths)),
		"removed":   removed,
		"paths":     compactStrings(req.Paths),
	})
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

func (s *Server) listStoredImages(r *http.Request, query string, sortMode string, dateScope string) ([]map[string]any, error) {
	items := make([]map[string]any, 0)
	err := filepath.WalkDir(s.cfg.ImagesDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), imageMetaSuffix) {
			return nil
		}
		rel, err := filepath.Rel(s.cfg.ImagesDir, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !matchImageDateScope(info.ModTime(), dateScope) {
			return nil
		}
		meta := readImageMeta(path)
		if strings.TrimSpace(meta.OwnerID) == "" {
			return nil
		}
		if query != "" {
			haystack := strings.ToLower(strings.Join([]string{
				entry.Name(),
				rel,
				meta.Prompt,
				meta.RevisedPrompt,
			}, " "))
			if !strings.Contains(haystack, query) {
				return nil
			}
		}
		displayPrompt := strings.TrimSpace(meta.Prompt)
		if displayPrompt == "" {
			displayPrompt = strings.TrimSpace(meta.RevisedPrompt)
		}
		if displayPrompt == "" {
			displayPrompt = entry.Name()
		}
		items = append(items, map[string]any{
			"path":           rel,
			"name":           entry.Name(),
			"url":            publicBaseURL(r) + "/images/" + rel,
			"size":           info.Size(),
			"created_at":     info.ModTime().UTC(),
			"prompt":         meta.Prompt,
			"revised_prompt": meta.RevisedPrompt,
			"display_prompt": displayPrompt,
			"owner_id":       meta.OwnerID,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		if sortMode == "large" {
			leftSize, _ := items[i]["size"].(int64)
			rightSize, _ := items[j]["size"].(int64)
			if leftSize == rightSize {
				left, _ := items[i]["created_at"].(time.Time)
				right, _ := items[j]["created_at"].(time.Time)
				return left.After(right)
			}
			return leftSize > rightSize
		}
		left, _ := items[i]["created_at"].(time.Time)
		right, _ := items[j]["created_at"].(time.Time)
		if sortMode == "old" {
			return left.Before(right)
		}
		return left.After(right)
	})
	return items, nil
}

func (s *Server) persistImageResults(r *http.Request, result map[string]any, prompt string, ownerID string) int {
	saved := persistImageResultItems(s.cfg.ImagesDir, publicBaseURL(r), result, prompt, ownerID)
	if saved == 0 && s.cfg.DebugLogging() {
		log.Printf("image_archive saved=0 path=%s items=%d", r.URL.Path, imageResultCount(result))
	}
	return saved
}

func persistImageResultItems(imagesDir string, baseURL string, result map[string]any, prompt string, ownerID string) int {
	saved := 0
	forEachImageResultItem(result, func(item map[string]any) {
		if persistImageItem(imagesDir, baseURL, item, prompt, ownerID) {
			saved++
		}
	})
	return saved
}

func persistImageItem(root string, baseURL string, item map[string]any, prompt string, ownerID string) bool {
	b64 := strings.TrimSpace(stringFromAny(item["b64_json"], ""))
	if b64 == "" {
		return false
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) == 0 {
		log.Printf("image_archive decode_failed err=%v", err)
		return false
	}
	sum := sha256.Sum256(data)
	now := time.Now().UTC()
	rel := filepath.ToSlash(filepath.Join(now.Format("2006"), now.Format("01"), now.Format("02"), fmt.Sprintf("%d_%s.png", now.Unix(), hex.EncodeToString(sum[:])[:16])))
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("image_archive mkdir_failed path=%s err=%v", filepath.Dir(path), err)
		return false
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("image_archive write_failed path=%s err=%v", path, err)
		return false
	}
	item["path"] = rel
	if baseURL != "" {
		item["url"] = strings.TrimRight(baseURL, "/") + "/images/" + rel
	} else {
		item["url"] = "/images/" + rel
	}
	writeImageMeta(path, storedImageMeta{
		Prompt:        strings.TrimSpace(prompt),
		RevisedPrompt: strings.TrimSpace(stringFromAny(item["revised_prompt"], "")),
		ArchivedAt:    now,
		OwnerID:       strings.TrimSpace(ownerID),
	})
	log.Printf("image_archive saved path=%s bytes=%d url_configured=%t", rel, len(data), baseURL != "")
	return true
}

func imageMetaPath(path string) string {
	return path + imageMetaSuffix
}

func writeImageMeta(path string, meta storedImageMeta) {
	payload, err := json.Marshal(meta)
	if err != nil {
		return
	}
	_ = os.WriteFile(imageMetaPath(path), payload, 0o644)
}

func readImageMeta(path string) storedImageMeta {
	body, err := os.ReadFile(imageMetaPath(path))
	if err != nil || len(body) == 0 {
		return storedImageMeta{}
	}
	var meta storedImageMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return storedImageMeta{}
	}
	return meta
}

func matchImageDateScope(createdAt time.Time, scope string) bool {
	now := time.Now()
	created := createdAt.In(now.Location())
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "today":
		y1, m1, d1 := now.Date()
		y2, m2, d2 := created.Date()
		return y1 == y2 && m1 == m2 && d1 == d2
	case "7d":
		return created.After(now.Add(-7 * 24 * time.Hour))
	default:
		return true
	}
}

func forEachImageResultItem(result map[string]any, fn func(map[string]any)) int {
	count := 0
	switch items := result["data"].(type) {
	case []map[string]any:
		for _, item := range items {
			fn(item)
			count++
		}
	case []any:
		for _, raw := range items {
			if item, ok := raw.(map[string]any); ok {
				fn(item)
				count++
			}
		}
	}
	return count
}

func imageResultCount(result map[string]any) int {
	return forEachImageResultItem(result, func(map[string]any) {})
}

func normalizeImageResponseFormat(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "url") {
		return "url"
	}
	return "b64_json"
}

func shapeImageResponseForClient(result map[string]any, format string) {
	if normalizeImageResponseFormat(format) != "url" {
		return
	}
	forEachImageResultItem(result, func(item map[string]any) {
		if strings.TrimSpace(stringFromAny(item["url"], "")) != "" {
			delete(item, "b64_json")
		}
	})
}

func publicBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = strings.Split(forwarded, ",")[0]
	}
	return scheme + "://" + r.Host
}
