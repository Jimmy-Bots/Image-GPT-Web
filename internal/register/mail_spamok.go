package register

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"encoding/hex"
	"cmp"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	defaultSpamOKBaseURL = "https://spamok.com"
	defaultSpamOKAPIBase = "https://api.spamok.com/v2"
	defaultSpamOKDomain  = "spamok.com"
)

type SpamOKConfig struct {
	BaseURL        string
	APIBaseURL     string
	Domain         string
	RequestTimeout time.Duration
	WaitTimeout    time.Duration
	WaitInterval   time.Duration
	UserAgent      string
	HTTPClient     *http.Client
}

func (c SpamOKConfig) withDefaults() SpamOKConfig {
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = defaultRequestTimeout
	}
	if c.WaitTimeout <= 0 {
		c.WaitTimeout = defaultWaitTimeout
	}
	if c.WaitInterval <= 0 {
		c.WaitInterval = defaultWaitInterval
	}
	if c.UserAgent == "" {
		c.UserAgent = "Mozilla/5.0"
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = defaultSpamOKBaseURL
	}
	if strings.TrimSpace(c.APIBaseURL) == "" {
		c.APIBaseURL = defaultSpamOKAPIBase
	}
	if strings.TrimSpace(c.Domain) == "" {
		c.Domain = defaultSpamOKDomain
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.APIBaseURL = strings.TrimRight(strings.TrimSpace(c.APIBaseURL), "/")
	c.Domain = strings.ToLower(strings.TrimSpace(c.Domain))
	return c
}

type SpamOKMailProvider struct {
	cfg    SpamOKConfig
	client *http.Client
	random RandomSource
}

func NewSpamOKMailProvider(cfg SpamOKConfig, random RandomSource) (*SpamOKMailProvider, error) {
	cfg = cfg.withDefaults()
	if cfg.BaseURL == "" {
		return nil, errors.New("spamok base url is required")
	}
	if cfg.Domain == "" {
		return nil, errors.New("spamok domain is required")
	}
	if random == nil {
		random = newDefaultRandomSource()
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.RequestTimeout}
	} else if client.Timeout <= 0 {
		client.Timeout = cfg.RequestTimeout
	}
	return &SpamOKMailProvider{
		cfg:    cfg,
		client: client,
		random: random,
	}, nil
}

func (p *SpamOKMailProvider) CreateMailbox(ctx context.Context) (Mailbox, error) {
	prefix := randomMailboxName(p.random)
	address := prefix + "@" + p.cfg.Domain
	return Mailbox{
		Address: address,
		Meta: map[string]any{
			"provider":     "spamok",
			"prefix":       prefix,
			"base_url":     p.cfg.BaseURL,
			"api_base_url": p.cfg.APIBaseURL,
			"domain":       p.cfg.Domain,
			"mailbox_url":  p.cfg.BaseURL + "/" + prefix,
		},
	}, nil
}

func (p *SpamOKMailProvider) WaitForCode(ctx context.Context, mailbox Mailbox) (string, error) {
	waitCtx := ctx
	if _, ok := ctx.Deadline(); !ok && p.cfg.WaitTimeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, p.cfg.WaitTimeout)
		defer cancel()
	}
	prefix, err := p.mailboxPrefix(mailbox)
	if err != nil {
		return "", err
	}
	seen := mailboxSeenRefs(mailbox)
	for {
		code, ref, err := p.fetchLatestCode(waitCtx, prefix, seen)
		if err != nil {
			return "", err
		}
		if code != "" {
			markMailboxSeenRef(mailbox, ref)
			return code, nil
		}
		if ref != "" {
			seen[ref] = struct{}{}
		}
		select {
		case <-waitCtx.Done():
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return "", nil
			}
			return "", waitCtx.Err()
		case <-time.After(p.cfg.WaitInterval):
		}
	}
}

func (p *SpamOKMailProvider) mailboxPrefix(mailbox Mailbox) (string, error) {
	prefix := strings.TrimSpace(stringValue(mailbox.Meta["prefix"]))
	if prefix == "" {
		localPart, _, _ := strings.Cut(strings.TrimSpace(mailbox.Address), "@")
		prefix = strings.TrimSpace(localPart)
	}
	if prefix == "" {
		return "", errors.New("spamok mailbox prefix is required")
	}
	return prefix, nil
}

func (p *SpamOKMailProvider) fetchLatestCode(ctx context.Context, prefix string, seen map[string]struct{}) (string, string, error) {
	body, err := p.requestAPI(ctx, prefix)
	if err != nil {
		return "", "", err
	}
	code, ref, found, err := p.extractCodeFromAPI(body, seen)
	if err == nil {
		if found {
			return code, ref, nil
		}
		return "", ref, nil
	}
	body, err = p.requestHTML(ctx, prefix)
	if err != nil {
		return "", "", err
	}
	ref = spamOKBodyRef(body)
	if _, ok := seen[ref]; ok {
		return "", ref, nil
	}
	code = extractSpamOKCodeFromHTML(string(body))
	if code != "" {
		return code, ref, nil
	}
	return "", ref, nil
}

