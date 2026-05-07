package register

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

type Registrar struct {
	cfg         Config
	mail        MailProvider
	accountRepo AccountRepository
	logSink     LogSink
	logger      Logger
	identity    IdentityGenerator
	httpFactory HTTPClientFactory
	random      RandomSource
	now         func() time.Time
}

func NewLoginOnly(cfg Config) (*LoginOnly, error) {
	return NewLoginOnlyWithMail(cfg, nil)
}

func NewLoginOnlyWithMail(cfg Config, mail MailProvider) (*LoginOnly, error) {
	cfg = cfg.withDefaults()
	return &LoginOnly{
		cfg:         cfg,
		mail:        mail,
		httpFactory: defaultHTTPClientFactory{},
		random:      newDefaultRandomSource(),
		now:         func() time.Time { return time.Now().UTC() },
		logger:      LoggerFunc(func(context.Context, string, string, ...any) {}),
	}, nil
}

func New(options Options) (*Registrar, error) {
	if options.MailProvider == nil {
		return nil, ErrMailProviderRequired
	}
	if options.AccountRepo == nil {
		return nil, ErrAccountRepoRequired
	}
	cfg := options.Config.withDefaults()
	random := options.RandomSource
	if random == nil {
		random = newDefaultRandomSource()
	}
	identity := options.NameGenerator
	if identity == nil {
		identity = newDefaultIdentityGenerator(random)
	}
	httpFactory := options.HTTPFactory
	if httpFactory == nil {
		httpFactory = defaultHTTPClientFactory{}
	}
	logger := options.Logger
	if logger == nil {
		logger = LoggerFunc(func(context.Context, string, string, ...any) {})
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Registrar{
		cfg:         cfg,
		mail:        options.MailProvider,
		accountRepo: options.AccountRepo,
		logSink:     options.LogSink,
		logger:      logger,
		identity:    identity,
		httpFactory: httpFactory,
		random:      random,
		now:         now,
	}, nil
}

func (r *Registrar) Register(ctx context.Context) (RegisterResult, error) {
	client, err := r.httpFactory.New(r.cfg)
	if err != nil {
		return RegisterResult{}, err
	}
	defer client.CloseIdleConnections()

	state := flowState{
		client:      client,
		deviceID:    randomUUID(r.random),
		cfg:         r.cfg,
		httpFactory: r.httpFactory,
		random:      r.random,
		now:         r.now,
		logger:      r.logger,
	}

	r.logRegistration(ctx, "info", "creating mailbox", nil)
	mailbox, err := r.mail.CreateMailbox(ctx)
	if err != nil {
		r.logRegistration(ctx, "error", "create mailbox failed", map[string]any{"error": err.Error()})
		return RegisterResult{}, err
	}
	email := strings.TrimSpace(mailbox.Address)
	if email == "" {
		r.logRegistration(ctx, "error", "mail provider returned empty address", nil)
		return RegisterResult{}, errors.New("mail provider returned empty address")
	}
	r.logRegistration(ctx, "info", "mailbox ready", map[string]any{"email": email})
	password := r.identity.Password()
	firstName, lastName := r.identity.Name()
	fullName := joinFullName(firstName, lastName)
	birthdate := r.identity.Birthdate()

	r.logRegistration(ctx, "info", "platform authorize", map[string]any{"email": email})
	if err := state.platformAuthorize(ctx, email); err != nil {
		r.logRegistration(ctx, "error", "platform authorize failed", map[string]any{"email": email, "error": err.Error()})
		return RegisterResult{}, err
	}
	r.logRegistration(ctx, "info", "register user", map[string]any{"email": email})
	if err := state.registerUser(ctx, email, password); err != nil {
		r.logRegistration(ctx, "error", "register user failed", map[string]any{"email": email, "error": err.Error()})
		return RegisterResult{}, err
	}
	r.logRegistration(ctx, "info", "send email otp", map[string]any{"email": email})
	if err := state.sendOTP(ctx); err != nil {
		r.logRegistration(ctx, "error", "send email otp failed", map[string]any{"email": email, "error": err.Error()})
		return RegisterResult{}, err
	}

	r.logRegistration(ctx, "info", "waiting for verification code", map[string]any{"email": email})
	code, err := r.waitForCode(ctx, mailbox)
	if err != nil {
		r.logRegistration(ctx, "error", "wait for verification code failed", map[string]any{"email": email, "error": err.Error()})
		return RegisterResult{}, err
	}
	r.logRegistration(ctx, "info", "verification code received", map[string]any{"email": email, "code": code})
	r.logRegistration(ctx, "info", "validate email otp", map[string]any{"email": email, "code": code})
	if _, err := state.validateOTP(ctx, code); err != nil {
		r.logRegistration(ctx, "error", "validate email otp failed", map[string]any{"email": email, "code": code, "error": err.Error()})
		return RegisterResult{}, err
	}
	r.logRegistration(ctx, "info", "create account profile", map[string]any{"email": email})
	if err := state.createAccount(ctx, fullName, birthdate); err != nil {
		r.logRegistration(ctx, "error", "create account profile failed", map[string]any{"email": email, "error": err.Error()})
		return RegisterResult{}, err
	}

	r.logRegistration(ctx, "info", "exchange platform tokens", map[string]any{"email": email})
	tokens, err := r.loginAndExchangeTokens(ctx, state, email, password, mailbox)
	if err != nil {
		r.logRegistration(ctx, "error", "exchange platform tokens failed", map[string]any{"email": email, "error": err.Error()})
		return RegisterResult{}, err
	}
	result := RegisterResult{
		Email:        email,
		Password:     password,
		AccessToken:  strings.TrimSpace(tokens.AccessToken),
		RefreshToken: strings.TrimSpace(tokens.RefreshToken),
		IDToken:      strings.TrimSpace(tokens.IDToken),
		CreatedAt:    r.now(),
	}
	if result.AccessToken == "" {
		r.logRegistration(ctx, "error", "empty access token", map[string]any{"email": email})
		return RegisterResult{}, errors.New("empty access token")
	}
	r.logRegistration(ctx, "info", "store account token", map[string]any{"email": email})
	if _, err := r.accountRepo.AddAccessToken(ctx, result.AccessToken, result.Password); err != nil {
		r.logRegistration(ctx, "error", "store account token failed", map[string]any{"email": email, "error": err.Error()})
		return RegisterResult{}, err
	}
	r.logRegistration(ctx, "info", "refresh account remote info", map[string]any{"email": email})
	if err := r.refreshAccountRemoteInfo(ctx, result.AccessToken); err != nil {
		r.logRegistration(ctx, "warn", "refresh account remote info failed", map[string]any{"email": email, "error": err.Error()})
	}
	r.logRegistration(ctx, "info", "register success", map[string]any{
		"email": result.Email,
	})
	return result, nil
}

func (r *Registrar) waitForCode(ctx context.Context, mailbox Mailbox) (string, error) {
	waitCtx := ctx
	if _, ok := ctx.Deadline(); !ok && r.cfg.WaitTimeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, r.cfg.WaitTimeout)
		defer cancel()
	}
	code, err := r.mail.WaitForCode(waitCtx, mailbox)
	if err != nil {
		return "", err
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", ErrCodeTimeout
	}
	return code, nil
}

