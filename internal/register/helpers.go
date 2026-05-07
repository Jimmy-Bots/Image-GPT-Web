package register

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func parseJSONMap(body []byte) map[string]any {
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func decodeJWTPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return map[string]any{}
	}
	payload := parts[1]
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return map[string]any{}
	}
	return parseJSONMap(data)
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func mapValue(value any) map[string]any {
	out, ok := value.(map[string]any)
	if !ok || out == nil {
		return map[string]any{}
	}
	return out
}

func sliceValue(value any) []any {
	out, ok := value.([]any)
	if !ok || out == nil {
		return []any{}
	}
	return out
}

func parseOAuthCallback(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	query := parsed.Query()
	code := strings.TrimSpace(query.Get("code"))
	if code == "" {
		return nil
	}
	return map[string]string{
		"code":  code,
		"state": strings.TrimSpace(query.Get("state")),
		"scope": strings.TrimSpace(query.Get("scope")),
	}
}

func randUint64String(src RandomSource) string {
	n, err := randomBigInt(src, new(big.Int).Lsh(big.NewInt(1), 63))
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return n.String()
}

func randomBigInt(src RandomSource, max *big.Int) (*big.Int, error) {
	if max.Sign() <= 0 {
		return big.NewInt(0), nil
	}
	byteLen := len(max.Bytes())
	if byteLen == 0 {
		byteLen = 1
	}
	buf := make([]byte, byteLen)
	for {
		if _, err := src.Read(buf); err != nil {
			return nil, err
		}
		n := new(big.Int).SetBytes(buf)
		if n.Cmp(max) < 0 {
			return n, nil
		}
	}
}

func withTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

var otpCodePattern = regexp.MustCompile(`(?:Verification code|code is|代码为|验证码)[:\s]*(\d{6})`)
var genericSixDigitPattern = regexp.MustCompile(`\b(\d{6})\b`)

func extractOTPCode(values ...string) string {
	content := strings.Join(values, "\n")
	if content == "" {
		return ""
	}
	if match := otpCodePattern.FindStringSubmatch(content); len(match) == 2 && match[1] != "177010" {
		return match[1]
	}
	matches := genericSixDigitPattern.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if len(match) == 2 && match[1] != "177010" {
			return match[1]
		}
	}
	return ""
}
