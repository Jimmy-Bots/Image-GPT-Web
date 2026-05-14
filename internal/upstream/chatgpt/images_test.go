package chatgpt

import (
	"errors"
	"strings"
	"testing"
)

func TestDetectImageMetadataRejectsUnsupportedFormatsWithFriendlyMessage(t *testing.T) {
	_, _, _, _, err := detectImageMetadata([]byte("not-an-image"), "image/webp")
	if err == nil {
		t.Fatalf("expected error")
	}
	message := err.Error()
	if !strings.Contains(message, "please use PNG, JPG, or GIF") {
		t.Fatalf("expected friendly format hint, got %q", message)
	}
}

func TestFilterReferenceCandidateIDsRemovesReferenceAndShortPrefixIDs(t *testing.T) {
	references := []uploadedImage{
		{FileID: "file_0000000032847206a560b1ae308733e0"},
	}
	input := []string{
		"file_0000000032847206a560b1ae308733e0",
		"file_0000000032847206a560b1ae",
		"file_00000000999988887777666655554444",
	}
	got := filterReferenceCandidateIDs(input, references)
	if len(got) != 1 {
		t.Fatalf("expected 1 filtered id, got %d (%v)", len(got), got)
	}
	if got[0] != "file_00000000999988887777666655554444" {
		t.Fatalf("expected only real result id to remain, got %v", got)
	}
}

func TestImagePromptAdjustErrorWrapsSentinel(t *testing.T) {
	err := &ImagePromptAdjustError{Text: "请提供更具体的修改方向"}
	if !strings.Contains(err.Error(), "请提供更具体的修改方向") {
		t.Fatalf("unexpected error text: %q", err.Error())
	}
	if !errors.Is(err, ErrImagePromptAdjust) {
		t.Fatalf("expected ImagePromptAdjustError to match ErrImagePromptAdjust")
	}
}

func TestIsImagePromptAdjustTextMatchesPolicyMessage(t *testing.T) {
	text := "非常抱歉，生成的图片可能违反了我们的内容政策。如果你认为此判断有误，请重试或修改提示语。"
	if !IsImagePromptAdjustText(text) {
		t.Fatalf("expected policy text to be treated as prompt-adjust text")
	}
}