func (r *Registrar) loginAndExchangeTokens(ctx context.Context, state flowState, email string, password string, mailbox Mailbox) (tokenBundle, error) {
	tokens, err := state.loginAndExchangeTokens(ctx, email, password, mailbox, r.mail)
	if err == nil {
		return tokens, nil
	}
	if !shouldRetryFreshLogin(err) {
		return tokenBundle{}, err
	}
	reason := classifyFreshLoginRetryReason(err)
	lastErr := err
	for retry := 1; retry <= 3; retry++ {
		reason = classifyFreshLoginRetryReason(lastErr)
		r.logRegistration(ctx, "warn", "reset login session and retry exchange", map[string]any{
			"email":  email,
			"reason": reason,
			"retry":  retry,
			"error":  lastErr.Error(),
		})
		client, newErr := r.httpFactory.New(r.cfg)
		if newErr != nil {
			return tokenBundle{}, newErr
		}
		freshState := flowState{
			client:      client,
			deviceID:    randomUUID(r.random),
			cfg:         r.cfg,
			httpFactory: r.httpFactory,
			random:      r.random,
			now:         r.now,
			logger:      r.logger,
		}
		r.logRegistration(ctx, "info", "retry exchange platform tokens", map[string]any{
			"email":  email,
			"reason": reason,
			"retry":  retry,
		})
		tokens, err = freshState.loginAndExchangeTokens(ctx, email, password, mailbox, r.mail)
		client.CloseIdleConnections()
		if err == nil {
			r.logRegistration(ctx, "info", "retry exchange platform tokens succeeded", map[string]any{
				"email":  email,
				"reason": reason,
				"retry":  retry,
			})
			return tokens, nil
		}
		lastErr = err
		r.logRegistration(ctx, "warn", "retry exchange platform tokens failed", map[string]any{
			"email":  email,
			"reason": classifyFreshLoginRetryReason(err),
			"retry":  retry,
			"error":  err.Error(),
		})
		if !shouldRetryFreshLogin(err) {
			return tokenBundle{}, err
		}
	}
	return tokenBundle{}, lastErr
}

