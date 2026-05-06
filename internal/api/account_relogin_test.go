package api

import "testing"

func TestReloginRequiresMailboxOTP(t *testing.T) {
	if !reloginRequiresMailboxOTP(errString("login-only flow requires a mailbox provider for otp")) {
		t.Fatal("expected mailbox otp error to be detected")
	}
	if reloginRequiresMailboxOTP(errString("password_verify_http_401")) {
		t.Fatal("did not expect non-otp relogin error to match")
	}
}

type errString string

func (e errString) Error() string {
	return string(e)
}
