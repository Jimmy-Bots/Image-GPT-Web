package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type smtpMailConfig struct {
	Enabled     bool
	Host        string
	Port        int
	Username    string
	Password    string
	FromAddress string
	FromName    string
	ReplyTo     string
	StartTLS    bool
	ImplicitTLS bool
}

func smtpMailConfigFromSettings(settings map[string]any) smtpMailConfig {
	raw, _ := settings["smtp_mail"].(map[string]any)
	cfg := smtpMailConfig{
		Enabled:     boolMapValue(raw, "enabled"),
		Host:        intFallbackStringPort(stringMapValue(raw, "host"), 587).Host,
		Port:        intMapValue(raw, "port"),
		Username:    stringMapValue(raw, "username"),
		Password:    stringMapValue(raw, "password"),
		FromAddress: stringMapValue(raw, "from_address"),
		FromName:    stringMapValue(raw, "from_name"),
		ReplyTo:     stringMapValue(raw, "reply_to"),
		StartTLS:    !rawHasFalse(raw, "starttls"),
		ImplicitTLS: boolMapValue(raw, "implicit_tls"),
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		cfg.Port = 587
	}
	return cfg
}

type hostPort struct {
	Host string
	Port int
}

func intFallbackStringPort(host string, fallbackPort int) hostPort {
	return hostPort{Host: strings.TrimSpace(host), Port: fallbackPort}
}

func validateSMTPMailConfig(cfg smtpMailConfig) error {
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("missing smtp host")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return errors.New("invalid smtp port")
	}
	if strings.TrimSpace(cfg.Username) == "" {
		return errors.New("missing smtp username")
	}
	if strings.TrimSpace(cfg.Password) == "" {
		return errors.New("missing smtp password")
	}
	if strings.TrimSpace(cfg.FromAddress) == "" {
		return errors.New("missing from address")
	}
	return nil
}

func sendSMTPMail(ctx context.Context, cfg smtpMailConfig, to string, subject string, plainBody string) error {
	if err := validateSMTPMailConfig(cfg); err != nil {
		return err
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return errors.New("missing test recipient")
	}

	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	client, err := newSMTPClient(ctx, dialer, cfg, addr)
	if err != nil {
		return err
	}
	defer client.Close()

	if cfg.StartTLS && !cfg.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			tlsConfig := &tls.Config{
				ServerName: cfg.Host,
				MinVersion: tls.VersionTLS12,
			}
			if err := client.StartTLS(tlsConfig); err != nil {
				return explainSMTPError(cfg, "starttls", err)
			}
		}
	}

	if ok, _ := client.Extension("AUTH"); ok {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := client.Auth(auth); err != nil {
			return explainSMTPError(cfg, "auth", err)
		}
	}

	from := strings.TrimSpace(cfg.FromAddress)
	if err := client.Mail(from); err != nil {
		return explainSMTPError(cfg, "mail_from", err)
	}
	if err := client.Rcpt(to); err != nil {
		return explainSMTPError(cfg, "rcpt_to", err)
	}

	writer, err := client.Data()
	if err != nil {
		return explainSMTPError(cfg, "data", err)
	}

	headers := []string{
		"From: " + formatSMTPAddress(cfg.FromName, from),
		"To: " + to,
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
		return explainSMTPError(cfg, "write_message", err)
	}
	if err := writer.Close(); err != nil {
		return explainSMTPError(cfg, "close_writer", err)
	}
	if err := client.Quit(); err != nil {
		return explainSMTPError(cfg, "quit", err)
	}
	return nil
}

func newSMTPClient(ctx context.Context, dialer *net.Dialer, cfg smtpMailConfig, addr string) (*smtp.Client, error) {
	if cfg.ImplicitTLS {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, explainSMTPError(cfg, "dial", err)
		}
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: cfg.Host,
			MinVersion: tls.VersionTLS12,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, explainSMTPError(cfg, "implicit_tls_handshake", err)
		}
		client, err := smtp.NewClient(tlsConn, cfg.Host)
		if err != nil {
			tlsConn.Close()
			return nil, explainSMTPError(cfg, "handshake", err)
		}
		return client, nil
	}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, explainSMTPError(cfg, "dial", err)
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return nil, explainSMTPError(cfg, "handshake", err)
	}
	return client, nil
}

