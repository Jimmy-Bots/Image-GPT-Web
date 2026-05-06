package api

import (
	"strings"
	"testing"
)

func TestExplainSMTPErrorAddsUsefulHandshakeHint(t *testing.T) {
	cfg := smtpMailConfig{
		Host:     "smtp.example.com",
		Port:     465,
		StartTLS: true,
	}
	err := explainSMTPError(cfg, "handshake", errString("EOF"))
	got := err.Error()
	if !strings.Contains(got, "smtp handshake failed host=smtp.example.com port=465 mode=starttls") {
		t.Fatalf("missing base context: %s", got)
	}
	if !strings.Contains(got, "port 465 usually expects implicit TLS or SSL, not STARTTLS") {
		t.Fatalf("missing 465 starttls hint: %s", got)
	}
}

func TestExplainSMTPErrorAddsAuthHint(t *testing.T) {
	cfg := smtpMailConfig{
		Host:     "smtp.example.com",
		Port:     587,
		StartTLS: true,
	}
	err := explainSMTPError(cfg, "auth", errString("535 Authentication credentials invalid"))
	got := err.Error()
	if !strings.Contains(got, "authentication was rejected") {
		t.Fatalf("missing auth hint: %s", got)
	}
}

func TestExplainSMTPErrorAddsRecipientHint(t *testing.T) {
	cfg := smtpMailConfig{
		Host:     "smtp.example.com",
		Port:     587,
		StartTLS: true,
	}
	err := explainSMTPError(cfg, "rcpt_to", errString("550 mailbox unavailable"))
	got := err.Error()
	if !strings.Contains(got, "recipient was rejected") {
		t.Fatalf("missing rcpt hint: %s", got)
	}
}

func TestSMTPImplicitTLSModeLabel(t *testing.T) {
	cfg := smtpMailConfig{
		Host:        "smtp.example.com",
		Port:        465,
		ImplicitTLS: true,
	}
	err := explainSMTPError(cfg, "implicit_tls_handshake", errString("EOF"))
	got := err.Error()
	if !strings.Contains(got, "mode=implicit_tls") {
		t.Fatalf("missing implicit tls mode label: %s", got)
	}
}
