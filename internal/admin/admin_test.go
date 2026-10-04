package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"golang.org/x/crypto/bcrypt"
)

const testPassword = "test-admin-password"

func TestBootstrapPreservesConfigurationAndAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "config.yaml")
	proxies := []string{"172.30.83.1", "2001:db8::1"}
	credentials, err := Bootstrap(path, BootstrapOptions{PublicURL: "https://proxy.example.com", TrustedProxies: proxies})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RemoteManagement.Admin.Enabled || len(cfg.APIKeys) != 0 || !cfg.RemoteManagement.AllowRemote || cfg.AuthDir != filepath.Join(filepath.Dir(path), "auths") {
		t.Fatal("unexpected bootstrap configuration")
	}
	if !reflect.DeepEqual(cfg.TrustedProxies, proxies) {
		t.Fatalf("trusted proxies = %v, want %v", cfg.TrustedProxies, proxies)
	}
	data, err := os.ReadFile(credentials)
	if err != nil {
		t.Fatal(err)
	}
	password := strings.TrimSpace(strings.SplitN(string(data), "password: ", 2)[1])
	record, err := loadAccount(StateDir(cfg.RemoteManagement.Admin, path))
	if err != nil || record.Username != "admin" || bcrypt.CompareHashAndPassword([]byte(record.PasswordHash), []byte(password)) != nil {
		t.Fatal("generated credentials do not match stored hash")
	}
	if len(password) < 24 || strings.Contains(record.PasswordHash, password) {
		t.Fatal("invalid generated password storage")
	}
	for _, file := range []string{path, credentials, filepath.Join(filepath.Dir(credentials), "account.json")} {
		info, errStat := os.Stat(file)
		if errStat != nil {
			t.Fatal(errStat)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("private file mode = %o", info.Mode().Perm())
		}
	}
	before, _ := os.ReadFile(path)
	result, err := Bootstrap(path, BootstrapOptions{PublicURL: "https://changed.example.com", Username: "other", Password: "different-password", TrustedProxies: []string{"192.0.2.1"}})
	if err != nil || result != "" {
		t.Fatalf("repeat bootstrap: %v", err)
	}
	after, _ := os.ReadFile(path)
	current, err := loadAccount(filepath.Dir(credentials))
	if err != nil || current != record || !bytes.Equal(before, after) {
		t.Fatal("repeat bootstrap replaced existing state")
	}
	resetPath, err := ResetPassword(cfg.RemoteManagement.Admin, path)
	if err != nil || resetPath != credentials {
		t.Fatalf("reset: %v", err)
	}
	resetData, _ := os.ReadFile(resetPath)
	resetPassword := strings.TrimSpace(strings.SplitN(string(resetData), "password: ", 2)[1])
	reset, err := loadAccount(filepath.Dir(credentials))
	if err != nil || reset.Username != "admin" || bcrypt.CompareHashAndPassword([]byte(reset.PasswordHash), []byte(resetPassword)) != nil || resetPassword == password {
		t.Fatal("reset did not replace the password")
	}
	if errWrite := os.WriteFile(filepath.Join(filepath.Dir(credentials), "account.json"), []byte("broken"), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, errInit := Initialize(cfg.RemoteManagement.Admin, path, "", ""); errInit == nil {
		t.Fatal("corrupt state must not silently create a new account")
	}
}

func TestBootstrapRejectsInvalidTrustedProxiesBeforeWriting(t *testing.T) {
	for _, proxies := range [][]string{{"proxy.example.com"}, {"172.30.83.1", ""}, {"172.30.83.0/99"}} {
		t.Run(strings.Join(proxies, ","), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if _, err := Bootstrap(path, BootstrapOptions{TrustedProxies: proxies}); err == nil {
				t.Fatal("invalid trusted proxy was accepted")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("invalid bootstrap left a configuration file")
			}
		})
	}
}

func newTestManager(t *testing.T) (*Manager, *gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	credentials, err := Bootstrap(path, BootstrapOptions{PublicURL: "https://proxy.example.com", Password: testPassword})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Open(cfg.RemoteManagement.Admin, path)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/login", m.Login)
	router.GET("/session", m.Session)
	router.POST("/logout", m.Logout)
	router.PUT("/password", m.ChangePassword)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		router.Handle(method, "/oauth/auth-url", func(c *gin.Context) {
			if m.Authorize(c) {
				c.Status(http.StatusNoContent)
			}
		})
	}
	return m, router, credentials
}

