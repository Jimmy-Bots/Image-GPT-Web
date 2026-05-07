package register

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
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

type sequenceAccountRepo struct {
	mu      sync.Mutex
	metrics []PoolMetrics
	index   int
}

func (r *sequenceAccountRepo) AddAccessToken(context.Context, string, string) (bool, error) {
	return true, nil
}

func (r *sequenceAccountRepo) RefreshAccount(context.Context, string) error { return nil }

func (r *sequenceAccountRepo) ListAccounts(context.Context) ([]AccountSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := PoolMetrics{}
	if len(r.metrics) > 0 {
		if r.index >= len(r.metrics) {
			current = r.metrics[len(r.metrics)-1]
		} else {
			current = r.metrics[r.index]
			r.index++
		}
	}
	items := make([]AccountSnapshot, 0, current.CurrentAvailable)
	for i := 0; i < current.CurrentAvailable; i++ {
		quota := 0
		if i == 0 {
			quota = current.CurrentQuota
		}
		items = append(items, AccountSnapshot{Status: "正常", Quota: quota})
	}
	return items, nil
}

func TestNewLoginOnlyWithMailUsesProvidedMailProvider(t *testing.T) {
	expected := fakeMailProvider{
		create: func(context.Context) (Mailbox, error) {
			return Mailbox{}, nil
		},
		wait: func(context.Context, Mailbox) (string, error) {
			return "123456", nil
		},
	}
	loginOnly, err := NewLoginOnlyWithMail(Config{}, expected)
	if err != nil {
		t.Fatalf("NewLoginOnlyWithMail() error = %v", err)
	}
	if loginOnly.mail == nil {
		t.Fatal("expected login-only mail provider to be set")
	}
	if _, ok := loginOnly.mail.(fakeMailProvider); !ok {
		t.Fatalf("expected fakeMailProvider, got %T", loginOnly.mail)
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

func TestNewRunnerFactoryCreatesIndependentRegistrarPerWorker(t *testing.T) {
	var factoryCalls atomic.Int32
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	repo := fakeAccountRepo{}

	runner, err := NewRunnerFactory(func() (*Registrar, error) {
		factoryCalls.Add(1)
		registrar, err := New(Options{
			MailProvider: fakeMailProvider{
				create: func(context.Context) (Mailbox, error) { return Mailbox{}, nil },
				wait:   func(context.Context, Mailbox) (string, error) { return "", nil },
			},
			AccountRepo: repo,
		})
		if err != nil {
			return nil, err
		}
		registrar.logSink = LogSinkFunc(func(ctx context.Context, level string, summary string, detail map[string]any) error {
			current := concurrent.Add(1)
			for {
				seen := maxConcurrent.Load()
				if current <= seen || maxConcurrent.CompareAndSwap(seen, current) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			concurrent.Add(-1)
			return errors.New("stop after start")
		})
		return registrar, nil
	}, repo, nil, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatalf("NewRunnerFactory() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, runErr := runner.Run(ctx, BatchConfig{
		Mode:          RegisterModeTotal,
		Total:         2,
		Threads:       2,
		CheckInterval: 10 * time.Millisecond,
	})
	if runErr == nil {
		t.Fatal("expected runner to fail after injected log sink error")
	}
	if factoryCalls.Load() < 2 {
		t.Fatalf("expected factory to be called per worker, got %d", factoryCalls.Load())
	}
	if maxConcurrent.Load() < 1 {
		t.Fatalf("expected worker execution, max=%d", maxConcurrent.Load())
	}
}

func TestRunnerAvailableModeKeepsMonitoringAndResumes(t *testing.T) {
	repo := &sequenceAccountRepo{
		metrics: []PoolMetrics{
			{CurrentQuota: 10, CurrentAvailable: 0},
			{CurrentQuota: 10, CurrentAvailable: 2},
			{CurrentQuota: 10, CurrentAvailable: 2},
			{CurrentQuota: 10, CurrentAvailable: 0},
			{CurrentQuota: 10, CurrentAvailable: 0},
		},
	}
	var registerCalls atomic.Int32
	runner, err := newRunnerWithRegisterFunc(func(ctx context.Context) (RegisterResult, error) {
		registerCalls.Add(1)
		return RegisterResult{Email: "test@example.com"}, nil
	}, repo, nil, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatalf("newRunnerWithRegisterFunc() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(ctx, BatchConfig{
			Mode:            RegisterModeAvailable,
			TargetAvailable: 2,
			Threads:         1,
			CheckInterval:   5 * time.Millisecond,
		})
		done <- runErr
	}()

	time.Sleep(500 * time.Millisecond)
	cancel()
	runErr := <-done
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", runErr)
	}
	if registerCalls.Load() < 2 {
		t.Fatalf("expected monitoring mode to resume registration, calls=%d", registerCalls.Load())
	}
}

type countingHTTPFactory struct {
	count atomic.Int32
}

func (f *countingHTTPFactory) New(Config) (HTTPClient, error) {
	f.count.Add(1)
	return &failingHTTPClient{}, nil
}

type failingHTTPClient struct{}

func (f *failingHTTPClient) Do(*fhttp.Request) (*fhttp.Response, error) {
	return nil, errors.New("missing workspace id")
}

func (f *failingHTTPClient) SetFollowRedirect(bool) {}
func (f *failingHTTPClient) SetCookies(*url.URL, []*fhttp.Cookie) {}
func (f *failingHTTPClient) GetCookies(*url.URL) []*fhttp.Cookie { return nil }
func (f *failingHTTPClient) CloseIdleConnections() {}

func TestLoginAndExchangeTokensRetriesFreshSessionMultipleTimes(t *testing.T) {
	factory := &countingHTTPFactory{}
	registrar, err := New(Options{
		MailProvider: fakeMailProvider{
			create: func(context.Context) (Mailbox, error) { return Mailbox{Address: "test@example.com"}, nil },
			wait:   func(context.Context, Mailbox) (string, error) { return "123456", nil },
		},
		AccountRepo: fakeAccountRepo{},
		HTTPFactory: factory,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	state := flowState{
		client:   &failingHTTPClient{},
		deviceID: "device",
		cfg:      registrar.cfg,
		random:   registrar.random,
		now:      registrar.now,
		logger:   registrar.logger,
	}
	_, err = registrar.loginAndExchangeTokens(context.Background(), state, "test@example.com", "pass", Mailbox{Address: "test@example.com"})
	if err == nil {
		t.Fatal("expected error")
	}
	if factory.count.Load() != 3 {
		t.Fatalf("expected 3 fresh-session retries, got %d", factory.count.Load())
	}
}

type tokenExchangeRetryFactory struct {
	count atomic.Int32
}

func (f *tokenExchangeRetryFactory) New(Config) (HTTPClient, error) {
	f.count.Add(1)
	return &tokenExchangeRetryClient{}, nil
}

type tokenExchangeRetryClient struct{}

func (c *tokenExchangeRetryClient) Do(req *fhttp.Request) (*fhttp.Response, error) {
	status := 200
	body := ""
	switch {
	case strings.Contains(req.URL.String(), "sentinel.openai.com/backend-api/sentinel/req"):
		body = `{"token":"sentinel_token","proofofwork":{"required":false}}`
	case strings.Contains(req.URL.String(), "/api/accounts/authorize"):
		body = `{}`
	case strings.Contains(req.URL.String(), "/api/accounts/password/verify"):
		body = `{"continue_url":"https://auth.openai.com/api/auth/callback?state=test"}`
	case strings.Contains(req.URL.String(), "/api/accounts/workspace/select"):
		body = `{"continue_url":"https://auth.openai.com/api/auth/callback?state=test","data":{"orgs":[{"id":"org_123","projects":[{"id":"proj_123"}]}]}}`
	case strings.Contains(req.URL.String(), "/api/accounts/organization/select"):
		status = 302
	case strings.Contains(req.URL.String(), "/oauth/token"):
		status = 500
		body = `{"error":"upstream"}`
	default:
		body = `{}`
	}
	resp := &fhttp.Response{
		StatusCode: status,
		Header:     make(fhttp.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
	if strings.Contains(req.URL.String(), "/api/accounts/organization/select") {
		resp.Header.Set("Location", "https://platform.openai.com/auth/callback?code=oauth_code")
	}
	return resp, nil
}

func (c *tokenExchangeRetryClient) SetFollowRedirect(bool) {}
func (c *tokenExchangeRetryClient) SetCookies(*url.URL, []*fhttp.Cookie) {}
func (c *tokenExchangeRetryClient) GetCookies(*url.URL) []*fhttp.Cookie {
	payload, _ := json.Marshal(map[string]any{
		"workspaces": []map[string]any{
			{"id": "ws_test"},
		},
	})
	return []*fhttp.Cookie{
		{
			Name:  "oai-client-auth-session",
			Value: base64.RawURLEncoding.EncodeToString(payload) + ".sig",
		},
	}
}
func (c *tokenExchangeRetryClient) CloseIdleConnections() {}

func TestLoginAndExchangeTokensRetriesOAuthTokenFailures(t *testing.T) {
	factory := &tokenExchangeRetryFactory{}
	registrar, err := New(Options{
		MailProvider: fakeMailProvider{
			create: func(context.Context) (Mailbox, error) { return Mailbox{Address: "test@example.com"}, nil },
			wait:   func(context.Context, Mailbox) (string, error) { return "123456", nil },
		},
		AccountRepo: fakeAccountRepo{},
		HTTPFactory: factory,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stateClient, err := factory.New(registrar.cfg)
	if err != nil {
		t.Fatalf("factory.New() error = %v", err)
	}
	state := flowState{
		client:      stateClient,
		deviceID:    "device",
		cfg:         registrar.cfg,
		httpFactory: registrar.httpFactory,
		random:      registrar.random,
		now:         registrar.now,
		logger:      registrar.logger,
	}
	_, err = registrar.loginAndExchangeTokens(context.Background(), state, "test@example.com", "pass", Mailbox{Address: "test@example.com"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "oauth_token_http_500") {
		t.Fatalf("expected oauth token error, got %v", err)
	}
	if factory.count.Load() != 8 {
		t.Fatalf("expected initial client plus 3 retries with fresh exchange clients, got %d", factory.count.Load())
	}
}

func TestLoginOnlyRetriesOAuthTokenFailuresThreeTimes(t *testing.T) {
	factory := &tokenExchangeRetryFactory{}
	loginOnly, err := NewLoginOnlyWithMail(Config{}, fakeMailProvider{
		create: func(context.Context) (Mailbox, error) { return Mailbox{Address: "test@example.com"}, nil },
		wait:   func(context.Context, Mailbox) (string, error) { return "123456", nil },
	})
	if err != nil {
		t.Fatalf("NewLoginOnlyWithMail() error = %v", err)
	}
	loginOnly.httpFactory = factory
	_, err = loginOnly.LoginAndExchangeTokens(context.Background(), "test@example.com", "pass")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "oauth_token_http_500") {
		t.Fatalf("expected oauth token error, got %v", err)
	}
	if factory.count.Load() != 8 {
		t.Fatalf("expected initial client plus 3 retries with fresh exchange clients, got %d", factory.count.Load())
	}
}

func TestShouldRetryFreshLoginSkipsAccountDeactivated(t *testing.T) {
	err := errors.New("password_verify_http_403: account_deactivated - You do not have an account because it has been deleted or deactivated. If you believe this was an error, please contact us through our help center at help.openai.com.")
	if shouldRetryFreshLogin(err) {
		t.Fatal("expected account_deactivated to be non-retryable")
	}
	if got := classifyFreshLoginRetryReason(err); got != "account_deactivated" {
		t.Fatalf("expected account_deactivated reason, got %q", got)
	}
}

type validateOTPErrorClient struct{}

func (c *validateOTPErrorClient) Do(req *fhttp.Request) (*fhttp.Response, error) {
	status := http.StatusOK
	body := `{}`
	switch {
	case strings.Contains(req.URL.String(), "sentinel.openai.com/backend-api/sentinel/req"):
		body = `{"token":"sentinel_token","proofofwork":{"required":false}}`
	case strings.Contains(req.URL.String(), "/api/accounts/email-otp/validate"):
		if req.Header.Get("openai-sentinel-token") == "" {
			status = http.StatusUnauthorized
			body = `{"error":{"code":"otp_required","message":"need sentinel token"}}`
		} else {
			status = http.StatusForbidden
			body = `{"error":{"code":"otp_expired","message":"verification code expired"}}`
		}
	}
	return &fhttp.Response{
		StatusCode: status,
		Header:     make(fhttp.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (c *validateOTPErrorClient) SetFollowRedirect(bool) {}
func (c *validateOTPErrorClient) SetCookies(*url.URL, []*fhttp.Cookie) {}
func (c *validateOTPErrorClient) GetCookies(*url.URL) []*fhttp.Cookie { return nil }
func (c *validateOTPErrorClient) CloseIdleConnections()               {}

func TestValidateOTPIncludesDetailedError(t *testing.T) {
	state := flowState{
		client:   &validateOTPErrorClient{},
		deviceID: "device-id",
		cfg: Config{
			AuthBaseURL:         "https://auth.openai.com",
			RequestTimeout:      time.Second,
			SentinelTimeout:     time.Second,
			LocalRetryAttempts:  1,
			TokenExchangeTimeout: time.Second,
		},
		httpFactory: defaultHTTPClientFactory{},
		random:      &deterministicRandom{},
		now:         func() time.Time { return time.Now().UTC() },
		logger:      LoggerFunc(func(context.Context, string, string, ...any) {}),
	}

	_, err := state.validateOTP(context.Background(), "123456")
	if err == nil {
		t.Fatal("expected error")
	}
	text := err.Error()
	if !strings.Contains(text, "validate_otp_http_403") {
		t.Fatalf("expected status code in error, got %q", text)
	}
	if !strings.Contains(text, "otp_expired") || !strings.Contains(text, "verification code expired") {
		t.Fatalf("expected detailed otp error, got %q", text)
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
	var requestedMethods []string
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requestedPaths = append(requestedPaths, r.URL.Path)
			requestedMethods = append(requestedMethods, r.Method)
			var body string
			status := http.StatusOK
			switch r.URL.Path {
			case "/api/v1/mailbox/aaaaa0a":
				body = `[{"id":"msg-1","date":"2026-05-05T10:00:00Z","subject":"Verify","from":"no-reply@openai.com"}]`
			case "/api/v1/mailbox/aaaaa0a/msg-1":
				if r.Method == http.MethodDelete {
					status = http.StatusNoContent
					body = ``
				} else {
					body = `{
						"id":"msg-1",
						"date":"2026-05-05T10:00:00Z",
						"subject":"OpenAI Verification code",
						"from":"no-reply@openai.com",
						"header":{"To":"aaaaa0a@aaaa.example.com"},
						"body":{"text":"Your Verification code: 123456","html":"<p>123456</p>"}
					}`
				}
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
	methods := strings.Join(requestedMethods, ",")
	if !strings.Contains(methods, http.MethodDelete) {
		t.Fatalf("expected delete request after consuming code, methods=%s", methods)
	}
}

func TestSpamOKMailProviderCreatesMailboxAndWaitsForCode(t *testing.T) {
	var requestedPaths []string
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requestedPaths = append(requestedPaths, r.URL.Path)
			body := `{"address":"aaaaa0a","subscribed":false,"mails":[{"id":123,"subject":"OpenAI Verification code","messagePreview":"Your Verification code: 654321","toDomain":"spamok.com","toLocal":"aaaaa0a","dateSystem":"2026-05-07T09:44:06.501656Z"}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		}),
	}

	provider, err := NewSpamOKMailProvider(SpamOKConfig{
		BaseURL:        "https://spamok.test",
		APIBaseURL:     "https://api.spamok.test/v2",
		Domain:         "spamok.com",
		RequestTimeout: time.Second,
		WaitTimeout:    time.Second,
		WaitInterval:   10 * time.Millisecond,
		HTTPClient:     client,
	}, &deterministicRandom{})
	if err != nil {
		t.Fatalf("NewSpamOKMailProvider() error = %v", err)
	}

	mailbox, err := provider.CreateMailbox(context.Background())
	if err != nil {
		t.Fatalf("CreateMailbox() error = %v", err)
	}
	if mailbox.Address != "aaaaa0a@spamok.com" {
		t.Fatalf("unexpected mailbox address: %s", mailbox.Address)
	}
	if got := strings.TrimSpace(stringValue(mailbox.Meta["mailbox_url"])); got != "https://spamok.test/aaaaa0a" {
		t.Fatalf("unexpected mailbox url: %s", got)
	}

	code, err := provider.WaitForCode(context.Background(), mailbox)
	if err != nil {
		t.Fatalf("WaitForCode() error = %v", err)
	}
	if code != "654321" {
		t.Fatalf("unexpected code: %s", code)
	}
	if strings.Join(requestedPaths, ",") != "/v2/EmailBox/aaaaa0a" {
		t.Fatalf("unexpected requested paths: %v", requestedPaths)
	}
}

func TestInbucketMailProviderWaitForCodeFallsBackToAddressWhenMetaMissing(t *testing.T) {
	var requestedPaths []string
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requestedPaths = append(requestedPaths, r.URL.Path)
			var body string
			status := http.StatusOK
			switch r.URL.Path {
			case "/api/v1/mailbox/user123":
				body = `[{"id":"msg-1","date":"2026-05-05T10:00:00Z","subject":"Verify","from":"no-reply@openai.com"}]`
			case "/api/v1/mailbox/user123/msg-1":
				if r.Method == http.MethodDelete {
					status = http.StatusNoContent
					body = ``
				} else {
					body = `{
						"id":"msg-1",
						"date":"2026-05-05T10:00:00Z",
						"subject":"OpenAI Verification code",
						"from":"no-reply@openai.com",
						"header":{"To":"user123@example.com"},
						"body":{"text":"Your Verification code: 123456","html":"<p>123456</p>"}
					}`
				}
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
		APIBase:        "http://inbucket.test",
		Domains:        []string{"example.com"},
		RequestTimeout: time.Second,
		WaitTimeout:    time.Second,
		WaitInterval:   10 * time.Millisecond,
		HTTPClient:     client,
	}, &deterministicRandom{})
	if err != nil {
		t.Fatalf("NewInbucketMailProvider() error = %v", err)
	}

	code, err := provider.WaitForCode(context.Background(), Mailbox{Address: "user123@example.com"})
	if err != nil {
		t.Fatalf("WaitForCode() error = %v", err)
	}
	if code != "123456" {
		t.Fatalf("unexpected code: %s", code)
	}
	if !strings.Contains(strings.Join(requestedPaths, ","), "/api/v1/mailbox/user123") {
		t.Fatalf("expected fallback mailbox path, got %v", requestedPaths)
	}
}

func TestSpamOKMailProviderWaitForCodeFallsBackToAddressWhenMetaMissing(t *testing.T) {
	var requestedPaths []string
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requestedPaths = append(requestedPaths, r.URL.Path)
			body := `{"address":"user123","subscribed":false,"mails":[{"id":123,"subject":"OpenAI Verification code","messagePreview":"Your Verification code: 654321","toDomain":"spamok.com","toLocal":"user123","dateSystem":"2026-05-07T09:44:06.501656Z"}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		}),
	}

	provider, err := NewSpamOKMailProvider(SpamOKConfig{
		BaseURL:        "https://spamok.test",
		APIBaseURL:     "https://api.spamok.test/v2",
		Domain:         "spamok.com",
		RequestTimeout: time.Second,
		WaitTimeout:    time.Second,
		WaitInterval:   10 * time.Millisecond,
		HTTPClient:     client,
	}, &deterministicRandom{})
	if err != nil {
		t.Fatalf("NewSpamOKMailProvider() error = %v", err)
	}

	code, err := provider.WaitForCode(context.Background(), Mailbox{Address: "user123@spamok.com"})
	if err != nil {
		t.Fatalf("WaitForCode() error = %v", err)
	}
	if code != "654321" {
		t.Fatalf("unexpected code: %s", code)
	}
	if !strings.Contains(strings.Join(requestedPaths, ","), "/v2/EmailBox/user123") {
		t.Fatalf("expected fallback spamok path, got %v", requestedPaths)
	}
}

func TestSpamOKMailProviderWaitForCodePrefersLatestMail(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := `{"address":"vqphv9ioc","subscribed":false,"mails":[{"id":65220066,"subject":"Your temporary OpenAI login code","fromDisplay":"noreply@tm.openai.com","fromDomain":"tm.openai.com","fromLocal":"noreply","toDomain":"spamok.com","toLocal":"vqphv9ioc","date":"2026-05-07T16:03:51Z","dateSystem":"2026-05-07T16:03:53.284283Z","messagePreview":"Enter this temporary verification code to continue: 682766.","secondsAgo":145},{"id":65212172,"subject":"Your temporary OpenAI login code","fromDisplay":"noreply@tm.openai.com","fromDomain":"tm.openai.com","fromLocal":"noreply","toDomain":"spamok.com","toLocal":"vqphv9ioc","date":"2026-05-07T12:27:38Z","dateSystem":"2026-05-07T12:27:40.665458Z","messagePreview":"Enter this temporary verification code to continue: 185146.","secondsAgo":13118},{"id":65212163,"subject":"Your temporary OpenAI verification code","fromDisplay":"noreply@tm.openai.com","fromDomain":"tm.openai.com","fromLocal":"noreply","toDomain":"spamok.com","toLocal":"vqphv9ioc","date":"2026-05-07T12:27:29Z","dateSystem":"2026-05-07T12:27:31.629958Z","messagePreview":"Enter this temporary verification code to continue: 030836.","secondsAgo":13127}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		}),
	}

	provider, err := NewSpamOKMailProvider(SpamOKConfig{
		BaseURL:        "https://spamok.test",
		APIBaseURL:     "https://api.spamok.test/v2",
		Domain:         "spamok.com",
		RequestTimeout: time.Second,
		WaitTimeout:    time.Second,
		WaitInterval:   10 * time.Millisecond,
		HTTPClient:     client,
	}, &deterministicRandom{})
	if err != nil {
		t.Fatalf("NewSpamOKMailProvider() error = %v", err)
	}

	code, err := provider.WaitForCode(context.Background(), Mailbox{Address: "vqphv9ioc@spamok.com"})
	if err != nil {
		t.Fatalf("WaitForCode() error = %v", err)
	}
	if code != "682766" {
		t.Fatalf("expected latest spamok code, got %s", code)
	}
}

func TestSpamOKMailProviderWaitForCodeSkipsOlderMailAndWaitsForNewerCode(t *testing.T) {
	var calls int32
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			current := atomic.AddInt32(&calls, 1)
			body := `{"address":"vqphv9ioc","subscribed":false,"mails":[{"id":65212172,"subject":"Your temporary OpenAI login code","toDomain":"spamok.com","toLocal":"vqphv9ioc","date":"2026-05-07T12:27:38Z","dateSystem":"2026-05-07T12:27:40.665458Z","messagePreview":"Enter this temporary verification code to continue: 185146."}]}`
			if current > 1 {
				body = `{"address":"vqphv9ioc","subscribed":false,"mails":[{"id":65220066,"subject":"Your temporary OpenAI login code","toDomain":"spamok.com","toLocal":"vqphv9ioc","date":"2026-05-08T12:27:45Z","dateSystem":"2026-05-08T12:27:45Z","messagePreview":"Enter this temporary verification code to continue: 682766."},{"id":65212172,"subject":"Your temporary OpenAI login code","toDomain":"spamok.com","toLocal":"vqphv9ioc","date":"2026-05-07T12:27:38Z","dateSystem":"2026-05-07T12:27:40.665458Z","messagePreview":"Enter this temporary verification code to continue: 185146."}]}`
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		}),
	}

	provider, err := NewSpamOKMailProvider(SpamOKConfig{
		BaseURL:        "https://spamok.test",
		APIBaseURL:     "https://api.spamok.test/v2",
		Domain:         "spamok.com",
		RequestTimeout: time.Second,
		WaitTimeout:    2 * time.Second,
		WaitInterval:   10 * time.Millisecond,
		HTTPClient:     client,
	}, &deterministicRandom{})
	if err != nil {
		t.Fatalf("NewSpamOKMailProvider() error = %v", err)
	}

	code, err := provider.WaitForCode(context.Background(), Mailbox{
		Address: "vqphv9ioc@spamok.com",
		Meta: map[string]any{
			"_wait_for_new_mail_after": "2026-05-08T12:27:42Z",
		},
	})
	if err != nil {
		t.Fatalf("WaitForCode() error = %v", err)
	}
	if code != "682766" {
		t.Fatalf("expected newer spamok code, got %s", code)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Fatalf("expected provider to poll for a newer mail, calls=%d", calls)
	}
}

func TestSpamOKMailProviderWaitForCodeFallsBackToOlderLatestAfterTenSeconds(t *testing.T) {
	var calls int32
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			atomic.AddInt32(&calls, 1)
			body := `{"address":"vqphv9ioc","subscribed":false,"mails":[{"id":65212172,"subject":"Your temporary OpenAI login code","toDomain":"spamok.com","toLocal":"vqphv9ioc","date":"2026-05-07T12:27:38Z","dateSystem":"2026-05-07T12:27:40.665458Z","messagePreview":"Enter this temporary verification code to continue: 185146."}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		}),
	}

	provider, err := NewSpamOKMailProvider(SpamOKConfig{
		BaseURL:        "https://spamok.test",
		APIBaseURL:     "https://api.spamok.test/v2",
		Domain:         "spamok.com",
		RequestTimeout: time.Second,
		WaitTimeout:    2 * time.Second,
		WaitInterval:   10 * time.Millisecond,
		HTTPClient:     client,
	}, &deterministicRandom{})
	if err != nil {
		t.Fatalf("NewSpamOKMailProvider() error = %v", err)
	}

	code, err := provider.WaitForCode(context.Background(), Mailbox{
		Address: "vqphv9ioc@spamok.com",
		Meta: map[string]any{
			"_wait_for_new_mail_after": time.Now().UTC().Add(-11 * time.Second).Format(time.RFC3339Nano),
		},
	})
	if err != nil {
		t.Fatalf("WaitForCode() error = %v", err)
	}
	if code != "185146" {
		t.Fatalf("expected fallback older spamok code, got %s", code)
	}
	if atomic.LoadInt32(&calls) < 1 {
		t.Fatalf("expected at least one poll call")
	}
}

func TestExtractSpamOKCodeFromHTMLIgnoresScriptsAndFindsVisibleCode(t *testing.T) {
	source := `
	<html>
		<head>
			<script>var build = "123456";</script>
			<style>.x{content:"654321"}</style>
		</head>
		<body>
			<div>OpenAI Verification code: 112233</div>
		</body>
	</html>`
	if got := extractSpamOKCodeFromHTML(source); got != "112233" {
		t.Fatalf("expected visible code, got %q", got)
	}
}
