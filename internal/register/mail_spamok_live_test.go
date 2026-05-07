package register

import (
	"context"
	"crypto/rand"
	"os"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

func TestSpamOKLiveSMTPDelivery(t *testing.T) {
	if os.Getenv("SPAMOK_LIVE_TEST") != "1" {
		t.Skip("set SPAMOK_LIVE_TEST=1 to run live SpamOK SMTP delivery test")
	}
	cfg := spamOKLiveSMTPConfig{
		Host:        mustEnv(t, "SPAMOK_LIVE_SMTP_HOST"),
		Port:        mustEnvInt(t, "SPAMOK_LIVE_SMTP_PORT"),
		Username:    mustEnv(t, "SPAMOK_LIVE_SMTP_USERNAME"),
		Password:    mustEnv(t, "SPAMOK_LIVE_SMTP_PASSWORD"),
		From:        mustEnv(t, "SPAMOK_LIVE_SMTP_FROM"),
		FromName:    envOrDefault("SPAMOK_LIVE_SMTP_FROM_NAME", "GPT Image Web"),
		ReplyTo:     os.Getenv("SPAMOK_LIVE_SMTP_REPLY_TO"),
		StartTLS:    envBoolDefault("SPAMOK_LIVE_SMTP_STARTTLS", false),
		ImplicitTLS: envBoolDefault("SPAMOK_LIVE_SMTP_IMPLICIT_TLS", true),
	}
	provider, err := NewSpamOKMailProvider(SpamOKConfig{
		BaseURL:        envOrDefault("SPAMOK_LIVE_BASE_URL", "https://spamok.com"),
		Domain:         "spamok.com",
		RequestTimeout: 20 * time.Second,
		WaitTimeout:    90 * time.Second,
		WaitInterval:   5 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("NewSpamOKMailProvider() error = %v", err)
	}
	mailbox, err := provider.CreateMailbox(context.Background())
	if err != nil {
		t.Fatalf("CreateMailbox() error = %v", err)
	}
	code := randomSixDigitCode(t)
	subject := "GPT Image Web SpamOK Live Test"
	body := "Your Verification code: " + code
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := sendSpamOKLiveSMTPMail(ctx, cfg, mailbox.Address, subject, body); err != nil {
		t.Fatalf("sendSpamOKLiveSMTPMail() error = %v", err)
	}
	got, err := provider.WaitForCode(ctx, mailbox)
	if err != nil {
		t.Fatalf("WaitForCode() error = %v", err)
	}
	if got != code {
		t.Fatalf("unexpected code: got=%q want=%q mailbox=%s", got, code, mailbox.Address)
	}
	t.Logf("spamok live smoke ok mailbox=%s code=%s", mailbox.Address, got)
}

type spamOKLiveSMTPConfig struct {
	Host        string
	Port        int
	Username    string
	Password    string
	From        string
	FromName    string
	ReplyTo     string
	StartTLS    bool
	ImplicitTLS bool
}

func sendSpamOKLiveSMTPMail(ctx context.Context, cfg spamOKLiveSMTPConfig, to string, subject string, plainBody string) error {
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var client *smtp.Client
	if cfg.ImplicitTLS {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: cfg.Host,
			MinVersion: tls.VersionTLS12,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return err
		}
		client, err = smtp.NewClient(tlsConn, cfg.Host)
		if err != nil {
			tlsConn.Close()
			return err
		}
	} else {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		client, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			conn.Close()
			return err
		}
	}
	defer client.Close()

	if cfg.StartTLS && !cfg.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{
				ServerName: cfg.Host,
				MinVersion: tls.VersionTLS12,
			}); err != nil {
				return err
			}
		}
	}

	if ok, _ := client.Extension("AUTH"); ok {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return err
	}
	if err := client.Rcpt(strings.TrimSpace(to)); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	headers := []string{
		"From: " + spamOKLiveFormatAddress(cfg.FromName, cfg.From),
		"To: " + strings.TrimSpace(to),
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
	}
	if replyTo := strings.TrimSpace(cfg.ReplyTo); replyTo != "" {
		headers = append(headers, "Reply-To: "+replyTo)
	}
	message := strings.Join(headers, "\r\n") + "\r\n\r\n" + plainBody
	if _, err := writer.Write([]byte(message)); err != nil {
		writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func spamOKLiveFormatAddress(name string, address string) string {
	name = strings.TrimSpace(name)
	address = strings.TrimSpace(address)
	if name == "" {
		return address
	}
	safe := strings.ReplaceAll(name, `"`, `'`)
	return fmt.Sprintf(`"%s" <%s>`, safe, address)
}

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Fatalf("missing env %s", key)
	}
	return value
}

func mustEnvInt(t *testing.T, key string) int {
	t.Helper()
	value := mustEnv(t, key)
	var out int
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			t.Fatalf("env %s must be numeric, got %q", key, value)
		}
		out = out*10 + int(ch-'0')
	}
	return out
}

func envOrDefault(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBoolDefault(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	case "":
		return fallback
	default:
		return fallback
	}
}

func randomSixDigitCode(t *testing.T) string {
	t.Helper()
	var buf [3]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	number := (int(buf[0])<<16 | int(buf[1])<<8 | int(buf[2])) % 1000000
	return fmt.Sprintf("%06d", number)
}