func (r *Registrar) refreshAccountRemoteInfo(ctx context.Context, accessToken string) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := r.accountRepo.RefreshAccount(ctx, accessToken); err != nil {
			lastErr = err
			if attempt < 3 {
				r.logRegistration(ctx, "warn", fmt.Sprintf("refresh account remote info retry %d/3", attempt), map[string]any{"error": err.Error()})
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(attempt) * time.Second):
				}
				continue
			}
			return err
		}
		return nil
	}
	return lastErr
}

func (l *LoginOnly) LoginAndExchangeTokens(ctx context.Context, email string, password string) (RegisterResult, error) {
	client, err := l.httpFactory.New(l.cfg)
	if err != nil {
		return RegisterResult{}, err
	}
	defer client.CloseIdleConnections()
	state := flowState{
		client:      client,
		deviceID:    randomUUID(l.random),
		cfg:         l.cfg,
		httpFactory: l.httpFactory,
		random:      l.random,
		now:         l.now,
		logger:      l.logger,
	}
	tokens, err := l.loginAndExchange(ctx, state, strings.TrimSpace(email), strings.TrimSpace(password))
	if err != nil {
		return RegisterResult{}, err
	}
	return RegisterResult{
		Email:        strings.TrimSpace(email),
		Password:     strings.TrimSpace(password),
		AccessToken:  strings.TrimSpace(tokens.AccessToken),
		RefreshToken: strings.TrimSpace(tokens.RefreshToken),
		IDToken:      strings.TrimSpace(tokens.IDToken),
		CreatedAt:    l.now(),
	}, nil
}

func (l *LoginOnly) loginAndExchange(ctx context.Context, state flowState, email string, password string) (tokenBundle, error) {
	emptyMailbox := Mailbox{Address: email}
	mail := l.mail
	if mail == nil {
		mail = loginOnlyMailProvider{}
	}
	tokens, err := state.loginAndExchangeTokens(ctx, email, password, emptyMailbox, mail)
	if err == nil {
		return tokens, nil
	}
	if !shouldRetryFreshLogin(err) {
		return tokenBundle{}, err
	}
	lastErr := err
	for retry := 1; retry <= 3; retry++ {
		reason := classifyFreshLoginRetryReason(lastErr)
		l.logger.Printf(ctx, "warn", "login-only fresh session retry reason=%s retry=%d error=%v", reason, retry, lastErr)
		client, newErr := l.httpFactory.New(l.cfg)
		if newErr != nil {
			return tokenBundle{}, newErr
		}
		freshState := flowState{
			client:      client,
			deviceID:    randomUUID(l.random),
			cfg:         l.cfg,
			httpFactory: l.httpFactory,
			random:      l.random,
			now:         l.now,
			logger:      l.logger,
		}
		tokens, err = freshState.loginAndExchangeTokens(ctx, email, password, emptyMailbox, mail)
		client.CloseIdleConnections()
		if err == nil {
			return tokens, nil
		}
		lastErr = err
		if !shouldRetryFreshLogin(err) {
			return tokenBundle{}, err
		}
	}
	return tokenBundle{}, lastErr
}

type loginOnlyMailProvider struct{}

func (loginOnlyMailProvider) CreateMailbox(context.Context) (Mailbox, error) {
	return Mailbox{}, errors.New("login-only mail provider cannot create mailbox")
}

func (loginOnlyMailProvider) WaitForCode(context.Context, Mailbox) (string, error) {
	return "", errors.New("login-only flow requires a mailbox provider for otp")
}

func (r *Registrar) logRegistration(ctx context.Context, level string, summary string, detail map[string]any) {
	if r.logSink != nil {
		_ = r.logSink.Log(ctx, level, summary, detail)
	}
}

func (r *Registrar) logf(ctx context.Context, level string, format string, args ...any) {
	r.logger.Printf(ctx, level, format, args...)
}

