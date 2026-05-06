package api

import (
	"testing"
	"time"
)

func TestRegisterCodeStoreVerifyConsumesCode(t *testing.T) {
	store := newRegisterCodeStore(time.Minute)
	store.Put("Test@Example.com", "123456")

	if err := store.Verify("test@example.com", "123456"); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if err := store.Verify("test@example.com", "123456"); err == nil {
		t.Fatalf("expected code to be consumed after successful verification")
	}
}

func TestRegisterCodeStoreExpiresCode(t *testing.T) {
	store := newRegisterCodeStore(time.Millisecond)
	store.Put("test@example.com", "123456")
	time.Sleep(5 * time.Millisecond)

	if err := store.Verify("test@example.com", "123456"); err == nil {
		t.Fatalf("expected expired code verification to fail")
	}
}
