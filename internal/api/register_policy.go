package api

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"gpt-image-web/internal/domain"
)

type publicRegisterStatus struct {
	Enabled             bool     `json:"enabled"`
	NeedsBootstrap      bool     `json:"needs_bootstrap"`
	OrdinaryUsers       int      `json:"ordinary_users"`
	MaxOrdinaryUsers    int      `json:"max_ordinary_users"`
	RemainingOrdinary   int      `json:"remaining_ordinary"`
	AllowedEmailDomains []string `json:"allowed_email_domains"`
	CodeCooldownSeconds int      `json:"code_cooldown_seconds"`
	CanRegister         bool     `json:"can_register"`
	DisabledReason      string   `json:"disabled_reason,omitempty"`
}

const defaultRegisterCodeCooldownSeconds = 60

func (s *Server) publicRegisterStatus(ctx context.Context) (publicRegisterStatus, error) {
	count, err := s.store.CountUsers(ctx)
	if err != nil {
		return publicRegisterStatus{}, err
	}
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return publicRegisterStatus{}, err
	}
	ordinaryUsers, err := s.countOrdinaryUsers(ctx)
	if err != nil {
		return publicRegisterStatus{}, err
	}
	maxOrdinaryUsers := maxInt(0, intMapValue(settings, "register_max_ordinary_users"))
	remaining := -1
	if maxOrdinaryUsers > 0 {
		remaining = maxInt(0, maxOrdinaryUsers-ordinaryUsers)
	}
	allowedDomains := registerAllowedEmailDomains(settings)
	needsBootstrap := count == 0
	enabled := publicRegistrationEnabled(needsBootstrap, s.cfg.AllowPublicRegistration, settings, maxOrdinaryUsers)
	canRegister := enabled && (maxOrdinaryUsers == 0 || remaining > 0)
	status := publicRegisterStatus{
		Enabled:             enabled,
		NeedsBootstrap:      needsBootstrap,
		OrdinaryUsers:       ordinaryUsers,
		MaxOrdinaryUsers:    maxOrdinaryUsers,
		RemainingOrdinary:   remaining,
		AllowedEmailDomains: allowedDomains,
		CodeCooldownSeconds: registerCodeCooldownSeconds(settings),
		CanRegister:         canRegister,
	}
	if !enabled {
		status.DisabledReason = "public registration is disabled"
	} else if maxOrdinaryUsers > 0 && remaining <= 0 {
		status.DisabledReason = "registration quota is full"
	}
	return status, nil
}

func publicRegistrationEnabled(needsBootstrap bool, configEnabled bool, settings map[string]any, maxOrdinaryUsers int) bool {
	_ = maxOrdinaryUsers
	if needsBootstrap || configEnabled || boolMapValue(settings, "public_registration_enabled") {
		return true
	}
	return false
}

func (s *Server) countOrdinaryUsers(ctx context.Context) (int, error) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, user := range users {
		if user.Role == domain.RoleUser {
			total++
		}
	}
	return total, nil
}

func registerAllowedEmailDomains(settings map[string]any) []string {
	raw := settings["register_allowed_email_domains"]
	switch typed := raw.(type) {
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			value := strings.ToLower(strings.TrimSpace(stringFromAny(item, "")))
			value = strings.TrimPrefix(value, "@")
			if value != "" {
				out = append(out, value)
			}
		}
		return compactStrings(out)
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			item = strings.ToLower(strings.TrimSpace(item))
			item = strings.TrimPrefix(item, "@")
			if item != "" {
				out = append(out, item)
			}
		}
		return compactStrings(out)
	case string:
		lines := strings.Split(typed, "\n")
		out := make([]string, 0, len(lines))
		for _, item := range lines {
			item = strings.ToLower(strings.TrimSpace(item))
			item = strings.TrimPrefix(item, "@")
			if item != "" {
				out = append(out, item)
			}
		}
		return compactStrings(out)
	default:
		return nil
	}
}

func registerCodeCooldownSeconds(settings map[string]any) int {
	seconds := intMapValue(settings, "register_code_cooldown_seconds")
	if seconds <= 0 {
		return defaultRegisterCodeCooldownSeconds
	}
	return seconds
}

func validateRegisterEmail(email string, settings map[string]any) error {
	email = normalizeRegistrationEmail(email)
	if email == "" {
		return fmt.Errorf("email is required")
	}
	address, err := mail.ParseAddress(email)
	if err != nil {
		return fmt.Errorf("invalid email address")
	}
	parts := strings.Split(strings.ToLower(strings.TrimSpace(address.Address)), "@")
	if len(parts) != 2 {
		return fmt.Errorf("invalid email address")
	}
	domain := strings.TrimSpace(parts[1])
	allowedDomains := registerAllowedEmailDomains(settings)
	if len(allowedDomains) == 0 {
		return nil
	}
	for _, item := range allowedDomains {
		if domain == item {
			return nil
		}
	}
	return fmt.Errorf("email domain is not allowed")
}

func (s *Server) ensurePublicRegistrationAllowed(ctx context.Context, settings map[string]any) error {
	status, err := s.publicRegisterStatus(ctx)
	if err != nil {
		return err
	}
	if !status.Enabled {
		return fmt.Errorf("public registration is disabled")
	}
	if !status.CanRegister {
		return fmt.Errorf("registration quota is full")
	}
	return nil
}

func (s *Server) registerCodeCooldown(email string, settings map[string]any) time.Duration {
	seconds := registerCodeCooldownSeconds(settings)
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
