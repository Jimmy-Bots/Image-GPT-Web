package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	items, err := s.listStoredImages(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "image_list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleDeleteImages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
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
			removed++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

func (s *Server) listStoredImages(r *http.Request) ([]map[string]any, error) {
	items := make([]map[string]any, 0)
	err := filepath.WalkDir(s.cfg.ImagesDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
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
		items = append(items, map[string]any{
			"path":       rel,
			"name":       entry.Name(),
			"url":        publicBaseURL(r) + "/images/" + rel,
			"size":       info.Size(),
			"created_at": info.ModTime().UTC(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		left, _ := items[i]["created_at"].(time.Time)
		right, _ := items[j]["created_at"].(time.Time)
		return left.After(right)
	})
	return items, nil
}

func (s *Server) persistImageResults(r *http.Request, result map[string]any) int {
	saved := persistImageResultItems(s.cfg.ImagesDir, publicBaseURL(r), result)
	if saved == 0 && s.cfg.DebugLogging() {
		log.Printf("image_archive saved=0 path=%s items=%d", r.URL.Path, imageResultCount(result))
	}
	return saved
}

func persistImageResultItems(imagesDir string, baseURL string, result map[string]any) int {
	saved := 0
	forEachImageResultItem(result, func(item map[string]any) {
		if persistImageItem(imagesDir, baseURL, item) {
			saved++
		}
	})
	return saved
}

func persistImageItem(root string, baseURL string, item map[string]any) bool {
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
	log.Printf("image_archive saved path=%s bytes=%d url_configured=%t", rel, len(data), baseURL != "")
	return true
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