func adminRequest(router http.Handler, method, path, body, origin, csrf string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Origin", origin)
	r.Header.Set("X-CSRF-Token", csrf)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func loginTestAdmin(t *testing.T, m *Manager, router http.Handler, password string) (session, *http.Cookie) {
	t.Helper()
	data, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
	w := adminRequest(router, "POST", "/login", string(data), m.origin, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	var current session
	if err := json.Unmarshal(w.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	return current, w.Result().Cookies()[0]
}

func TestSessionExpiryCSRFAndLogout(t *testing.T) {
	m, router, credentials := newTestManager(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	current, cookie := loginTestAdmin(t, m, router, testPassword)
	if cookie.Path != "/v8/management" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 30*24*60*60 {
		t.Fatal("session cookie does not meet the management-only 30-day contract")
	}
	if current.ExpiresAt.Sub(now) != 30*24*time.Hour {
		t.Fatal("incorrect session lifetime")
	}
	if _, err := os.Stat(credentials); !os.IsNotExist(err) {
		t.Fatal("initial plaintext credentials remain after login")
	}
	for _, tc := range []struct{ method, origin, csrf string }{
		{"POST", m.origin, ""}, {"POST", "https://other.example.com", current.CSRF},
		{"POST", "", current.CSRF}, {"GET", m.origin, ""},
	} {
		w := adminRequest(router, tc.method, "/oauth/auth-url", "", tc.origin, tc.csrf, cookie)
		if w.Code != http.StatusForbidden {
			t.Fatalf("unsafe request accepted: %+v status=%d", tc, w.Code)
		}
	}
	if w := adminRequest(router, "GET", "/oauth/auth-url", "", m.origin, current.CSRF, cookie); w.Code != http.StatusNoContent {
		t.Fatalf("authorized OAuth start status=%d", w.Code)
	}
	now = now.Add(30*24*time.Hour - time.Second)
	if w := adminRequest(router, "GET", "/session", "", "", "", cookie); w.Code != http.StatusOK {
		t.Fatal("session expired before 30 days")
	}
	now = now.Add(time.Second)
	if w := adminRequest(router, "GET", "/session", "", "", "", cookie); w.Code != http.StatusUnauthorized {
		t.Fatal("session did not expire at 30 days")
	}
	current, cookie = loginTestAdmin(t, m, router, testPassword)
	if w := adminRequest(router, "POST", "/logout", "{}", m.origin, current.CSRF, cookie); w.Code != http.StatusOK {
		t.Fatal("logout failed")
	}
	if w := adminRequest(router, "GET", "/session", "", "", "", cookie); w.Code != http.StatusUnauthorized {
		t.Fatal("logout left session active")
	}
}

func TestPasswordChangeRevokesSessionsAndPersists(t *testing.T) {
	m, router, _ := newTestManager(t)
	first, cookie := loginTestAdmin(t, m, router, testPassword)
	_, second := loginTestAdmin(t, m, router, testPassword)
	w := adminRequest(router, "PUT", "/password", `{"current_password":"test-admin-password","new_password":"replacement-password"}`, m.origin, first.CSRF, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("change password status=%d", w.Code)
	}
	for _, previous := range []*http.Cookie{cookie, second} {
		if w := adminRequest(router, "GET", "/session", "", "", "", previous); w.Code != http.StatusUnauthorized {
			t.Fatal("password change did not revoke all sessions")
		}
	}
	record, err := loadAccount(m.dir)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(record.PasswordHash), []byte("replacement-password")) != nil {
		t.Fatal("new password was not persisted")
	}
	if w := adminRequest(router, "POST", "/login", `{"username":"admin","password":"test-admin-password"}`, m.origin, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatal("old password accepted")
	}
	loginTestAdmin(t, m, router, "replacement-password")
}

func TestLoginValidationAndThrottling(t *testing.T) {
	m, router, _ := newTestManager(t)
	now := time.Now()
	m.now = func() time.Time { return now }
	for _, body := range []string{"", `{}`, `{"extra":true}`, `{} {}`, strings.Repeat("x", 4097)} {
		w := adminRequest(router, "POST", "/login", body, m.origin, "", nil)
		if w.Code != http.StatusBadRequest && w.Code != http.StatusUnauthorized {
			t.Fatalf("invalid body status=%d", w.Code)
		}
	}
	if w := adminRequest(router, "POST", "/login", `{}`, "https://other.example.com", "", nil); w.Code != http.StatusForbidden {
		t.Fatal("cross-origin login accepted")
	}
	clear(m.attempts)
	for i := 0; i < 5; i++ {
		if w := adminRequest(router, "POST", "/login", `{"username":"admin","password":"wrong"}`, m.origin, "", nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d", i, w.Code)
		}
	}
	if w := adminRequest(router, "POST", "/login", `{"username":"admin","password":"test-admin-password"}`, m.origin, "", nil); w.Code != http.StatusTooManyRequests {
		t.Fatal("login throttling was bypassed")
	}
	now = now.Add(lockoutDuration)
	loginTestAdmin(t, m, router, testPassword)
}
