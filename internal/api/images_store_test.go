package api

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistReferenceImageReusesExistingReferenceByHash(t *testing.T) {
	tempDir := t.TempDir()
	referenceRoot := filepath.Join(tempDir, "references")
	imagesRoot := filepath.Join(tempDir, "images")
	if err := os.MkdirAll(referenceRoot, 0o755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	if err := os.MkdirAll(imagesRoot, 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}

	data := []byte("same-image-binary")
	first, err := persistReferenceImage(imagesRoot, referenceRoot, UploadImage{
		Name:        "sample.png",
		ContentType: "image/png",
		Data:        data,
	}, "user-1")
	if err != nil {
		t.Fatalf("persist first reference: %v", err)
	}
	second, err := persistReferenceImage(imagesRoot, referenceRoot, UploadImage{
		Name:        "another-name.png",
		ContentType: "image/png",
		Data:        data,
	}, "user-2")
	if err != nil {
		t.Fatalf("persist second reference: %v", err)
	}
	if first != second {
		t.Fatalf("expected second persist to reuse existing reference, first=%q second=%q", first, second)
	}
}

func TestPersistReferenceImageCreatesGeneratedReferenceIndexWhenSourceMatches(t *testing.T) {
	tempDir := t.TempDir()
	referenceRoot := filepath.Join(tempDir, "references")
	imagesRoot := filepath.Join(tempDir, "images")
	if err := os.MkdirAll(referenceRoot, 0o755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	generatedRel := filepath.ToSlash(filepath.Join("2026", "05", "14", "generated.png"))
	generatedPath := filepath.Join(imagesRoot, filepath.FromSlash(generatedRel))
	if err := os.MkdirAll(filepath.Dir(generatedPath), 0o755); err != nil {
		t.Fatalf("mkdir generated parent: %v", err)
	}
	data := []byte("generated-image-binary")
	if err := os.WriteFile(generatedPath, data, 0o644); err != nil {
		t.Fatalf("write generated image: %v", err)
	}

	stored, err := persistReferenceImage(imagesRoot, referenceRoot, UploadImage{
		Name:        "generated.png",
		ContentType: "image/png",
		Data:        data,
		SourcePath:  generatedRel,
	}, "user-1")
	if err != nil {
		t.Fatalf("persist reference from generated image: %v", err)
	}
	if stored == generatedRel {
		t.Fatalf("expected generated image to create a reference index entry, got direct generated path %q", stored)
	}
	meta := readReferenceMeta(filepath.Join(referenceRoot, filepath.FromSlash(stored)))
	if meta.SourceType != "generated" {
		t.Fatalf("expected source_type generated, got %q", meta.SourceType)
	}
	if meta.SourcePath != generatedRel {
		t.Fatalf("expected source_path %q, got %q", generatedRel, meta.SourcePath)
	}
}

func TestPersistReferenceImageReusesLegacyReferenceWithoutHashInFilename(t *testing.T) {
	tempDir := t.TempDir()
	referenceRoot := filepath.Join(tempDir, "references")
	imagesRoot := filepath.Join(tempDir, "images")
	if err := os.MkdirAll(referenceRoot, 0o755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	if err := os.MkdirAll(imagesRoot, 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	legacyRel := filepath.ToSlash(filepath.Join("legacy", "old-reference-upload.jpg"))
	legacyPath := filepath.Join(referenceRoot, filepath.FromSlash(legacyRel))
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("mkdir legacy parent: %v", err)
	}
	data := []byte("legacy-reference-binary")
	if err := os.WriteFile(legacyPath, data, 0o644); err != nil {
		t.Fatalf("write legacy reference: %v", err)
	}

	stored, err := persistReferenceImage(imagesRoot, referenceRoot, UploadImage{
		Name:        "new-name.jpg",
		ContentType: "image/jpeg",
		Data:        data,
	}, "user-2")
	if err != nil {
		t.Fatalf("persist reference from legacy file: %v", err)
	}
	if stored != legacyRel {
		t.Fatalf("expected legacy reference path to be reused, got %q want %q", stored, legacyRel)
	}
}

func TestNormalizeLegacyReferenceDedupIndexesExistingDuplicates(t *testing.T) {
	tempDir := t.TempDir()
	referenceRoot := filepath.Join(tempDir, "references")
	if err := os.MkdirAll(referenceRoot, 0o755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}

	data := []byte("legacy-duplicate-reference-binary")
	canonicalRel := filepath.ToSlash(filepath.Join("2026", "05", "14", "1715712000_abcdef1234567890.jpg"))
	duplicateRel := filepath.ToSlash(filepath.Join("legacy", "old-upload-copy.jpg"))
	canonicalPath := filepath.Join(referenceRoot, filepath.FromSlash(canonicalRel))
	duplicatePath := filepath.Join(referenceRoot, filepath.FromSlash(duplicateRel))
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o755); err != nil {
		t.Fatalf("mkdir canonical parent: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(duplicatePath), 0o755); err != nil {
		t.Fatalf("mkdir duplicate parent: %v", err)
	}
	if err := os.WriteFile(canonicalPath, data, 0o644); err != nil {
		t.Fatalf("write canonical reference: %v", err)
	}
	if err := os.WriteFile(duplicatePath, data, 0o644); err != nil {
		t.Fatalf("write duplicate reference: %v", err)
	}
	writeReferenceMeta(duplicatePath, storedReferenceMeta{
		OriginalName: "old-upload-copy.jpg",
		ContentType:  "image/jpeg",
		OwnerID:      "user-1",
		SourceType:   "uploaded",
	})

	indexed, err := normalizeLegacyReferenceDedup(referenceRoot)
	if err != nil {
		t.Fatalf("normalize legacy reference dedup: %v", err)
	}
	if indexed != 1 {
		t.Fatalf("expected one duplicate to be indexed, got %d", indexed)
	}

	metaBody, err := os.ReadFile(imageMetaPath(duplicatePath))
	if err != nil {
		t.Fatalf("read duplicate meta: %v", err)
	}
	var meta storedReferenceMeta
	if err := json.Unmarshal(metaBody, &meta); err != nil {
		t.Fatalf("unmarshal duplicate meta: %v", err)
	}
	if meta.CanonicalPath != canonicalRel {
		t.Fatalf("expected canonical path %q, got %q", canonicalRel, meta.CanonicalPath)
	}

	sum := sha256.Sum256(data)
	matched, ok := findExistingReferenceByHash(referenceRoot, sum)
	if !ok {
		t.Fatalf("expected hash lookup to succeed after normalization")
	}
	if matched != canonicalRel {
		t.Fatalf("expected canonical path %q from hash lookup, got %q", canonicalRel, matched)
	}
}

func TestPersistReferenceImageCreatesReferenceIndexForGeneratedSource(t *testing.T) {
	tempDir := t.TempDir()
	referenceRoot := filepath.Join(tempDir, "references")
	imagesRoot := filepath.Join(tempDir, "images")
	if err := os.MkdirAll(referenceRoot, 0o755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	generatedRel := filepath.ToSlash(filepath.Join("2026", "05", "14", "generated.png"))
	generatedPath := filepath.Join(imagesRoot, filepath.FromSlash(generatedRel))
	if err := os.MkdirAll(filepath.Dir(generatedPath), 0o755); err != nil {
		t.Fatalf("mkdir generated parent: %v", err)
	}
	data := []byte("generated-image-reference-index")
	if err := os.WriteFile(generatedPath, data, 0o644); err != nil {
		t.Fatalf("write generated image: %v", err)
	}

	stored, err := persistReferenceImage(imagesRoot, referenceRoot, UploadImage{
		Name:        "generated.png",
		ContentType: "image/png",
		Data:        data,
		SourcePath:  generatedRel,
	}, "user-1")
	if err != nil {
		t.Fatalf("persist generated reference index: %v", err)
	}
	if stored == generatedRel {
		t.Fatalf("expected generated image reference to create a reference entry, got direct source path %q", stored)
	}
	storedPath := filepath.Join(referenceRoot, filepath.FromSlash(stored))
	meta := readReferenceMeta(storedPath)
	if meta.SourceType != "generated" {
		t.Fatalf("expected source_type generated, got %q", meta.SourceType)
	}
	if meta.SourcePath != generatedRel {
		t.Fatalf("expected source_path %q, got %q", generatedRel, meta.SourcePath)
	}
}

func TestBackfillGeneratedReferenceIndexesMigratesImageMetaReference(t *testing.T) {
	tempDir := t.TempDir()
	referenceRoot := filepath.Join(tempDir, "references")
	imagesRoot := filepath.Join(tempDir, "images")
	if err := os.MkdirAll(referenceRoot, 0o755); err != nil {
		t.Fatalf("mkdir references: %v", err)
	}
	generatedRel := filepath.ToSlash(filepath.Join("2026", "05", "14", "generated.png"))
	generatedPath := filepath.Join(imagesRoot, filepath.FromSlash(generatedRel))
	if err := os.MkdirAll(filepath.Dir(generatedPath), 0o755); err != nil {
		t.Fatalf("mkdir generated parent: %v", err)
	}
	data := []byte("legacy-generated-reference-index")
	if err := os.WriteFile(generatedPath, data, 0o644); err != nil {
		t.Fatalf("write generated image: %v", err)
	}
	writeReferenceMeta(generatedPath, storedReferenceMeta{
		OriginalName: "legacy-generated.png",
		ContentType:  "image/png",
		OwnerID:      "user-1",
		SourceType:   "generated",
		SourcePath:   generatedRel,
	})

	indexed, err := backfillGeneratedReferenceIndexes(imagesRoot, referenceRoot)
	if err != nil {
		t.Fatalf("backfill generated reference indexes: %v", err)
	}
	if indexed != 1 {
		t.Fatalf("expected one generated reference to be backfilled, got %d", indexed)
	}
	matched, ok := findExistingGeneratedReferenceBySource(referenceRoot, generatedRel)
	if !ok {
		t.Fatalf("expected generated reference index to be created")
	}
	meta := readReferenceMeta(filepath.Join(referenceRoot, filepath.FromSlash(matched)))
	if meta.SourceType != "generated" || meta.SourcePath != generatedRel {
		t.Fatalf("unexpected generated reference meta: %#v", meta)
	}
}
