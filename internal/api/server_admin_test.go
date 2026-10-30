package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/admin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type adminTestSession struct {
	cookie *http.Cookie
	CSRF   string `json:"csrf_token"`
}

func newAdminTestServer(t *testing.T, path string) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("MANAGEMENT_PASSWORD", "")
	if _, err := admin.Bootstrap(path, admin.BootstrapOptions{PublicURL: "https://proxy.example.com", Password: "test-admin-password"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if errMkdir := os.MkdirAll(cfg.AuthDir, 0700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	s := NewServer(cfg, auth.NewManager(nil, nil, nil), nil, path)
	if s.adminErr != nil || s.admin == nil {
		t.Fatalf("administrator unavailable: %v", s.adminErr)
	}
	s.mgmt.SetConfigReloadHook(func(ctx context.Context, cfg *config.Config) { s.UpdateClientsContext(ctx, cfg) })
	s.AttachWebsocketRoute("/test/ws", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	return s
}

func (s *Server) adminTestRequest(method, path, body string, session adminTestSession, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Origin", "https://proxy.example.com")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", session.CSRF)
	if session.cookie != nil {
		r.AddCookie(session.cookie)
	}
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, r)
	return w
}

func (s *Server) loginAdminTest(t *testing.T) adminTestSession {
	t.Helper()
	w := s.adminTestRequest("POST", "/v8/management/auth/login", `{"username":"admin","password":"test-admin-password"}`, adminTestSession{}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	var current adminTestSession
	if err := json.Unmarshal(w.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	current.cookie = w.Result().Cookies()[0]
	return current
}

func TestAdminDeviceKeysPersistAcrossSessionsAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	s := newAdminTestServer(t, path)
	current := s.loginAdminTest(t)
	for _, route := range []struct{ method, path string }{
		{"GET", "/v1/models"}, {"POST", "/v1/chat/completions"}, {"POST", "/v1/responses"},
		{"POST", "/v1/messages"}, {"POST", "/v1beta/models/test:generateContent"},
		{"GET", "/v1/responses"}, {"GET", "/backend-api/codex/responses"}, {"GET", "/test/ws"},
	} {
		for _, token := range []string{"", "invalid-key"} {
			w := s.adminTestRequest(route.method, route.path, `{}`, current, token)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("empty keys or administrator cookie allowed inference: %s %d", route.path, w.Code)
			}
		}
	}
	w := s.adminTestRequest("POST", "/v8/management/client-keys", `{}`, current, "")
	var key struct{ ID, Key string }
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &key) != nil || !strings.HasPrefix(key.Key, "sk-cpa-") {
		t.Fatalf("create key status=%d", w.Code)
	}
	for _, route := range []string{"/v1/models", "/v1beta/models", "/test/ws"} {
		want := http.StatusOK
		if route == "/test/ws" {
			want = http.StatusNoContent
		}
		if w := s.adminTestRequest("GET", route, "", adminTestSession{}, key.Key); w.Code != want {
			t.Fatalf("device key rejected: %s %d", route, w.Code)
		}
	}
	for _, route := range []string{"/v8/management/client-keys", "/v8/management/credentials", "/v8/management/auth/session"} {
		if w := s.adminTestRequest("GET", route, "", adminTestSession{}, key.Key); w.Code == http.StatusOK {
			t.Fatalf("device key granted administrator access: %s", route)
		}
	}
	if w := s.adminTestRequest("PATCH", "/v8/management/config", `{"management":{"admin":{"enabled":false}}}`, current, ""); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "read_only_field") {
		t.Fatalf("admin mode changed without restart: %d", w.Code)
	}
	if w := s.adminTestRequest("GET", "/v0/management/config", "", current, ""); w.Code != http.StatusNotFound {
		t.Fatal("admin-only mode enabled legacy v0 management")
	}
	if w := s.adminTestRequest("POST", "/v8/management/auth/logout", `{}`, current, ""); w.Code != http.StatusOK {
		t.Fatal("logout failed")
	}
	if w := s.adminTestRequest("GET", "/v1/models", "", adminTestSession{}, key.Key); w.Code != http.StatusOK {
		t.Fatal("logout invalidated device key")
	}
	current = s.loginAdminTest(t)
	s = newAdminTestServer(t, path)
	if w := s.adminTestRequest("GET", "/v8/management/auth/session", "", current, ""); w.Code != http.StatusUnauthorized {
		t.Fatal("restart did not invalidate web session")
	}
	if w := s.adminTestRequest("GET", "/v1/models", "", adminTestSession{}, key.Key); w.Code != http.StatusOK {
		t.Fatal("restart invalidated device key")
	}
	current = s.loginAdminTest(t)
	if w := s.adminTestRequest("GET", "/v8/management/client-keys", "", current, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), key.Key) {
		t.Fatal("persisted key unavailable after re-login")
	}
	if w := s.adminTestRequest("DELETE", "/v8/management/client-keys/"+key.ID, "", current, ""); w.Code != http.StatusOK {
		t.Fatalf("revoke key status=%d", w.Code)
	}
	for _, route := range []string{"/v1/models", "/test/ws"} {
		if w := s.adminTestRequest("GET", route, "", adminTestSession{}, key.Key); w.Code != http.StatusUnauthorized {
			t.Fatalf("revoked final key still accepted: %s %d", route, w.Code)
		}
	}
	cfg, err := config.LoadConfig(path)
	if err != nil || len(cfg.APIKeys) != 0 {
		t.Fatal("revocation not persisted")
	}
}

func TestAdminConcurrentKeyCreationAndSaveFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	s := newAdminTestServer(t, path)
	current := s.loginAdminTest(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			if w := s.adminTestRequest("POST", "/v8/management/client-keys", `{}`, current, ""); w.Code != http.StatusCreated {
				t.Errorf("concurrent key creation status=%d", w.Code)
			}
		})
	}
	wg.Wait()
	cfg, err := config.LoadConfig(path)
	if err != nil || len(cfg.APIKeys) != 8 {
		t.Fatalf("concurrent writes lost keys: %v", err)
	}
	if errRemove := os.Remove(path); errRemove != nil {
		t.Fatal(errRemove)
	}
	if w := s.adminTestRequest("POST", "/v8/management/client-keys", `{}`, current, ""); w.Code != http.StatusInternalServerError {
		t.Fatal("failed persistence reported success")
	}
	if w := s.adminTestRequest("GET", "/v1/models", "", adminTestSession{}, cfg.APIKeys[0]); w.Code != http.StatusOK {
		t.Fatal("failed write invalidated existing key")
	}
}
