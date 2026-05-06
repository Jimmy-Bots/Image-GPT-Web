package api

import "testing"

func TestValidateRegisterEmail(t *testing.T) {
	settings := map[string]any{
		"register_allowed_email_domains": []any{"zju.edu.cn", "@gmail.com"},
	}

	if err := validateRegisterEmail("test@zju.edu.cn", settings); err != nil {
		t.Fatalf("expected zju email to pass: %v", err)
	}
	if err := validateRegisterEmail("test@gmail.com", settings); err != nil {
		t.Fatalf("expected gmail email to pass: %v", err)
	}
	if err := validateRegisterEmail("test@example.com", settings); err == nil {
		t.Fatalf("expected example.com to be rejected")
	}
}

func TestRegisterCodeCooldownSeconds(t *testing.T) {
	if got := registerCodeCooldownSeconds(map[string]any{}); got != 60 {
		t.Fatalf("expected default cooldown 60, got %d", got)
	}
	if got := registerCodeCooldownSeconds(map[string]any{"register_code_cooldown_seconds": 90}); got != 90 {
		t.Fatalf("expected custom cooldown 90, got %d", got)
	}
}

func TestPublicRegistrationEnabled(t *testing.T) {
	if !publicRegistrationEnabled(false, false, map[string]any{"public_registration_enabled": true}, 0) {
		t.Fatalf("expected explicit public_registration_enabled to enable public registration")
	}
	if publicRegistrationEnabled(false, false, map[string]any{"register_max_ordinary_users": 5}, 5) {
		t.Fatalf("expected max ordinary users alone not to enable public registration")
	}
	if publicRegistrationEnabled(false, false, map[string]any{}, 0) {
		t.Fatalf("expected registration to stay disabled without config, settings, or cap")
	}
}