type flowState struct {
	client   HTTPClient
	deviceID string
	cfg      Config
	httpFactory HTTPClientFactory
	random   RandomSource
	now      func() time.Time
	logger   Logger
}

type tokenBundle struct {
	Email        string
	AccessToken  string
	RefreshToken string
	IDToken      string
}

func (f flowState) platformAuthorize(ctx context.Context, email string) error {
	authURL, err := url.Parse(f.cfg.AuthBaseURL)
	if err != nil {
		return err
	}
	f.client.SetFollowRedirect(true)
	f.client.SetCookies(authURL, []*fhttp.Cookie{
		{Name: "oai-did", Value: f.deviceID, Domain: ".auth.openai.com", Path: "/"},
		{Name: "oai-did", Value: f.deviceID, Domain: "auth.openai.com", Path: "/"},
	})
	_, codeChallenge := generatePKCE(f.random)
	params := f.oauthParams(email, codeChallenge)
	headers := f.navigateHeaders(f.cfg.PlatformBaseURL + "/")
	reqCtx, cancel := withTimeout(ctx, f.cfg.RequestTimeout)
	defer cancel()
	resp, err := doWithRetry(reqCtx, f.client, f.cfg.LocalRetryAttempts, func() (*fhttp.Request, error) {
		return newRequest("GET", f.cfg.AuthBaseURL+"/api/accounts/authorize?"+params.Encode(), headers, nil)
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		data := resp.JSON()
		errData := mapValue(data["error"])
		detail := ""
		if len(errData) > 0 {
			detail = fmt.Sprintf(": %s - %s", stringValue(errData["code"]), stringValue(errData["message"]))
		}
		return fmt.Errorf("platform_authorize_http_%d%s", resp.StatusCode, strings.TrimSpace(detail))
	}
	return nil
}

func (f flowState) registerUser(ctx context.Context, email string, password string) error {
	f.client.SetFollowRedirect(true)
	headers := f.jsonHeaders(f.cfg.AuthBaseURL + "/create-account/password")
	token, err := buildSentinelToken(ctx, f.client, f.cfg, f.deviceID, "username_password_create", f.random, f.now)
	if err != nil {
		return err
	}
	headers["openai-sentinel-token"] = token
	payload, _ := json.Marshal(map[string]string{
		"username": email,
		"password": password,
	})
	reqCtx, cancel := withTimeout(ctx, f.cfg.RequestTimeout)
	defer cancel()
	resp, err := doWithRetry(reqCtx, f.client, f.cfg.LocalRetryAttempts, func() (*fhttp.Request, error) {
		return newRequest("POST", f.cfg.AuthBaseURL+"/api/accounts/user/register", headers, payload)
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		detail := ""
		if data := resp.JSON(); len(data) > 0 {
			value, _ := json.Marshal(data)
			detail = ", detail=" + string(value)
		}
		return fmt.Errorf("user_register_http_%d%s", resp.StatusCode, detail)
	}
	return nil
}

func (f flowState) sendOTP(ctx context.Context) error {
	f.client.SetFollowRedirect(true)
	headers := f.navigateHeaders(f.cfg.AuthBaseURL + "/create-account/password")
	reqCtx, cancel := withTimeout(ctx, f.cfg.RequestTimeout)
	defer cancel()
	resp, err := doWithRetry(reqCtx, f.client, f.cfg.LocalRetryAttempts, func() (*fhttp.Request, error) {
		return newRequest("GET", f.cfg.AuthBaseURL+"/api/accounts/email-otp/send", headers, nil)
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 && resp.StatusCode != 302 {
		return fmt.Errorf("send_otp_http_%d", resp.StatusCode)
	}
	return nil
}

func (f flowState) validateOTP(ctx context.Context, code string) (string, error) {
	f.client.SetFollowRedirect(true)
	payload, _ := json.Marshal(map[string]string{"code": code})
	headers := f.jsonHeaders(f.cfg.AuthBaseURL + "/email-verification")
	reqCtx, cancel := withTimeout(ctx, f.cfg.RequestTimeout)
	defer cancel()
	resp, err := doWithRetry(reqCtx, f.client, f.cfg.LocalRetryAttempts, func() (*fhttp.Request, error) {
		return newRequest("POST", f.cfg.AuthBaseURL+"/api/accounts/email-otp/validate", headers, payload)
	})
	if err != nil {
		return "", err
	}
	if resp.StatusCode == 200 {
		return strings.TrimSpace(stringValue(resp.JSON()["continue_url"])), nil
	}
	token, err := buildSentinelToken(ctx, f.client, f.cfg, f.deviceID, "authorize_continue", f.random, f.now)
	if err != nil {
		return "", err
	}
	headers["openai-sentinel-token"] = token
	resp, err = doWithRetry(reqCtx, f.client, f.cfg.LocalRetryAttempts, func() (*fhttp.Request, error) {
		return newRequest("POST", f.cfg.AuthBaseURL+"/api/accounts/email-otp/validate", headers, payload)
	})
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validate_otp_http_%d%s", resp.StatusCode, responseErrorDetail(resp))
	}
	return strings.TrimSpace(stringValue(resp.JSON()["continue_url"])), nil
}

func (f flowState) createAccount(ctx context.Context, name string, birthdate string) error {
	f.client.SetFollowRedirect(true)
	headers := f.jsonHeaders(f.cfg.AuthBaseURL + "/about-you")
	token, err := buildSentinelToken(ctx, f.client, f.cfg, f.deviceID, "oauth_create_account", f.random, f.now)
	if err != nil {
		return err
	}
	headers["openai-sentinel-token"] = token
	payload, _ := json.Marshal(map[string]string{
		"name":      name,
		"birthdate": birthdate,
	})
	reqCtx, cancel := withTimeout(ctx, f.cfg.RequestTimeout)
	defer cancel()
	resp, err := doWithRetry(reqCtx, f.client, f.cfg.LocalRetryAttempts, func() (*fhttp.Request, error) {
		return newRequest("POST", f.cfg.AuthBaseURL+"/api/accounts/create_account", headers, payload)
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 && resp.StatusCode != 302 {
		detail := ""
		if data := resp.JSON(); len(data) > 0 {
			value, _ := json.Marshal(data)
			detail = ", detail=" + string(value)
		}
		return fmt.Errorf("create_account_http_%d%s", resp.StatusCode, detail)
	}
	return nil
}

func (f flowState) loginAndExchangeTokens(ctx context.Context, email string, password string, mailbox Mailbox, mail MailProvider) (tokenBundle, error) {
	f.client.SetFollowRedirect(true)
	codeVerifier, codeChallenge := generatePKCE(f.random)
	params := f.oauthParams(email, codeChallenge)
	headers := f.navigateHeaders(f.cfg.PlatformBaseURL + "/")
	reqCtx, cancel := withTimeout(ctx, f.cfg.RequestTimeout)
	defer cancel()
	resp, err := doWithRetry(reqCtx, f.client, f.cfg.LocalRetryAttempts, func() (*fhttp.Request, error) {
		return newRequest("GET", f.cfg.AuthBaseURL+"/api/accounts/authorize?"+params.Encode(), headers, nil)
	})
	if err != nil {
		return tokenBundle{}, err
	}
	if resp.StatusCode == 0 {
		return tokenBundle{}, errors.New("platform_login_authorize_failed")
	}

	passwordHeaders := f.jsonHeaders(f.cfg.AuthBaseURL + "/log-in/password")
	token, err := buildSentinelToken(ctx, f.client, f.cfg, f.deviceID, "password_verify", f.random, f.now)
	if err != nil {
		return tokenBundle{}, err
	}
	passwordHeaders["openai-sentinel-token"] = token
	payload, _ := json.Marshal(map[string]string{"password": password})
	f.client.SetFollowRedirect(false)
	resp, err = doWithRetry(reqCtx, f.client, f.cfg.LocalRetryAttempts, func() (*fhttp.Request, error) {
		return newRequest("POST", f.cfg.AuthBaseURL+"/api/accounts/password/verify", passwordHeaders, payload)
	})
	if err != nil {
		return tokenBundle{}, err
	}
	if resp.StatusCode != 200 {
		detail := responseErrorDetail(resp)
		return tokenBundle{}, fmt.Errorf("password_verify_http_%d%s", resp.StatusCode, detail)
	}
	data := resp.JSON()
	continueURL := stringValue(data["continue_url"])
	pageType := stringValue(mapValue(data["page"])["type"])
	if pageType == "email_otp_verification" || strings.Contains(continueURL, "email-verification") || strings.Contains(continueURL, "email-otp") {
		code, err := mail.WaitForCode(ctx, mailbox)
		if err != nil {
			return tokenBundle{}, err
		}
		if strings.TrimSpace(code) == "" {
			return tokenBundle{}, ErrCodeTimeout
		}
		nextURL, err := f.validateOTP(ctx, code)
		if err != nil {
			return tokenBundle{}, err
		}
		if strings.TrimSpace(nextURL) != "" {
			continueURL = strings.TrimSpace(nextURL)
		}
	}
	if continueURL == "" {
		continueURL = f.cfg.AuthBaseURL + "/sign-in-with-chatgpt/codex/consent"
	}
	return f.exchangePlatformTokens(ctx, codeVerifier, continueURL)
}

func (f flowState) exchangePlatformTokens(ctx context.Context, codeVerifier string, consentURL string) (tokenBundle, error) {
	callback, err := f.extractOAuthCallback(ctx, consentURL)
	if err != nil {
		return tokenBundle{}, err
	}
	code := strings.TrimSpace(callback["code"])
	if code == "" {
		return tokenBundle{}, errors.New("missing oauth callback code")
	}
	values := url.Values{
		"grant_type":    []string{"authorization_code"},
		"code":          []string{code},
		"redirect_uri":  []string{f.cfg.PlatformOAuthRedirect},
		"client_id":     []string{f.cfg.PlatformOAuthClientID},
		"code_verifier": []string{codeVerifier},
	}
	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	}
	reqCtx, cancel := withTimeout(ctx, f.cfg.TokenExchangeTimeout)
	defer cancel()
	exchangeClient := f.client
	closeExchangeClient := func() {}
	if f.httpFactory != nil {
		freshClient, newErr := f.httpFactory.New(f.cfg)
		if newErr != nil {
			return tokenBundle{}, newErr
		}
		exchangeClient = freshClient
		closeExchangeClient = freshClient.CloseIdleConnections
	}
	defer closeExchangeClient()
	resp, err := doWithRetry(reqCtx, exchangeClient, f.cfg.LocalRetryAttempts, func() (*fhttp.Request, error) {
		return newRequest("POST", f.cfg.AuthBaseURL+"/oauth/token", headers, []byte(values.Encode()))
	})
	if err != nil {
		return tokenBundle{}, err
	}
	data := resp.JSON()
	if resp.StatusCode != 200 {
		return tokenBundle{}, fmt.Errorf("oauth_token_http_%d", resp.StatusCode)
	}
	accessToken := stringValue(data["access_token"])
	refreshToken := stringValue(data["refresh_token"])
	idToken := stringValue(data["id_token"])
	if accessToken == "" || refreshToken == "" || idToken == "" {
		return tokenBundle{}, errors.New("token exchange failed")
	}
	payload := decodeJWTPayload(idToken)
	if len(payload) == 0 {
		payload = decodeJWTPayload(accessToken)
	}
	return tokenBundle{
		Email:        stringValue(payload["email"]),
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		IDToken:      idToken,
	}, nil
}

func (f flowState) extractOAuthCallback(ctx context.Context, consentURL string) (map[string]string, error) {
	if strings.HasPrefix(consentURL, "/") {
		consentURL = f.cfg.AuthBaseURL + consentURL
	}
	currentURL := consentURL
	for range 10 {
		f.client.SetFollowRedirect(false)
		resp, err := doWithRetry(ctx, f.client, 1, func() (*fhttp.Request, error) {
			return newRequest("GET", currentURL, f.navigateHeaders(""), nil)
		})
		if err != nil {
			return nil, err
		}
		if parsed := parseOAuthCallback(resp.URL); parsed != nil {
			return parsed, nil
		}
		if parsed := parseOAuthCallback(headerValue(resp.Header, "Location")); parsed != nil {
			return parsed, nil
		}
		location := strings.TrimSpace(headerValue(resp.Header, "Location"))
		if !isRedirect(resp.StatusCode) || location == "" {
			break
		}
		if strings.HasPrefix(location, "/") {
			currentURL = f.cfg.AuthBaseURL + location
		} else {
			currentURL = location
		}
	}
	rawSession := f.cookieValue(f.cfg.AuthBaseURL, "oai-client-auth-session")
	if rawSession == "" {
		return nil, fmt.Errorf("missing oai-client-auth-session cookie consent_url=%s current_url=%s device_id=%s", consentURL, currentURL, f.deviceID)
	}
	workspaceID := extractWorkspaceID(rawSession)
	if workspaceID == "" {
		return nil, fmt.Errorf("missing workspace id consent_url=%s current_url=%s auth_session_len=%d device_id=%s", consentURL, currentURL, len(rawSession), f.deviceID)
	}
	headers := f.jsonHeaders(consentURL)
	payload, _ := json.Marshal(map[string]string{"workspace_id": workspaceID})
	f.client.SetFollowRedirect(false)
	resp, err := doWithRetry(ctx, f.client, f.cfg.LocalRetryAttempts, func() (*fhttp.Request, error) {
		return newRequest("POST", f.cfg.AuthBaseURL+"/api/accounts/workspace/select", headers, payload)
	})
	if err != nil {
		return nil, err
	}
	if parsed := parseOAuthCallback(headerValue(resp.Header, "Location")); parsed != nil {
		return parsed, nil
	}
	data := resp.JSON()
	orgs := sliceValue(mapValue(data["data"])["orgs"])
	if len(orgs) == 0 {
		return nil, fmt.Errorf("missing orgs in workspace selection consent_url=%s workspace_id=%s continue_url=%s", consentURL, workspaceID, strings.TrimSpace(stringValue(data["continue_url"])))
	}
	firstOrg := mapValue(orgs[0])
	orgID := stringValue(firstOrg["id"])
	projects := sliceValue(firstOrg["projects"])
	projectID := ""
	if len(projects) > 0 {
		projectID = stringValue(mapValue(projects[0])["id"])
	}
	if orgID == "" {
		return nil, fmt.Errorf("missing org id consent_url=%s workspace_id=%s continue_url=%s", consentURL, workspaceID, strings.TrimSpace(stringValue(data["continue_url"])))
	}
	orgPayload := map[string]string{"org_id": orgID}
	if projectID != "" {
		orgPayload["project_id"] = projectID
	}
	body, _ := json.Marshal(orgPayload)
	orgReferer := strings.TrimSpace(stringValue(data["continue_url"]))
	if orgReferer == "" {
		orgReferer = consentURL
	}
	orgHeaders := f.jsonHeaders(orgReferer)
	f.client.SetFollowRedirect(false)
	resp, err = doWithRetry(ctx, f.client, f.cfg.LocalRetryAttempts, func() (*fhttp.Request, error) {
		return newRequest("POST", f.cfg.AuthBaseURL+"/api/accounts/organization/select", orgHeaders, body)
	})
	if err != nil {
		return nil, err
	}
	if parsed := parseOAuthCallback(headerValue(resp.Header, "Location")); parsed != nil {
		return parsed, nil
	}
	return nil, fmt.Errorf("missing oauth callback after consent consent_url=%s workspace_id=%s org_id=%s project_id=%s", consentURL, workspaceID, orgID, projectID)
}

func (f flowState) oauthParams(email string, codeChallenge string) url.Values {
	return url.Values{
		"issuer":                []string{f.cfg.AuthBaseURL},
		"client_id":             []string{f.cfg.PlatformOAuthClientID},
		"audience":              []string{f.cfg.PlatformOAuthAudience},
		"redirect_uri":          []string{f.cfg.PlatformOAuthRedirect},
		"device_id":             []string{f.deviceID},
		"screen_hint":           []string{"login_or_signup"},
		"max_age":               []string{"0"},
		"login_hint":            []string{email},
		"scope":                 []string{"openid profile email offline_access"},
		"response_type":         []string{"code"},
		"response_mode":         []string{"query"},
		"state":                 []string{randomID(f.random, 24)},
		"nonce":                 []string{randomID(f.random, 24)},
		"code_challenge":        []string{codeChallenge},
		"code_challenge_method": []string{"S256"},
		"auth0Client":           []string{f.cfg.PlatformAuth0Client},
	}
}

func (f flowState) jsonHeaders(referer string) map[string]string {
	headers := map[string]string{
		"accept":                      "application/json",
		"accept-language":             "en-US,en;q=0.9",
		"content-type":                "application/json",
		"origin":                      f.cfg.AuthBaseURL,
		"priority":                    "u=1, i",
		"user-agent":                  f.cfg.UserAgent,
		"sec-ch-ua":                   f.cfg.SecCHUA,
		"sec-ch-ua-arch":              `"x86_64"`,
		"sec-ch-ua-bitness":           `"64"`,
		"sec-ch-ua-full-version-list": f.cfg.SecCHUAFullVersion,
		"sec-ch-ua-mobile":            "?0",
		"sec-ch-ua-model":             `""`,
		"sec-ch-ua-platform":          `"Windows"`,
		"sec-ch-ua-platform-version":  `"10.0.0"`,
		"sec-fetch-dest":              "empty",
		"sec-fetch-mode":              "cors",
		"sec-fetch-site":              "same-origin",
		"referer":                     referer,
		"oai-device-id":               f.deviceID,
	}
	for key, value := range traceHeaders(f.random) {
		headers[key] = value
	}
	return headers
}

func (f flowState) navigateHeaders(referer string) map[string]string {
	headers := map[string]string{
		"accept":                      "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"accept-language":             "en-US,en;q=0.9",
		"user-agent":                  f.cfg.UserAgent,
		"sec-ch-ua":                   f.cfg.SecCHUA,
		"sec-ch-ua-arch":              `"x86_64"`,
		"sec-ch-ua-bitness":           `"64"`,
		"sec-ch-ua-full-version-list": f.cfg.SecCHUAFullVersion,
		"sec-ch-ua-mobile":            "?0",
		"sec-ch-ua-model":             `""`,
		"sec-ch-ua-platform":          `"Windows"`,
		"sec-ch-ua-platform-version":  `"10.0.0"`,
		"sec-fetch-dest":              "document",
		"sec-fetch-mode":              "navigate",
		"sec-fetch-site":              "same-origin",
		"sec-fetch-user":              "?1",
		"upgrade-insecure-requests":   "1",
	}
	if referer != "" {
		headers["referer"] = referer
	}
	return headers
}

func (f flowState) cookieValue(rawURL string, name string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, item := range f.client.GetCookies(u) {
		if item != nil && item.Name == name {
			return item.Value
		}
	}
	return ""
}

func headerValue(header map[string][]string, key string) string {
	for currentKey, values := range header {
		if strings.EqualFold(currentKey, key) && len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

func isRedirect(status int) bool {
	switch status {
	case 301, 302, 303, 307, 308:
		return true
	default:
		return false
	}
}

func extractWorkspaceID(raw string) string {
	firstPart := raw
	if dot := strings.Index(firstPart, "."); dot >= 0 {
		firstPart = firstPart[:dot]
	}
	data, err := decodeBase64URL(firstPart)
	if err != nil {
		return ""
	}
	payload := parseJSONMap(data)
	workspaces := sliceValue(payload["workspaces"])
	if len(workspaces) == 0 {
		return ""
	}
	return stringValue(mapValue(workspaces[0])["id"])
}

func decodeBase64URL(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
}

func shouldRetryFreshLogin(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(text, "account_deactivated") ||
		strings.Contains(text, "you do not have an account because it has been deleted or deactivated") {
		return false
	}
	return strings.Contains(text, "invalid session") ||
		strings.Contains(text, "invalid_state") ||
		strings.Contains(text, "missing workspace id") ||
		strings.Contains(text, "missing orgs in workspace selection") ||
		strings.Contains(text, "missing org id") ||
		strings.Contains(text, "missing oauth callback after consent") ||
		strings.Contains(text, "oauth_token_http_") ||
		strings.Contains(text, "token exchange failed") ||
		strings.Contains(text, "password_verify_http_409") ||
		strings.Contains(text, "password_verify_http_401")
}

func classifyFreshLoginRetryReason(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(text, "account_deactivated"),
		strings.Contains(text, "you do not have an account because it has been deleted or deactivated"):
		return "account_deactivated"
	case strings.Contains(text, "missing workspace id"):
		return "missing_workspace_id"
	case strings.Contains(text, "missing orgs in workspace selection"):
		return "missing_workspace_orgs"
	case strings.Contains(text, "missing org id"):
		return "missing_org_id"
	case strings.Contains(text, "missing oauth callback after consent"):
		return "missing_oauth_callback_after_consent"
	case strings.Contains(text, "oauth_token_http_"):
		return "oauth_token_http"
	case strings.Contains(text, "token exchange failed"):
		return "token_exchange_failed"
	case strings.Contains(text, "invalid_state"):
		return "invalid_state"
	case strings.Contains(text, "invalid session"):
		return "invalid_session"
	case strings.Contains(text, "password_verify_http_409"):
		return "password_verify_http_409"
	case strings.Contains(text, "password_verify_http_401"):
		return "password_verify_http_401"
	default:
		return "fresh_session_retry"
	}
}
