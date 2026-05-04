package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gpt-image-web/internal/domain"
)

type SessionClaims struct {
	SubjectID string      `json:"sub"`
	Role      domain.Role `json:"role"`
	IssuedAt  int64       `json:"iat"`
	ExpiresAt int64       `json:"exp"`
	Nonce     string      `json:"nonce"`
}

type SessionSigner struct {
	secret []byte
	ttl    time.Duration
}

func NewSessionSigner(secret string, ttl time.Duration) *SessionSigner {
	return &SessionSigner{secret: []byte(secret), ttl: ttl}
}

func (s *SessionSigner) Sign(subjectID string, role domain.Role) (string, time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(s.ttl)
	claims := SessionClaims{
		SubjectID: subjectID,
		Role:      role,
		IssuedAt:  now.Unix(),
		ExpiresAt: expiresAt.Unix(),
		Nonce:     RandomID(12),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(payload)
	sigPart := s.sign(payloadPart)
	return "session." + payloadPart + "." + sigPart, expiresAt, nil
}

func (s *SessionSigner) Verify(token string) (SessionClaims, bool) {
	if !strings.HasPrefix(token, "session.") {
		return SessionClaims{}, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return SessionClaims{}, false
	}
	expected := s.sign(parts[1])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[2])) != 1 {
		return SessionClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return SessionClaims{}, false
	}
	var claims SessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return SessionClaims{}, false
	}
	if claims.SubjectID == "" || claims.ExpiresAt < time.Now().UTC().Unix() {
		return SessionClaims{}, false
	}
	return claims, true
}

func (s *SessionSigner) sign(payloadPart string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payloadPart))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func HashAPIKey(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func NewAPIKey() string {
	return "sk-" + RandomID(32)
}

func RandomID(size int) string {
	if size < 8 {
		size = 8
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("random id: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func ExtractBearer(header string) (string, error) {
	scheme, value, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(value) == "" {
		return "", errors.New("missing bearer token")
	}
	return strings.TrimSpace(value), nil
}
