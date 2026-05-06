package api

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"gpt-image-web/internal/auth"
)

type registerCodeEntry struct {
	Code      string
	SentAt    time.Time
	ExpiresAt time.Time
}

type registerCodeStore struct {
	mu   sync.Mutex
	ttl  time.Duration
	data map[string]registerCodeEntry
}

func newRegisterCodeStore(ttl time.Duration) *registerCodeStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &registerCodeStore{
		ttl:  ttl,
		data: make(map[string]registerCodeEntry),
	}
}

func (s *registerCodeStore) Put(email string, code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[strings.ToLower(strings.TrimSpace(email))] = registerCodeEntry{
		Code:      strings.TrimSpace(code),
		SentAt:    time.Now(),
		ExpiresAt: time.Now().Add(s.ttl),
	}
}

func (s *registerCodeStore) CooldownRemaining(email string, cooldown time.Duration) time.Duration {
	if cooldown <= 0 {
		return 0
	}
	key := strings.ToLower(strings.TrimSpace(email))
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.data[key]
	if !ok || entry.SentAt.IsZero() {
		return 0
	}
	remaining := cooldown - time.Since(entry.SentAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (s *registerCodeStore) Verify(email string, code string) error {
	key := strings.ToLower(strings.TrimSpace(email))
	input := strings.TrimSpace(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.data[key]
	if !ok {
		return fmt.Errorf("verification code not found or expired")
	}
	if time.Now().After(entry.ExpiresAt) {
		delete(s.data, key)
		return fmt.Errorf("verification code expired")
	}
	if entry.Code != input {
		return fmt.Errorf("verification code is invalid")
	}
	delete(s.data, key)
	return nil
}

func (s *registerCodeStore) Delete(email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, strings.ToLower(strings.TrimSpace(email)))
}

func generateVerificationCode() string {
	raw := auth.RandomID(6)
	digits := make([]byte, 0, 6)
	for i := 0; i < len(raw) && len(digits) < 6; i++ {
		digits = append(digits, '0'+(raw[i]%10))
	}
	for len(digits) < 6 {
		digits = append(digits, '0')
	}
	return string(digits)
}