func (p *SpamOKMailProvider) requestAPI(ctx context.Context, prefix string) ([]byte, error) {
	target := p.cfg.APIBaseURL + "/EmailBox/" + url.PathEscape(prefix)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6")
	req.Header.Set("Origin", p.cfg.BaseURL)
	req.Header.Set("Referer", p.cfg.BaseURL+"/")
	req.Header.Set("Priority", "u=1, i")
	req.Header.Set("Sec-CH-UA", `"Microsoft Edge";v="147", "Not.A/Brand";v="8", "Chromium";v="147"`)
	req.Header.Set("Sec-CH-UA-Mobile", "?0")
	req.Header.Set("Sec-CH-UA-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("User-Agent", spamOKAPIUserAgent(p.cfg.UserAgent))
	req.Header.Set("X-Asdasd-Platform-Id", "blazor-en-us")
	req.Header.Set("X-Asdasd-Platform-Version", "blazor-1.0.0")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spamok api request failed: GET /EmailBox/%s, http %d, body=%s", prefix, resp.StatusCode, string(limitBytes(body, 300)))
	}
	return body, nil
}

func spamOKAPIUserAgent(current string) string {
	current = strings.TrimSpace(current)
	if current != "" && strings.Contains(strings.ToLower(current), "edg/") {
		return current
	}
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36 Edg/147.0.0.0"
}

func (p *SpamOKMailProvider) requestHTML(ctx context.Context, prefix string) ([]byte, error) {
	target := p.cfg.BaseURL + "/" + url.PathEscape(prefix)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spamok request failed: GET /%s, http %d, body=%s", prefix, resp.StatusCode, string(limitBytes(body, 300)))
	}
	return body, nil
}

type spamOKEmailBox struct {
	Address string            `json:"address"`
	Mails   []spamOKEmailItem `json:"mails"`
}

type spamOKEmailItem struct {
	ID             any    `json:"id"`
	Subject        string `json:"subject"`
	MessagePreview string `json:"messagePreview"`
	FromDisplay    string `json:"fromDisplay"`
	FromDomain     string `json:"fromDomain"`
	FromLocal      string `json:"fromLocal"`
	ToDomain       string `json:"toDomain"`
	ToLocal        string `json:"toLocal"`
	Date           string `json:"date"`
	DateSystem     string `json:"dateSystem"`
}

func (p *SpamOKMailProvider) extractCodeFromAPI(body []byte, seen map[string]struct{}) (string, string, bool, error) {
	var payload spamOKEmailBox
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", false, err
	}
	type candidate struct {
		ref  string
		at   time.Time
		code string
	}
	candidates := make([]candidate, 0, len(payload.Mails))
	for _, item := range payload.Mails {
		ref := strings.TrimSpace(stringValue(item.ID))
		if ref == "" {
			ref = spamOKBodyRef([]byte(item.Subject + "\n" + item.MessagePreview + "\n" + item.DateSystem))
		}
		itemTime := parseMailTime(item.DateSystem)
		if itemTime.Equal(time.Unix(0, 0).UTC()) {
			itemTime = parseMailTime(item.Date)
		}
		candidates = append(candidates, candidate{
			ref:  ref,
			at:   itemTime,
			code: extractOTPCode(item.Subject, item.MessagePreview),
		})
	}
	if len(candidates) == 0 {
		return "", "", true, nil
	}
	slices.SortStableFunc(candidates, func(left, right candidate) int {
		return cmp.Compare(right.at.UnixNano(), left.at.UnixNano())
	})
	latestRef := strings.TrimSpace(candidates[0].ref)
	if latestRef == "" {
		return "", "", true, nil
	}
	for _, item := range candidates[1:] {
		if ref := strings.TrimSpace(item.ref); ref != "" {
			seen[ref] = struct{}{}
		}
	}
	for _, item := range candidates {
		ref := strings.TrimSpace(item.ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		if item.code != "" {
			return item.code, ref, true, nil
		}
		return "", ref, true, nil
	}
	return "", latestRef, true, nil
}

func spamOKBodyRef(body []byte) string {
	sum := sha1.Sum(body)
	return hex.EncodeToString(sum[:])
}

var spamOKScriptPattern = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
var spamOKStylePattern = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
var spamOKCommentPattern = regexp.MustCompile(`(?is)<!--.*?-->`)
var spamOKTagPattern = regexp.MustCompile(`(?is)<[^>]+>`)
var spamOKWhitespacePattern = regexp.MustCompile(`\s+`)

func extractSpamOKCodeFromHTML(source string) string {
	text := spamOKVisibleText(source)
	if text == "" {
		return ""
	}
	return extractOTPCode(text)
}

func spamOKVisibleText(source string) string {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	text := spamOKScriptPattern.ReplaceAllString(source, " ")
	text = spamOKStylePattern.ReplaceAllString(text, " ")
	text = spamOKCommentPattern.ReplaceAllString(text, " ")
	text = spamOKTagPattern.ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	text = spamOKWhitespacePattern.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}
