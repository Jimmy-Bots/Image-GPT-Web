package register

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

type sentinelGenerator struct {
	deviceID  string
	userAgent string
	sessionID string
	random    RandomSource
}

func newSentinelGenerator(deviceID string, userAgent string, random RandomSource) sentinelGenerator {
	return sentinelGenerator{
		deviceID:  deviceID,
		userAgent: userAgent,
		sessionID: randomID(random, 16),
		random:    random,
	}
}

func (g sentinelGenerator) requirementsToken(now time.Time) string {
	payload := g.config(now, 1, int64(5+g.random.Intn(46)))
	return "gAAAAAC" + g.base64JSON(payload)
}

func (g sentinelGenerator) proofToken(seed string, difficulty string, now time.Time) string {
	value, err := chatgptBuildProof(seed, difficulty, g.userAgent)
	if err != nil {
		return g.requirementsToken(now)
	}
	return value
}

func (g sentinelGenerator) config(now time.Time, attempts int, elapsedMillis int64) []any {
	perfNow := 1000 + g.random.Float64()*49000
	return []any{
		"1920x1080",
		now.UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)"),
		4294705152,
		attempts,
		g.userAgent,
		"https://sentinel.openai.com/sentinel/20260124ceb8/sdk.js",
		nil,
		nil,
		"en-US",
		elapsedMillis,
		randomChoiceString(g.random, []string{"vendorSub-undefined", "plugins-undefined", "mimeTypes-undefined", "hardwareConcurrency-undefined"}),
		randomChoiceString(g.random, []string{"location", "implementation", "URL", "documentURI", "compatMode"}),
		randomChoiceString(g.random, []string{"Object", "Function", "Array", "Number", "parseFloat", "undefined"}),
		perfNow,
		g.sessionID,
		"",
		[]int{4, 8, 12, 16}[g.random.Intn(4)],
		now.UnixMilli() - int64(perfNow),
	}
}

func (g sentinelGenerator) base64JSON(value any) string {
	payload, _ := json.Marshal(value)
	return base64.StdEncoding.EncodeToString(payload)
}

func buildSentinelToken(ctx context.Context, client HTTPClient, cfg Config, deviceID string, flow string, random RandomSource, now func() time.Time) (string, error) {
	generator := newSentinelGenerator(deviceID, cfg.UserAgent, random)
	body, _ := json.Marshal(map[string]any{
		"p":    generator.requirementsToken(now()),
		"id":   deviceID,
		"flow": flow,
	})
	headers := map[string]string{
		"Content-Type":       "text/plain;charset=UTF-8",
		"Referer":            "https://sentinel.openai.com/backend-api/sentinel/frame.html",
		"Origin":             "https://sentinel.openai.com",
		"User-Agent":         cfg.UserAgent,
		"sec-ch-ua":          cfg.SecCHUA,
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": `"Windows"`,
	}
	reqCtx, cancel := withTimeout(ctx, cfg.SentinelTimeout)
	defer cancel()
	resp, err := doWithRetry(reqCtx, client, cfg.LocalRetryAttempts, func() (*fhttp.Request, error) {
		return newRequest("POST", "https://sentinel.openai.com/backend-api/sentinel/req", headers, body)
	})
	if err != nil {
		return "", err
	}
	data := resp.JSON()
	token := stringValue(data["token"])
	if resp.StatusCode != 200 || token == "" {
		return "", fmt.Errorf("sentinel_req_failed_%d", resp.StatusCode)
	}
	proof := mapValue(data["proofofwork"])
	pValue := generator.requirementsToken(now())
	if required, _ := proof["required"].(bool); required && stringValue(proof["seed"]) != "" {
		pValue = generator.proofToken(stringValue(proof["seed"]), stringValue(proof["difficulty"]), now())
	}
	payload, _ := json.Marshal(map[string]string{
		"p":    pValue,
		"t":    "",
		"c":    token,
		"id":   deviceID,
		"flow": flow,
	})
	return string(payload), nil
}

func chatgptBuildProof(seed string, difficulty string, userAgent string) (string, error) {
	token, err := chatgptBuildProofToken(seed, difficulty, userAgent)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(token, "~S") {
		return token, nil
	}
	return token + "~S", nil
}

func chatgptBuildProofToken(seed string, difficulty string, userAgent string) (string, error) {
	return chatgptBuildProofTokenInternal(seed, difficulty, userAgent)
}

func randomChoiceString(src RandomSource, items []string) string {
	return items[src.Intn(len(items))]
}

func traceHeaders(src RandomSource) map[string]string {
	traceID := randUint64String(src)
	parentID := randUint64String(src)
	parentInt, _ := strconv.ParseUint(parentID, 10, 64)
	return map[string]string{
		"traceparent":                 fmt.Sprintf("00-%s-%016x-01", randomID(src, 16), parentInt),
		"tracestate":                  "dd=s:1;o:rum",
		"x-datadog-origin":            "rum",
		"x-datadog-parent-id":         parentID,
		"x-datadog-sampling-priority": "1",
		"x-datadog-trace-id":          traceID,
	}
}
