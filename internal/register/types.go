package register

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	defaultAuthBaseURL            = "https://auth.openai.com"
	defaultPlatformBaseURL        = "https://platform.openai.com"
	defaultPlatformOAuthClientID  = "app_2SKx67EdpoN0G6j64rFvigXD"
	defaultPlatformOAuthRedirect  = "https://platform.openai.com/auth/callback"
	defaultPlatformOAuthAudience  = "https://api.openai.com/v1"
	defaultPlatformAuth0Client    = "eyJuYW1lIjoiYXV0aDAtc3BhLWpzIiwidmVyc2lvbiI6IjEuMjEuMCJ9"
	defaultUserAgent              = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"
	defaultSecCHUA                = `"Google Chrome";v="145", "Not?A_Brand";v="8", "Chromium";v="145"`
	defaultSecCHUAFullVersionList = `"Chromium";v="145.0.0.0", "Not:A-Brand";v="99.0.0.0", "Google Chrome";v="145.0.0.0"`
	defaultRequestTimeout         = 30 * time.Second
	defaultSentinelTimeout        = 20 * time.Second
	defaultTokenExchangeTimeout   = 60 * time.Second
	defaultWaitTimeout            = 30 * time.Second
	defaultWaitInterval           = 2 * time.Second
	defaultCheckInterval          = 5 * time.Second
	defaultThreads                = 3
	defaultTotal                  = 10
	registerLogType               = "register"
)

type Mailbox struct {
	Address string         `json:"address"`
	Meta    map[string]any `json:"meta,omitempty"`
}

type RegisterResult struct {
	Email        string    `json:"email"`
	Password     string    `json:"password"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token"`
	CreatedAt    time.Time `json:"created_at"`
}

type AccountSnapshot struct {
	Status            string
	Quota             int
	ImageQuotaUnknown bool
}

type PoolMetrics struct {
	CurrentQuota     int `json:"current_quota"`
	CurrentAvailable int `json:"current_available"`
}

type RegisterMode string

const (
	RegisterModeTotal     RegisterMode = "total"
	RegisterModeQuota     RegisterMode = "quota"
	RegisterModeAvailable RegisterMode = "available"
)

type MailProvider interface {
	CreateMailbox(ctx context.Context) (Mailbox, error)
	WaitForCode(ctx context.Context, mailbox Mailbox) (string, error)
}

type AccountRepository interface {
	AddAccessToken(ctx context.Context, token string, password string) (bool, error)
	RefreshAccount(ctx context.Context, token string) error
	ListAccounts(ctx context.Context) ([]AccountSnapshot, error)
}

type LogSink interface {
	Log(ctx context.Context, level string, summary string, detail map[string]any) error
}

type LogSinkFunc func(ctx context.Context, level string, summary string, detail map[string]any) error

func (f LogSinkFunc) Log(ctx context.Context, level string, summary string, detail map[string]any) error {
	if f == nil {
		return nil
	}
	return f(ctx, level, summary, detail)
}

type LoggerFunc func(ctx context.Context, level string, format string, args ...any)

func (f LoggerFunc) Printf(ctx context.Context, level string, format string, args ...any) {
	if f != nil {
		f(ctx, level, format, args...)
	}
}

type Logger interface {
	Printf(ctx context.Context, level string, format string, args ...any)
}

type Config struct {
	ProxyURL              string
	AuthBaseURL           string
	PlatformBaseURL       string
	PlatformOAuthClientID string
	PlatformOAuthRedirect string
	PlatformOAuthAudience string
	PlatformAuth0Client   string
	UserAgent             string
	SecCHUA               string
	SecCHUAFullVersion    string
	RequestTimeout        time.Duration
	SentinelTimeout       time.Duration
	TokenExchangeTimeout  time.Duration
	WaitTimeout           time.Duration
	WaitInterval          time.Duration
	LocalRetryAttempts    int
}

type LoginOnly struct {
	cfg         Config
	mail        MailProvider
	httpFactory HTTPClientFactory
	random      RandomSource
	now         func() time.Time
	logger      Logger
}

