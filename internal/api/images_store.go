package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"math"
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

type referenceImagesDeleteRequest struct {
	Paths []string `json:"paths"`
}

const imageMetaSuffix = ".meta.json"

type storedImageMeta struct {
	Prompt        string    `json:"prompt,omitempty"`
	RevisedPrompt string    `json:"revised_prompt,omitempty"`
	ArchivedAt    time.Time `json:"archived_at,omitempty"`
	OwnerID       string    `json:"owner_id,omitempty"`
}

type storedReferenceMeta struct {
	OriginalName  string    `json:"original_name,omitempty"`
	ContentType   string    `json:"content_type,omitempty"`
	StoredAt      time.Time `json:"stored_at,omitempty"`
	OwnerID       string    `json:"owner_id,omitempty"`
	SourceType    string    `json:"source_type,omitempty"`
	SourcePath    string    `json:"source_path,omitempty"`
	CanonicalPath string    `json:"canonical_path,omitempty"`
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

func (s *Server) handleListReferenceImages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query")))
	sortMode := strings.TrimSpace(r.URL.Query().Get("sort"))
	dateScope := strings.TrimSpace(r.URL.Query().Get("date_scope"))
	items, err := s.listStoredReferenceImages(query, sortMode, dateScope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reference_image_list_failed", err.Error())
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

func (s *Server) handleDeleteReferenceImages(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var req referenceImagesDeleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	removed := 0
	for _, rel := range compactStrings(req.Paths) {
		path, ok := safeJoin(s.cfg.ReferenceImagesDir, rel)
		if !ok {
			continue
		}
		if err := os.Remove(path); err == nil {
			_ = os.Remove(imageMetaPath(path))
			removed++
		}
	}
	s.addAuditLog(r, identity, "image", "删除参考图暂存", map[string]any{
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
			"preview_url":    publicBaseURL(r) + "/images-preview/" + rel,
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

func (s *Server) listStoredReferenceImages(query string, sortMode string, dateScope string) ([]map[string]any, error) {
	items := make([]map[string]any, 0)
	err := filepath.WalkDir(s.cfg.ReferenceImagesDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), imageMetaSuffix) {
			return nil
		}
		rel, err := filepath.Rel(s.cfg.ReferenceImagesDir, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		meta := readReferenceMeta(path)
		if strings.TrimSpace(meta.OwnerID) == "" {
			return nil
		}
		if strings.TrimSpace(meta.CanonicalPath) != "" {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !matchImageDateScope(info.ModTime(), dateScope) {
			return nil
		}
		if query != "" {
			haystack := strings.ToLower(strings.Join([]string{
				entry.Name(),
				rel,
				meta.OriginalName,
				meta.ContentType,
				meta.OwnerID,
			}, " "))
			if !strings.Contains(haystack, query) {
				return nil
			}
		}
		items = append(items, map[string]any{
			"path":          rel,
			"name":          entry.Name(),
			"url":           "/reference-images/" + rel,
			"preview_url":   "/reference-images-preview/" + rel,
			"size":          info.Size(),
			"created_at":    info.ModTime().UTC(),
			"owner_id":      meta.OwnerID,
			"original_name": meta.OriginalName,
			"content_type":  meta.ContentType,
			"source_type":   meta.SourceType,
			"source_path":   meta.SourcePath,
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
		item["preview_url"] = strings.TrimRight(baseURL, "/") + "/images-preview/" + rel
	} else {
		item["url"] = "/images/" + rel
		item["preview_url"] = "/images-preview/" + rel
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

func readReferenceMeta(path string) storedReferenceMeta {
	body, err := os.ReadFile(imageMetaPath(path))
	if err != nil || len(body) == 0 {
		return storedReferenceMeta{}
	}
	var meta storedReferenceMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return storedReferenceMeta{}
	}
	return meta
}

func writeReferenceMeta(path string, meta storedReferenceMeta) {
	payload, err := json.Marshal(meta)
	if err != nil {
		return
	}
	_ = os.WriteFile(imageMetaPath(path), payload, 0o644)
}

func persistReferenceImage(imagesRoot string, referenceRoot string, image UploadImage, ownerID string) (string, error) {
	if len(image.Data) == 0 {
		return "", fmt.Errorf("reference image data is empty")
	}
	sum := sha256.Sum256(image.Data)
	if rel, ok := resolveGeneratedImageReference(imagesRoot, image.SourcePath, image.Data); ok {
		return persistGeneratedReferenceIndex(imagesRoot, referenceRoot, rel, image, ownerID)
	}
	if rel, ok := findExistingReferenceByHash(referenceRoot, sum); ok {
		return rel, nil
	}
	now := time.Now().UTC()
	ext := filepath.Ext(strings.TrimSpace(image.Name))
	if ext == "" {
		ext = contentTypeExtension(image.ContentType)
	}
	if ext == "" {
		ext = ".bin"
	}
	rel := filepath.ToSlash(filepath.Join(
		now.Format("2006"),
		now.Format("01"),
		now.Format("02"),
		fmt.Sprintf("%d_%s%s", now.Unix(), hex.EncodeToString(sum[:])[:16], ext),
	))
	path := filepath.Join(referenceRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, image.Data, 0o644); err != nil {
		return "", err
	}
	writeReferenceMeta(path, storedReferenceMeta{
		OriginalName: strings.TrimSpace(image.Name),
		ContentType:  strings.TrimSpace(image.ContentType),
		StoredAt:     now,
		OwnerID:      strings.TrimSpace(ownerID),
		SourceType:   "uploaded",
	})
	return rel, nil
}

func resolveGeneratedImageReference(imagesRoot string, sourcePath string, data []byte) (string, bool) {
	rel := strings.TrimSpace(sourcePath)
	if rel == "" {
		return "", false
	}
	path, ok := safeJoin(imagesRoot, rel)
	if !ok {
		return "", false
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if sha256.Sum256(existing) != sha256.Sum256(data) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func persistGeneratedReferenceIndex(imagesRoot string, referenceRoot string, sourceRel string, image UploadImage, ownerID string) (string, error) {
	if existing, ok := findExistingGeneratedReferenceBySource(referenceRoot, sourceRel); ok {
		return existing, nil
	}
	sourcePath, ok := safeJoin(imagesRoot, sourceRel)
	if !ok {
		return "", fmt.Errorf("invalid generated image source path")
	}
	now := time.Now().UTC()
	sum := sha256.Sum256(image.Data)
	ext := filepath.Ext(strings.TrimSpace(image.Name))
	if ext == "" {
		ext = filepath.Ext(sourceRel)
	}
	if ext == "" {
		ext = contentTypeExtension(image.ContentType)
	}
	if ext == "" {
		ext = ".bin"
	}
	rel := filepath.ToSlash(filepath.Join(
		now.Format("2006"),
		now.Format("01"),
		now.Format("02"),
		fmt.Sprintf("%d_%s%s", now.Unix(), hex.EncodeToString(sum[:])[:16], ext),
	))
	path := filepath.Join(referenceRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.Link(sourcePath, path); err != nil {
		if writeErr := os.WriteFile(path, image.Data, 0o644); writeErr != nil {
			return "", err
		}
	}
	writeReferenceMeta(path, storedReferenceMeta{
		OriginalName: strings.TrimSpace(image.Name),
		ContentType:  strings.TrimSpace(image.ContentType),
		StoredAt:     now,
		OwnerID:      strings.TrimSpace(ownerID),
		SourceType:   "generated",
		SourcePath:   sourceRel,
	})
	return rel, nil
}

func findExistingGeneratedReferenceBySource(root string, sourceRel string) (string, bool) {
	target := filepath.ToSlash(strings.TrimSpace(sourceRel))
	if target == "" {
		return "", false
	}
	var matched string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil {
			return err
		}
		if entry.IsDir() || strings.HasSuffix(strings.ToLower(entry.Name()), imageMetaSuffix) {
			return nil
		}
		meta := readReferenceMeta(path)
		if meta.SourceType != "generated" {
			return nil
		}
		if filepath.ToSlash(strings.TrimSpace(meta.SourcePath)) != target {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		matched = filepath.ToSlash(rel)
		return filepath.SkipAll
	})
	if err != nil || matched == "" {
		return "", false
	}
	return matched, true
}

func findExistingReferenceByHash(root string, sum [32]byte) (string, bool) {
	prefix := hex.EncodeToString(sum[:])[:16]
	var matched string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), imageMetaSuffix) {
			return nil
		}
		if !strings.Contains(entry.Name(), prefix) {
			return nil
		}
		meta := readReferenceMeta(path)
		if canonical := strings.TrimSpace(meta.CanonicalPath); canonical != "" {
			matched = filepath.ToSlash(canonical)
			return filepath.SkipAll
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil || sha256.Sum256(existing) != sum {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		matched = filepath.ToSlash(rel)
		return filepath.SkipAll
	})
	if err == nil && strings.TrimSpace(matched) != "" {
		return matched, true
	}
	// Compatibility fallback for older cached references whose filenames did not
	// embed the content-hash prefix. This is slower, so we only run it after the
	// fast path misses.
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), imageMetaSuffix) {
			return nil
		}
		meta := readReferenceMeta(path)
		if canonical := strings.TrimSpace(meta.CanonicalPath); canonical != "" {
			matched = filepath.ToSlash(canonical)
			return filepath.SkipAll
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil || sha256.Sum256(existing) != sum {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		matched = filepath.ToSlash(rel)
		return filepath.SkipAll
	})
	if err != nil || strings.TrimSpace(matched) == "" {
		return "", false
	}
	return matched, true
}

func normalizeLegacyReferenceDedup(root string) (int, error) {
	type candidate struct {
		rel     string
		path    string
		modTime time.Time
	}
	canonicals := map[[32]byte]candidate{}
	duplicates := make([]candidate, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil {
			return err
		}
		if entry.IsDir() || strings.HasSuffix(strings.ToLower(entry.Name()), imageMetaSuffix) {
			return nil
		}
		meta := readReferenceMeta(path)
		if meta.SourceType == "generated" && strings.TrimSpace(meta.SourcePath) != "" {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || len(data) == 0 {
			return nil
		}
		sum := sha256.Sum256(data)
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		current := candidate{
			rel:     filepath.ToSlash(rel),
			path:    path,
			modTime: info.ModTime(),
		}
		if existing, ok := canonicals[sum]; ok {
			if current.rel < existing.rel {
				duplicates = append(duplicates, existing)
				canonicals[sum] = current
			} else {
				duplicates = append(duplicates, current)
			}
			return nil
		}
		canonicals[sum] = current
		return nil
	})
	if err != nil {
		return 0, err
	}
	indexed := 0
	for _, item := range duplicates {
		meta := readReferenceMeta(item.path)
		if strings.TrimSpace(meta.CanonicalPath) != "" {
			continue
		}
		data, readErr := os.ReadFile(item.path)
		if readErr != nil || len(data) == 0 {
			continue
		}
		sum := sha256.Sum256(data)
		canonical := canonicals[sum]
		if canonical.rel == "" || canonical.rel == item.rel {
			continue
		}
		meta.CanonicalPath = canonical.rel
		if strings.TrimSpace(meta.SourceType) == "" {
			meta.SourceType = "uploaded"
		}
		writeReferenceMeta(item.path, meta)
		indexed++
	}
	return indexed, nil
}

func backfillGeneratedReferenceIndexes(imagesRoot string, referenceRoot string) (int, error) {
	if _, err := os.Stat(imagesRoot); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	indexed := 0
	err := filepath.WalkDir(imagesRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil {
			return err
		}
		if entry.IsDir() || strings.HasSuffix(strings.ToLower(entry.Name()), imageMetaSuffix) {
			return nil
		}
		meta := readReferenceMeta(path)
		if meta.SourceType != "generated" {
			return nil
		}
		rel, relErr := filepath.Rel(imagesRoot, path)
		if relErr != nil {
			return nil
		}
		sourceRel := filepath.ToSlash(rel)
		if _, ok := findExistingGeneratedReferenceBySource(referenceRoot, sourceRel); ok {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || len(data) == 0 {
			return nil
		}
		name := strings.TrimSpace(meta.OriginalName)
		if name == "" {
			name = filepath.Base(sourceRel)
		}
		if _, persistErr := persistGeneratedReferenceIndex(imagesRoot, referenceRoot, sourceRel, UploadImage{
			Name:        name,
			ContentType: strings.TrimSpace(meta.ContentType),
			Data:        data,
		}, strings.TrimSpace(meta.OwnerID)); persistErr != nil {
			return nil
		}
		indexed++
		return nil
	})
	if err != nil {
		return 0, err
	}
	return indexed, nil
}

func contentTypeExtension(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
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

func fitImageBounds(width int, height int, maxEdge int) (int, int) {
	if width <= 0 || height <= 0 {
		return width, height
	}
	longest := width
	if height > longest {
		longest = height
	}
	if longest <= maxEdge {
		return width, height
	}
	scale := float64(maxEdge) / float64(longest)
	nextWidth := int(math.Round(float64(width) * scale))
	nextHeight := int(math.Round(float64(height) * scale))
	if nextWidth < 1 {
		nextWidth = 1
	}
	if nextHeight < 1 {
		nextHeight = 1
	}
	return nextWidth, nextHeight
}

func resizeNearest(src image.Image, width int, height int) image.Image {
	bounds := src.Bounds()
	srcWidth := bounds.Dx()
	srcHeight := bounds.Dy()
	if width <= 0 || height <= 0 || srcWidth <= 0 || srcHeight <= 0 {
		return src
	}
	if width == srcWidth && height == srcHeight {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		srcY := bounds.Min.Y + y*srcHeight/height
		for x := 0; x < width; x++ {
			srcX := bounds.Min.X + x*srcWidth/width
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}
	return dst
}

func previewCachePath(previewRoot string, kind string, rel string) string {
	base := strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel))
	return filepath.Join(previewRoot, filepath.FromSlash(kind), filepath.FromSlash(base+".jpg"))
}

func ensurePreviewImage(sourcePath string, previewRoot string, kind string, rel string, maxEdge int) (string, error) {
	cachePath := previewCachePath(previewRoot, kind, rel)
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return "", err
	}
	if cacheInfo, err := os.Stat(cachePath); err == nil && !cacheInfo.ModTime().Before(sourceInfo.ModTime()) {
		return cachePath, nil
	}
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	width, height := fitImageBounds(img.Bounds().Dx(), img.Bounds().Dy(), maxEdge)
	preview := resizeNearest(img, width, height)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return "", err
	}
	file, err := os.Create(cachePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if err := jpeg.Encode(file, preview, &jpeg.Options{Quality: 78}); err != nil {
		return "", err
	}
	_ = os.Chtimes(cachePath, time.Now(), sourceInfo.ModTime())
	return cachePath, nil
}
