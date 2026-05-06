package register

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type InbucketConfig struct {
	APIBase         string
	Domains         []string
	RandomSubdomain bool
	RequestTimeout  time.Duration
	WaitTimeout     time.Duration
	WaitInterval    time.Duration
	UserAgent       string
	HTTPClient      *http.Client
}

func (c InbucketConfig) withDefaults() InbucketConfig {
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
	c.APIBase = strings.TrimRight(strings.TrimSpace(c.APIBase), "/")
	normalized := make([]string, 0, len(c.Domains))
	for _, item := range c.Domains {
		item = strings.TrimSpace(item)
		if item != "" {
			normalized = append(normalized, item)
		}
	}
	c.Domains = normalized
	return c
}

type InbucketMailProvider struct {
	cfg    InbucketConfig
	client *http.Client
	random RandomSource
}

func NewInbucketMailProvider(cfg InbucketConfig, random RandomSource) (*InbucketMailProvider, error) {
	cfg = cfg.withDefaults()
	if cfg.APIBase == "" {
		return nil, errors.New("inbucket api base is required")
	}
	if len(cfg.Domains) == 0 {
		return nil, errors.New("inbucket requires at least one domain")
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
	return &InbucketMailProvider{
		cfg:    cfg,
		client: client,
		random: random,
	}, nil
}

func (p *InbucketMailProvider) CreateMailbox(ctx context.Context) (Mailbox, error) {
	localPart := randomMailboxName(p.random)
	baseDomain := p.cfg.Domains[p.random.Intn(len(p.cfg.Domains))]
	domain := baseDomain
	if p.cfg.RandomSubdomain {
		domain = randomSubdomainLabel(p.random) + "." + baseDomain
	}
	address := localPart + "@" + domain
	return Mailbox{
		Address: address,
		Meta: map[string]any{
			"provider":         "inbucket",
			"api_base":         p.cfg.APIBase,
			"base_domain":      baseDomain,
			"mailbox_name":     localPart,
			"random_subdomain": p.cfg.RandomSubdomain,
		},
	}, nil
}

func (p *InbucketMailProvider) WaitForCode(ctx context.Context, mailbox Mailbox) (string, error) {
	waitCtx := ctx
	if _, ok := ctx.Deadline(); !ok && p.cfg.WaitTimeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, p.cfg.WaitTimeout)
		defer cancel()
	}
	seen := make(map[string]struct{})
	for {
		code, messageID, err := p.fetchLatestCode(waitCtx, mailbox, seen)
		if err != nil {
			return "", err
		}
		if code != "" {
			return code, nil
		}
		if messageID != "" {
			seen[messageID] = struct{}{}
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

type inbucketMailboxMessage struct {
	ID      string `json:"id"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
	From    string `json:"from"`
}

type inbucketMessageDetail struct {
	ID      string         `json:"id"`
	Date    string         `json:"date"`
	Subject string         `json:"subject"`
	From    string         `json:"from"`
	Header  map[string]any `json:"header"`
	Body    map[string]any `json:"body"`
}

func (p *InbucketMailProvider) fetchLatestCode(ctx context.Context, mailbox Mailbox, seen map[string]struct{}) (string, string, error) {
	mailboxName := strings.TrimSpace(stringValue(mailbox.Meta["mailbox_name"]))
	if mailboxName == "" {
		localPart, _, _ := strings.Cut(strings.TrimSpace(mailbox.Address), "@")
		mailboxName = strings.TrimSpace(localPart)
	}
	if mailboxName == "" {
		return "", "", errors.New("inbucket mailbox name is required")
	}
	items, err := p.listMailbox(ctx, mailboxName)
	if err != nil {
		return "", "", err
	}
	sort.Slice(items, func(i, j int) bool {
		left := parseMailTime(items[i].Date)
		right := parseMailTime(items[j].Date)
		if left.Equal(right) {
			return items[i].ID > items[j].ID
		}
		return left.After(right)
	})
	for _, item := range items {
		if item.ID == "" {
			continue
		}
		if _, ok := seen[item.ID]; ok {
			continue
		}
		detail, err := p.getMessage(ctx, mailboxName, item.ID)
		if err != nil {
			return "", "", err
		}
		if !p.messageMatchesAddress(detail, mailbox.Address) {
			seen[item.ID] = struct{}{}
			continue
		}
		textContent := strings.TrimSpace(stringValue(detail.Body["text"]))
		htmlContent := strings.TrimSpace(stringValue(detail.Body["html"]))
		code := extractOTPCode(detail.Subject, textContent, htmlContent)
		if code != "" {
			return code, item.ID, nil
		}
		seen[item.ID] = struct{}{}
	}
	return "", "", nil
}

func (p *InbucketMailProvider) listMailbox(ctx context.Context, mailboxName string) ([]inbucketMailboxMessage, error) {
	path := "/api/v1/mailbox/" + url.PathEscape(mailboxName)
	body, err := p.request(ctx, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	var items []inbucketMailboxMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (p *InbucketMailProvider) getMessage(ctx context.Context, mailboxName string, messageID string) (inbucketMessageDetail, error) {
	path := "/api/v1/mailbox/" + url.PathEscape(mailboxName) + "/" + url.PathEscape(messageID)
	body, err := p.request(ctx, http.MethodGet, path)
	if err != nil {
		return inbucketMessageDetail{}, err
	}
	var detail inbucketMessageDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		return inbucketMessageDetail{}, err
	}
	return detail, nil
}

func (p *InbucketMailProvider) request(ctx context.Context, method string, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.cfg.APIBase+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
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
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("inbucket request failed: %s %s, http %d, body=%s", method, path, resp.StatusCode, string(limitBytes(body, 300)))
	}
	if resp.StatusCode == http.StatusNoContent {
		return []byte(`{}`), nil
	}
	return body, nil
}

func (p *InbucketMailProvider) messageMatchesAddress(detail inbucketMessageDetail, address string) bool {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" {
		return true
	}
	headerTo := detail.Header["To"]
	switch value := headerTo.(type) {
	case string:
		return strings.Contains(strings.ToLower(value), address)
	case []any:
		for _, item := range value {
			if strings.Contains(strings.ToLower(stringValue(item)), address) {
				return true
			}
		}
	case []string:
		for _, item := range value {
			if strings.Contains(strings.ToLower(item), address) {
				return true
			}
		}
	}
	return headerTo == nil
}

func randomMailboxName(src RandomSource) string {
	letters := "abcdefghijklmnopqrstuvwxyz"
	digits := "0123456789"
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteByte(letters[src.Intn(len(letters))])
	}
	for i := 0; i < 1+src.Intn(3); i++ {
		b.WriteByte(digits[src.Intn(len(digits))])
	}
	for i := 0; i < 1+src.Intn(3); i++ {
		b.WriteByte(letters[src.Intn(len(letters))])
	}
	return b.String()
}

func randomSubdomainLabel(src RandomSource) string {
	chars := "abcdefghijklmnopqrstuvwxyz0123456789"
	size := 4 + src.Intn(7)
	var b strings.Builder
	for i := 0; i < size; i++ {
		b.WriteByte(chars[src.Intn(len(chars))])
	}
	return b.String()
}

func parseMailTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Unix(0, 0).UTC()
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC()
	}
	if parsed, err := time.Parse(time.RFC1123Z, value); err == nil {
		return parsed.UTC()
	}
	if parsed, err := time.Parse(time.RFC1123, value); err == nil {
		return parsed.UTC()
	}
	return time.Unix(0, 0).UTC()
}

func limitBytes(body []byte, max int) []byte {
	if len(body) <= max {
		return body
	}
	return body[:max]
}
