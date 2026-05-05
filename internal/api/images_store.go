package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
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

func (s *Server) persistImageResults(r *http.Request, result map[string]any) {
	persistImageResultItems(s.cfg.ImagesDir, publicBaseURL(r), result)
}

func persistImageResultItems(imagesDir string, baseURL string, result map[string]any) {
	items, ok := result["data"].([]map[string]any)
	if !ok {
		if rawItems, ok := result["data"].([]any); ok {
			for _, raw := range rawItems {
				if item, ok := raw.(map[string]any); ok {
					persistImageItem(imagesDir, baseURL, item)
				}
			}
		}
		return
	}
	for _, item := range items {
		persistImageItem(imagesDir, baseURL, item)
	}
}

func persistImageItem(root string, baseURL string, item map[string]any) {
	if baseURL == "" {
		return
	}
	b64 := strings.TrimSpace(stringFromAny(item["b64_json"], ""))
	if b64 == "" {
		return
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) == 0 {
		return
	}
	sum := sha256.Sum256(data)
	now := time.Now().UTC()
	rel := filepath.ToSlash(filepath.Join(now.Format("2006"), now.Format("01"), now.Format("02"), fmt.Sprintf("%d_%s.png", now.Unix(), hex.EncodeToString(sum[:])[:16])))
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return
	}
	item["url"] = strings.TrimRight(baseURL, "/") + "/images/" + rel
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
