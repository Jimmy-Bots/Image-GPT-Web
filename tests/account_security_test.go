package tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gpt-image-web/internal/api"
)

func TestAccountsListDoesNotExposeRawTokens(t *testing.T) {
	server, cleanup := newTestServer(t, fakeStreamUpstream{})
	defer cleanup()

	rawToken := "secret-access-token-for-account"
	addReq := httptest.NewRequest(http.MethodPost, "/api/accounts", strings.NewReader(`{"tokens":["`+rawToken+`"]}`))
	addReq.Header.Set("Authorization", "Bearer dev-key")
	addReq.Header.Set("Content-Type", "application/json")
	addRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(addRec, addReq)
	if addRec.Code != http.StatusOK {
		t.Fatalf("unexpected add status: %d body=%s", addRec.Code, addRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	listReq.Header.Set("Authorization", "Bearer dev-key")
	listRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("unexpected list status: %d body=%s", listRec.Code, listRec.Body.String())
	}
	body := listRec.Body.String()
	if strings.Contains(body, rawToken) || strings.Contains(body, `"access_token"`) {
		t.Fatalf("account list leaked token: %s", body)
	}
	if !strings.Contains(body, `"token_ref"`) || !strings.Contains(body, `"access_token_masked"`) {
		t.Fatalf("account list missing public token fields: %s", body)
	}
}

func TestAccountRefreshErrorsDoNotExposeRawTokens(t *testing.T) {
	rawToken := "account-token-that-must-stay-private"
	server, cleanup := newTestServer(t, refreshErrorUpstream{token: rawToken})
	defer cleanup()

	addReq := httptest.NewRequest(http.MethodPost, "/api/accounts", strings.NewReader(`{"tokens":["`+rawToken+`"]}`))
	addReq.Header.Set("Authorization", "Bearer dev-key")
	addReq.Header.Set("Content-Type", "application/json")
	addRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(addRec, addReq)
	if addRec.Code != http.StatusOK {
		t.Fatalf("unexpected add status: %d body=%s", addRec.Code, addRec.Body.String())
	}

	refreshReq := httptest.NewRequest(http.MethodPost, "/api/accounts/refresh", strings.NewReader(`{}`))
	refreshReq.Header.Set("Authorization", "Bearer dev-key")
	refreshReq.Header.Set("Content-Type", "application/json")
	refreshRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(refreshRec, refreshReq)
	if refreshRec.Code != http.StatusOK {
		t.Fatalf("unexpected refresh status: %d body=%s", refreshRec.Code, refreshRec.Body.String())
	}
	body := refreshRec.Body.String()
	if strings.Contains(body, rawToken) {
		t.Fatalf("refresh response leaked raw token: %s", body)
	}
	if !strings.Contains(body, `"access_token":"accoun...vate"`) {
		t.Fatalf("refresh response missing masked token: %s", body)
	}
}

type refreshErrorUpstream struct {
	fakeStreamUpstream
	token string
}

func (u refreshErrorUpstream) RefreshAccounts(ctx context.Context, tokens []string) (int, []map[string]string) {
	return 0, []map[string]string{{"access_token": u.token, "error": "boom"}}
}

var _ api.Upstream = refreshErrorUpstream{}
