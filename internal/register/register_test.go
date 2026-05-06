package register

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fakeMailProvider struct {
	create func(context.Context) (Mailbox, error)
	wait   func(context.Context, Mailbox) (string, error)
}

func (f fakeMailProvider) CreateMailbox(ctx context.Context) (Mailbox, error) {
	return f.create(ctx)
}

func (f fakeMailProvider) WaitForCode(ctx context.Context, mailbox Mailbox) (string, error) {
	return f.wait(ctx, mailbox)
}

type fakeAccountRepo struct{}

func (fakeAccountRepo) AddAccessToken(context.Context, string, string) (bool, error) {
	return true, nil
}
func (fakeAccountRepo) RefreshAccount(context.Context, string) error { return nil }
func (fakeAccountRepo) ListAccounts(context.Context) ([]AccountSnapshot, error) {
	return []AccountSnapshot{
		{Status: "正常", Quota: 2},
		{Status: "限流", Quota: 0},
		{Status: "正常", Quota: 3},
	}, nil
}

func TestWaitForCodeReturnsTimeoutErrorOnEmptyCode(t *testing.T) {
	registrar, err := New(Options{
		MailProvider: fakeMailProvider{
			create: func(context.Context) (Mailbox, error) {
				return Mailbox{Address: "test@example.com"}, nil
			},
			wait: func(context.Context, Mailbox) (string, error) {
				return "", nil
			},
		},
		AccountRepo: fakeAccountRepo{},
		Config: Config{
			WaitTimeout:  5 * time.Millisecond,
			WaitInterval: 1 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = registrar.waitForCode(context.Background(), Mailbox{Address: "test@example.com"})
	if !errors.Is(err, ErrCodeTimeout) {
		t.Fatalf("expected ErrCodeTimeout, got %v", err)
	}
}

func TestRunnerPoolMetrics(t *testing.T) {
	registrar, err := New(Options{
		MailProvider: fakeMailProvider{
			create: func(context.Context) (Mailbox, error) { return Mailbox{}, nil },
			wait:   func(context.Context, Mailbox) (string, error) { return "", nil },
		},
		AccountRepo: fakeAccountRepo{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	runner, err := NewRunner(registrar, fakeAccountRepo{}, nil, nil)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	metrics, err := runner.poolMetrics(context.Background())
	if err != nil {
		t.Fatalf("poolMetrics() error = %v", err)
	}
	if metrics.CurrentAvailable != 2 || metrics.CurrentQuota != 5 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}

type deterministicRandom struct {
	ints  []int
	index int
}

func (d *deterministicRandom) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	if len(d.ints) == 0 {
		return 0
	}
	value := d.ints[d.index%len(d.ints)] % n
	d.index++
	return value
}

func (d *deterministicRandom) Float64() float64 { return 0.5 }

func (d *deterministicRandom) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(i + 1)
	}
	return len(p), nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestInbucketMailProviderCreatesMailboxAndWaitsForCode(t *testing.T) {
	var requestedPaths []string
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requestedPaths = append(requestedPaths, r.URL.Path)
			var body string
			status := http.StatusOK
			switch r.URL.Path {
			case "/api/v1/mailbox/aaaaa0a":
				body = `[{"id":"msg-1","date":"2026-05-05T10:00:00Z","subject":"Verify","from":"no-reply@openai.com"}]`
			case "/api/v1/mailbox/aaaaa0a/msg-1":
				body = `{
					"id":"msg-1",
					"date":"2026-05-05T10:00:00Z",
					"subject":"OpenAI Verification code",
					"from":"no-reply@openai.com",
					"header":{"To":"aaaaa0a@aaaa.example.com"},
					"body":{"text":"Your Verification code: 123456","html":"<p>123456</p>"}
				}`
			default:
				status = http.StatusNotFound
				body = `{"error":"not found"}`
			}
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		}),
	}

	provider, err := NewInbucketMailProvider(InbucketConfig{
		APIBase:         "http://inbucket.test",
		Domains:         []string{"example.com"},
		RandomSubdomain: true,
		RequestTimeout:  time.Second,
		WaitTimeout:     time.Second,
		WaitInterval:    10 * time.Millisecond,
		HTTPClient:      client,
	}, &deterministicRandom{})
	if err != nil {
		t.Fatalf("NewInbucketMailProvider() error = %v", err)
	}

	mailbox, err := provider.CreateMailbox(context.Background())
	if err != nil {
		t.Fatalf("CreateMailbox() error = %v", err)
	}
	if mailbox.Address != "aaaaa0a@aaaa.example.com" {
		t.Fatalf("unexpected mailbox address: %s", mailbox.Address)
	}

	code, err := provider.WaitForCode(context.Background(), mailbox)
	if err != nil {
		t.Fatalf("WaitForCode() error = %v", err)
	}
	if code != "123456" {
		t.Fatalf("unexpected code: %s", code)
	}
	joined := strings.Join(requestedPaths, ",")
	if !strings.Contains(joined, "/api/v1/mailbox/aaaaa0a") || !strings.Contains(joined, "/api/v1/mailbox/aaaaa0a/msg-1") {
		t.Fatalf("unexpected requested paths: %s", joined)
	}
}
