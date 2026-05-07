package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminCreatesUserWithSingleAPIKeyAndPasswordLogin(t *testing.T) {
	server, cleanup := newTestServer(t, fakeStreamUpstream{})
	defer cleanup()

	createReq := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"email":"user@example.com","name":"Normal User","password":"password123","role":"user"}`))
	createReq.Header.Set("Authorization", "Bearer dev-key")
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create user failed: %d body=%s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Item struct {
			ID     string `json:"id"`
			APIKey struct {
				ID string `json:"id"`
			} `json:"api_key"`
		} `json:"item"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Item.ID == "" || created.Item.APIKey.ID == "" || !strings.HasPrefix(created.Key, "sk-") {
		t.Fatalf("create response missing user API key: %s", createRec.Body.String())
	}
	assertLogContains(t, server, "user", "创建用户")
	assertLogQueryContains(t, server, map[string]string{
		"type":       "user",
		"subject_id": created.Item.ID,
		"query":      "创建用户",
	}, "创建用户")

	loginReq := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"email":"user@example.com","password":"password123"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK || !strings.Contains(loginRec.Body.String(), `"token":"session.`) {
		t.Fatalf("normal user login failed: %d body=%s", loginRec.Code, loginRec.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/api/users/"+created.Item.ID+"/api-key/reset", nil)
	secondReq.Header.Set("Authorization", "Bearer dev-key")
	secondRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK || !strings.Contains(secondRec.Body.String(), `"key":"sk-`) {
		t.Fatalf("reset key failed: %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	var reset struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(secondRec.Body.Bytes(), &reset); err != nil {
		t.Fatalf("decode reset response: %v", err)
	}
	if reset.Key == "" || reset.Key == created.Key {
		t.Fatalf("reset did not return a new key: old=%q new=%q", created.Key, reset.Key)
	}
	assertLogContains(t, server, "user", "重置用户 API Key")
	oldKeyReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	oldKeyReq.Header.Set("Authorization", "Bearer "+created.Key)
	oldKeyRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(oldKeyRec, oldKeyReq)
	if oldKeyRec.Code != http.StatusUnauthorized {
		t.Fatalf("old key should be invalid after reset, got %d body=%s", oldKeyRec.Code, oldKeyRec.Body.String())
	}
	newKeyReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	newKeyReq.Header.Set("Authorization", "Bearer "+reset.Key)
	newKeyRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(newKeyRec, newKeyReq)
	if newKeyRec.Code != http.StatusOK {
		t.Fatalf("new key should call api, got %d body=%s", newKeyRec.Code, newKeyRec.Body.String())
	}
}

func TestAPIKeyCannotLoginButCanCallAPI(t *testing.T) {
	server, cleanup := newTestServer(t, fakeStreamUpstream{})
	defer cleanup()

	createReq := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"email":"api-user@example.com","password":"password123","role":"user"}`))
	createReq.Header.Set("Authorization", "Bearer dev-key")
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create user failed: %d body=%s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{}`))
	loginReq.Header.Set("Authorization", "Bearer "+created.Key)
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusBadRequest {
		t.Fatalf("api key should not login, got %d body=%s", loginRec.Code, loginRec.Body.String())
	}

	modelReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelReq.Header.Set("Authorization", "Bearer "+created.Key)
	modelRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(modelRec, modelReq)
	if modelRec.Code != http.StatusOK {
		t.Fatalf("api key should call api, got %d body=%s", modelRec.Code, modelRec.Body.String())
	}
}

func TestAdminCannotCreateAdditionalAPIKeys(t *testing.T) {
	server, cleanup := newTestServer(t, fakeStreamUpstream{})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/me/api-keys", strings.NewReader(`{"name":"another key"}`))
	req.Header.Set("Authorization", "Bearer dev-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "key_creation_disabled") {
		t.Fatalf("admin should not create standalone api keys, got %d body=%s", rec.Code, rec.Body.String())
	}
}
