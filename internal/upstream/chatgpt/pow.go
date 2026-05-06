package chatgpt

import (
	"crypto/sha3"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"time"

	"gpt-image-web/internal/auth"
)

const defaultPOWScript = "https://chatgpt.com/backend-api/sentinel/sdk.js"

var (
	buildScriptPattern = regexp.MustCompile(`c/[^/]*/_`)
	htmlBuildPattern   = regexp.MustCompile(`<html[^>]*data-build="([^"]*)"`)
	scriptSrcPattern   = regexp.MustCompile(`<script[^>]+src="([^"]+)"`)
)

func parsePOWResources(html string) ([]string, string) {
	matches := scriptSrcPattern.FindAllStringSubmatch(html, -1)
	sources := make([]string, 0, len(matches))
	dataBuild := ""
	for _, match := range matches {
		if len(match) < 2 || match[1] == "" {
			continue
		}
		sources = append(sources, match[1])
		if dataBuild == "" {
			dataBuild = buildScriptPattern.FindString(match[1])
		}
	}
	if len(sources) == 0 {
		sources = []string{defaultPOWScript}
	}
	if dataBuild == "" {
		if match := htmlBuildPattern.FindStringSubmatch(html); len(match) == 2 {
			dataBuild = match[1]
		}
	}
	return sources, dataBuild
}

func buildLegacyRequirementsToken(userAgent string, scriptSources []string, dataBuild string) string {
	seed := fmt.Sprintf("%0.16f", rand.Float64())
	config := buildPOWConfig(userAgent, scriptSources, dataBuild)
	answer, _ := powGenerate(seed, "0fffff", config, 500000)
	return "gAAAAAC" + answer
}

func buildProofToken(seed string, difficulty string, userAgent string, scriptSources []string, dataBuild string) (string, error) {
	config := buildPOWConfig(userAgent, scriptSources, dataBuild)
	answer, solved := powGenerate(seed, difficulty, config, 500000)
	if !solved {
		return "", fmt.Errorf("failed to solve proof token: difficulty=%s", difficulty)
	}
	return "gAAAAAB" + answer, nil
}

func BuildProofToken(seed string, difficulty string, userAgent string) (string, error) {
	return buildProofToken(seed, difficulty, userAgent, nil, "")
}

func buildPOWConfig(userAgent string, scriptSources []string, dataBuild string) []any {
	if len(scriptSources) == 0 {
		scriptSources = []string{defaultPOWScript}
	}
	cores := []int{8, 16, 24, 32}
	documentKeys := []string{"_reactListeningo743lnnpvdg", "location"}
	navigatorKeys := []string{
		"registerProtocolHandler−function registerProtocolHandler() { [native code] }",
		"storage−[object StorageManager]",
		"locks−[object LockManager]",
		"appCodeName−Mozilla",
		"permissions−[object Permissions]",
		"share−function share() { [native code] }",
		"webdriver−false",
		"managed−[object NavigatorManagedData]",
		"canShare−function canShare() { [native code] }",
		"vendor−Google Inc.",
		"mediaDevices−[object MediaDevices]",
		"vibrate−function vibrate() { [native code] }",
		"storageBuckets−[object StorageBucketManager]",
		"mediaCapabilities−[object MediaCapabilities]",
		"cookieEnabled−true",
		"product−Gecko",
		"onLine−true",
		"language−zh-CN",
		"hardwareConcurrency−32",
	}
	windowKeys := []string{
		"0", "window", "self", "document", "name", "location", "customElements", "history",
		"navigation", "innerWidth", "innerHeight", "scrollX", "scrollY", "screenX", "screenY",
		"outerWidth", "outerHeight", "devicePixelRatio", "screen", "chrome", "navigator",
		"performance", "crypto", "indexedDB", "sessionStorage", "localStorage", "scheduler",
		"alert", "atob", "btoa", "fetch", "matchMedia", "postMessage", "queueMicrotask",
		"requestAnimationFrame", "setInterval", "setTimeout", "caches", "__NEXT_DATA__",
	}
	perf := float64(time.Now().UnixNano()%1_000_000_000) / 1_000_000
	return []any{
		randomChoice([]int{3000, 4000, 5000}),
		legacyParseTime(),
		4294705152,
		0,
		userAgent,
		randomChoice(scriptSources),
		dataBuild,
		"en-US",
		"en-US,es-US,en,es",
		0,
		randomChoice(navigatorKeys),
		randomChoice(documentKeys),
		randomChoice(windowKeys),
		perf,
		auth.RandomID(16),
		"",
		randomChoice(cores),
		float64(time.Now().UnixMilli()) - perf,
	}
}

func powGenerate(seed string, difficulty string, config []any, limit int) (string, bool) {
	target, err := hex.DecodeString(difficulty)
	if err != nil || len(target) == 0 {
		return fallbackPOW(seed), false
	}
	diffLen := len(difficulty) / 2
	seedBytes := []byte(seed)
	static1 := []byte(strings.TrimSuffix(mustJSON(config[:3]), "]") + ",")
	mid := mustJSON(config[4:9])
	static2 := []byte("," + strings.TrimSuffix(strings.TrimPrefix(mid, "["), "]") + ",")
	tail := mustJSON(config[10:])
	static3 := []byte("," + strings.TrimPrefix(tail, "["))
	for i := 0; i < limit; i++ {
		finalJSON := append([]byte{}, static1...)
		finalJSON = append(finalJSON, []byte(fmt.Sprint(i))...)
		finalJSON = append(finalJSON, static2...)
		finalJSON = append(finalJSON, []byte(fmt.Sprint(i>>1))...)
		finalJSON = append(finalJSON, static3...)
		encoded := []byte(base64.StdEncoding.EncodeToString(finalJSON))
		sum := sha3.Sum512(append(seedBytes, encoded...))
		if bytesLE(sum[:diffLen], target[:diffLen]) {
			return string(encoded), true
		}
	}
	return fallbackPOW(seed), false
}

func bytesLE(left []byte, right []byte) bool {
	for i := range left {
		if left[i] < right[i] {
			return true
		}
		if left[i] > right[i] {
			return false
		}
	}
	return true
}

func fallbackPOW(seed string) string {
	payload, _ := json.Marshal(seed)
	return "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D" + base64.StdEncoding.EncodeToString(payload)
}

func mustJSON(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(payload)
}

func legacyParseTime() string {
	location := time.FixedZone("Eastern Standard Time", -5*60*60)
	return time.Now().In(location).Format("Mon Jan 02 2006 15:04:05") + " GMT-0500 (Eastern Standard Time)"
}

func randomChoice[T any](items []T) T {
	return items[rand.Intn(len(items))]
}