func formatSMTPAddress(name string, address string) string {
	name = strings.TrimSpace(name)
	address = strings.TrimSpace(address)
	if name == "" {
		return address
	}
	safe := strings.ReplaceAll(name, `"`, `'`)
	return fmt.Sprintf(`"%s" <%s>`, safe, address)
}

func explainSMTPError(cfg smtpMailConfig, stage string, err error) error {
	if err == nil {
		return nil
	}
	base := fmt.Sprintf("smtp %s failed host=%s port=%d mode=%s", stage, cfg.Host, cfg.Port, smtpSecurityMode(cfg))
	raw := strings.TrimSpace(err.Error())
	if raw == "" {
		raw = "unknown error"
	}
	hints := smtpErrorHints(cfg, stage, raw)
	if hints == "" {
		return fmt.Errorf("%s: %s", base, raw)
	}
	return fmt.Errorf("%s: %s | hint=%s", base, raw, hints)
}

func smtpErrorHints(cfg smtpMailConfig, stage string, raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return ""
	}
	if strings.Contains(lower, "eof") {
		hints := []string{"server closed the connection early"}
		if stage == "handshake" {
			hints = append(hints, "the server greeting was not received")
		}
		if stage == "implicit_tls_handshake" {
			hints = append(hints, "the tls handshake did not complete")
		}
		if cfg.Port == 465 && cfg.StartTLS {
			hints = append(hints, "port 465 usually expects implicit TLS or SSL, not STARTTLS")
		}
		if cfg.Port == 465 && !cfg.ImplicitTLS {
			hints = append(hints, "port 465 usually expects implicit TLS or SSL")
		}
		if cfg.Port == 587 && !cfg.StartTLS {
			hints = append(hints, "port 587 commonly requires STARTTLS")
		}
		if cfg.Port == 587 && cfg.ImplicitTLS {
			hints = append(hints, "port 587 usually uses STARTTLS instead of implicit TLS")
		}
		if cfg.Port == 25 && cfg.StartTLS {
			hints = append(hints, "some port 25 servers do not advertise STARTTLS")
		}
		return strings.Join(hints, "; ")
	}
	if strings.Contains(lower, "certificate") || strings.Contains(lower, "tls") || strings.Contains(lower, "handshake failure") {
		return "tls negotiation failed; check host name, port, STARTTLS setting, and server certificate requirements"
	}
	if strings.Contains(lower, "auth") || strings.Contains(lower, "535") || strings.Contains(lower, "534") || strings.Contains(lower, "authentication") {
		return "authentication was rejected; check username, password or app password, and whether smtp auth is enabled"
	}
	if strings.Contains(lower, "553") || strings.Contains(lower, "from") {
		if stage == "mail_from" {
			return "sender address was rejected; check from address format and whether the account is allowed to send as this address"
		}
	}
	if strings.Contains(lower, "550") || strings.Contains(lower, "551") || strings.Contains(lower, "552") || strings.Contains(lower, "553") || strings.Contains(lower, "554") {
		if stage == "rcpt_to" {
			return "recipient was rejected; check recipient address format or whether the smtp provider blocks this destination"
		}
	}
	if strings.Contains(lower, "connection refused") || strings.Contains(lower, "no route to host") || strings.Contains(lower, "i/o timeout") || strings.Contains(lower, "deadline exceeded") {
		return "network connection failed; check smtp host, port, firewall, docker egress, and provider accessibility"
	}
	if strings.Contains(lower, "missing port in address") {
		return "smtp address is incomplete; check host and port"
	}
	return ""
}

func smtpSecurityMode(cfg smtpMailConfig) string {
	if cfg.ImplicitTLS {
		return "implicit_tls"
	}
	if cfg.StartTLS {
		return "starttls"
	}
	return "plain"
}

func TestOnlySMTPMailConfigFromSettingsMap(settings map[string]any) smtpMailConfig {
	return smtpMailConfigFromSettings(settings)
}

func TestOnlySendSMTPMail(ctx context.Context, cfg smtpMailConfig, to string, subject string, plainBody string) error {
	return sendSMTPMail(ctx, cfg, to, subject, plainBody)
}
