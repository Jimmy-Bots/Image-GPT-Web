package api

import (
	"testing"

	"gpt-image-web/internal/register"
)

func TestReloginRequiresMailboxOTP(t *testing.T) {
	if !reloginRequiresMailboxOTP(errString("login-only flow requires a mailbox provider for otp")) {
		t.Fatal("expected mailbox otp error to be detected")
	}
	if reloginRequiresMailboxOTP(errString("password_verify_http_401")) {
		t.Fatal("did not expect non-otp relogin error to match")
	}
}

func TestRegisterMailProviderForReloginSelectsSpamOKByEmail(t *testing.T) {
	provider := registerMailProviderForRelogin(map[string]any{
		"register": map[string]any{
			"mail": map[string]any{
				"provider":             "inbucket",
				"inbucket_api_base":    "http://inbucket.test",
				"inbucket_domains":     []any{"example.com"},
				"spamok_base_url":      "https://spamok.com",
				"spamok_api_base_url":  "https://api.spamok.com/v2",
			},
		},
	}, "abc@spamok.com")
	if provider == nil {
		t.Fatal("expected provider")
	}
	if _, ok := provider.(*register.SpamOKMailProvider); !ok {
		t.Fatalf("expected SpamOKMailProvider, got %T", provider)
	}
}

func TestRegisterMailProviderForReloginSelectsInbucketByEmail(t *testing.T) {
	provider := registerMailProviderForRelogin(map[string]any{
		"register": map[string]any{
			"mail": map[string]any{
				"inbucket_api_base": "http://inbucket.test",
				"inbucket_domains":  []any{"example.com"},
			},
		},
	}, "abc@example.com")
	if provider == nil {
		t.Fatal("expected provider")
	}
	if _, ok := provider.(*register.InbucketMailProvider); !ok {
		t.Fatalf("expected InbucketMailProvider, got %T", provider)
	}
}

type errString string

func (e errString) Error() string {
	return string(e)
}