func (c Config) withDefaults() Config {
	if c.AuthBaseURL == "" {
		c.AuthBaseURL = defaultAuthBaseURL
	}
	if c.PlatformBaseURL == "" {
		c.PlatformBaseURL = defaultPlatformBaseURL
	}
	if c.PlatformOAuthClientID == "" {
		c.PlatformOAuthClientID = defaultPlatformOAuthClientID
	}
	if c.PlatformOAuthRedirect == "" {
		c.PlatformOAuthRedirect = defaultPlatformOAuthRedirect
	}
	if c.PlatformOAuthAudience == "" {
		c.PlatformOAuthAudience = defaultPlatformOAuthAudience
	}
	if c.PlatformAuth0Client == "" {
		c.PlatformAuth0Client = defaultPlatformAuth0Client
	}
	if c.UserAgent == "" {
		c.UserAgent = defaultUserAgent
	}
	if c.SecCHUA == "" {
		c.SecCHUA = defaultSecCHUA
	}
	if c.SecCHUAFullVersion == "" {
		c.SecCHUAFullVersion = defaultSecCHUAFullVersionList
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = defaultRequestTimeout
	}
	if c.SentinelTimeout <= 0 {
		c.SentinelTimeout = defaultSentinelTimeout
	}
	if c.TokenExchangeTimeout <= 0 {
		c.TokenExchangeTimeout = defaultTokenExchangeTimeout
	}
	if c.WaitTimeout <= 0 {
		c.WaitTimeout = defaultWaitTimeout
	}
	if c.WaitInterval <= 0 {
		c.WaitInterval = defaultWaitInterval
	}
	if c.LocalRetryAttempts <= 0 {
		c.LocalRetryAttempts = 3
	}
	return c
}

type Options struct {
	Config        Config
	MailProvider  MailProvider
	AccountRepo   AccountRepository
	LogSink       LogSink
	Logger        Logger
	NameGenerator IdentityGenerator
	HTTPFactory   HTTPClientFactory
	RandomSource  RandomSource
	Now           func() time.Time
}

type BatchConfig struct {
	Mail            map[string]any `json:"mail,omitempty"`
	Proxy           string         `json:"proxy,omitempty"`
	Total           int            `json:"total"`
	Threads         int            `json:"threads"`
	Mode            RegisterMode   `json:"mode"`
	TargetQuota     int            `json:"target_quota"`
	TargetAvailable int            `json:"target_available"`
	CheckInterval   time.Duration  `json:"check_interval"`
}

func (c BatchConfig) withDefaults() BatchConfig {
	if c.Total < 1 {
		c.Total = defaultTotal
	}
	if c.Threads < 1 {
		c.Threads = defaultThreads
	}
	switch c.Mode {
	case RegisterModeQuota, RegisterModeAvailable, RegisterModeTotal:
	default:
		c.Mode = RegisterModeTotal
	}
	if c.TargetQuota < 1 {
		c.TargetQuota = 100
	}
	if c.TargetAvailable < 1 {
		c.TargetAvailable = 10
	}
	if c.CheckInterval <= 0 {
		c.CheckInterval = defaultCheckInterval
	}
	return c
}

func (c BatchConfig) WithDefaults() BatchConfig {
	return c.withDefaults()
}

type BatchStats struct {
	JobID            string     `json:"job_id,omitempty"`
	Success          int        `json:"success"`
	Fail             int        `json:"fail"`
	Done             int        `json:"done"`
	Running          int        `json:"running"`
	Threads          int        `json:"threads"`
	ElapsedSeconds   float64    `json:"elapsed_seconds"`
	AvgSeconds       float64    `json:"avg_seconds"`
	SuccessRate      float64    `json:"success_rate"`
	CurrentQuota     int        `json:"current_quota"`
	CurrentAvailable int        `json:"current_available"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	UpdatedAt        *time.Time `json:"updated_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}

type BatchState struct {
	Config  BatchConfig `json:"config"`
	Enabled bool        `json:"enabled"`
	Stats   BatchStats  `json:"stats"`
}

func (s BatchState) Clone() BatchState {
	raw, _ := json.Marshal(s)
	var out BatchState
	_ = json.Unmarshal(raw, &out)
	return out
}

var (
	ErrMailProviderRequired = errors.New("mail provider is required")
	ErrAccountRepoRequired  = errors.New("account repository is required")
	ErrCodeTimeout          = errors.New("waiting for verification code timed out")
)
