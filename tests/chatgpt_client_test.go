package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gpt-image-web/internal/upstream/chatgpt"
)

func TestChatGPTUserInfoNormalizesAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token-1" {
			t.Fatalf("missing authorization header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/backend-api/me":
			json.NewEncoder(w).Encode(map[string]any{"email": "user@example.com", "id": "user-123"})
		case "/backend-api/conversation/init":
			json.NewEncoder(w).Encode(map[string]any{
				"default_model_slug": "gpt-5",
				"limits_progress": []map[string]any{
					{"feature_name": "image_gen", "remaining": 7, "reset_after": "2026-05-06T00:00:00Z"},
				},
			})
		case "/backend-api/accounts/check/v4-2023-04-27":
			json.NewEncoder(w).Encode(map[string]any{
				"accounts": map[string]any{
					"default": map[string]any{
						"account": map[string]any{"plan_type": "plus"},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	httpClient, err := chatgpt.NewHTTPClient("")
	if err != nil {
		t.Fatalf("NewHTTPClient returned error: %v", err)
	}
	account, err := chatgpt.NewClient("token-1", chatgpt.WithBaseURL(server.URL), chatgpt.WithHTTPClient(httpClient)).UserInfo(context.Background())
	if err != nil {
		t.Fatalf("UserInfo returned error: %v", err)
	}
	if account.Email != "user@example.com" || account.UserID != "user-123" {
		t.Fatalf("unexpected identity: %#v", account)
	}
	if account.Type != "plus" || account.Status != "正常" || account.Quota != 7 || account.ImageQuotaUnknown {
		t.Fatalf("unexpected quota status: %#v", account)
	}
	if account.DefaultModelSlug != "gpt-5" || account.RestoreAt != "2026-05-06T00:00:00Z" {
		t.Fatalf("unexpected model/restore: %#v", account)
	}
}

func TestChatGPTUserInfoMarksFreeUnknownQuotaLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/backend-api/me":
			json.NewEncoder(w).Encode(map[string]any{"email": "free@example.com", "id": "free-123"})
		case "/backend-api/conversation/init":
			json.NewEncoder(w).Encode(map[string]any{"limits_progress": []any{}})
		case "/backend-api/accounts/check/v4-2023-04-27":
			json.NewEncoder(w).Encode(map[string]any{
				"accounts": map[string]any{
					"default": map[string]any{
						"account": map[string]any{"plan_type": "free"},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	httpClient, err := chatgpt.NewHTTPClient("")
	if err != nil {
		t.Fatalf("NewHTTPClient returned error: %v", err)
	}
	account, err := chatgpt.NewClient("token-2", chatgpt.WithBaseURL(server.URL), chatgpt.WithHTTPClient(httpClient)).UserInfo(context.Background())
	if err != nil {
		t.Fatalf("UserInfo returned error: %v", err)
	}
	if !account.ImageQuotaUnknown || account.Status != "限流" {
		t.Fatalf("expected free unknown quota to be limited: %#v", account)
	}
}
