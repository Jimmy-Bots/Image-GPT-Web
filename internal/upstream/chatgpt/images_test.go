package chatgpt

import (
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
