package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/admin"
)

func TestAdminLoginTrustedProxyIsolation(t *testing.T) {
	for _, proxy := range []string{"172.30.83.1:50000", "[2001:db8::1]:50000"} {
		t.Run(proxy, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if _, err := admin.Bootstrap(path, admin.BootstrapOptions{
				PublicURL: "https://proxy.example.com", Password: "test-admin-password",
				TrustedProxies: []string{"172.30.83.1", "2001:db8::1"},
			}); err != nil {
				t.Fatal(err)
			}
			s := newAdminTestServer(t, path)
			for range 5 {
				if status := proxyLogin(s, proxy, "198.51.100.10", "wrong"); status != http.StatusUnauthorized {
					t.Fatalf("failed login status=%d", status)
				}
			}
			if status := proxyLogin(s, proxy, "198.51.100.10", "test-admin-password"); status != http.StatusTooManyRequests {
				t.Fatalf("throttled client status=%d", status)
			}
			if status := proxyLogin(s, proxy, "203.0.113.20", "test-admin-password"); status != http.StatusOK {
				t.Fatalf("another client was locked out: status=%d", status)
			}
			if status := proxyLogin(s, proxy, "203.0.113.20, 198.51.100.10", "test-admin-password"); status != http.StatusTooManyRequests {
				t.Fatalf("prepended forwarded address bypassed throttling: status=%d", status)
			}
		})
	}
}

func TestAdminLoginUntrustedPeerCannotSpoofClientIP(t *testing.T) {
	for _, proxies := range [][]string{nil, {"172.30.83.1"}} {
		t.Run(strings.Join(proxies, ","), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if _, err := admin.Bootstrap(path, admin.BootstrapOptions{
				PublicURL: "https://proxy.example.com", Password: "test-admin-password", TrustedProxies: proxies,
			}); err != nil {
				t.Fatal(err)
			}
			s := newAdminTestServer(t, path)
			for _, forwarded := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3", "::ffff:198.51.100.4", ""} {
				if status := proxyLogin(s, "192.0.2.10:50000", forwarded, "wrong"); status != http.StatusUnauthorized {
					t.Fatalf("failed login status=%d", status)
				}
			}
			if status := proxyLogin(s, "192.0.2.10:50000", "203.0.113.20", "test-admin-password"); status != http.StatusTooManyRequests {
				t.Fatalf("untrusted peer bypassed throttling: status=%d", status)
			}
			if status := proxyLogin(s, "192.0.2.20:50000", "203.0.113.20", "test-admin-password"); status != http.StatusOK {
				t.Fatalf("independent direct client status=%d", status)
			}
			cfg := s.getConfig().CloneForRuntime()
			cfg.RemoteManagement.AllowRemote = false
			s.UpdateClients(cfg)
			if status := proxyLogin(s, "192.0.2.30:50000", "127.0.0.1", "test-admin-password"); status != http.StatusForbidden {
				t.Fatalf("spoofed loopback bypassed remote access policy: status=%d", status)
			}
		})
	}
}

func proxyLogin(s *Server, peer, forwarded, password string) int {
	r := httptest.NewRequest(http.MethodPost, "/v8/management/auth/login", strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
	r.RemoteAddr = peer
	r.Header.Set("Origin", "https://proxy.example.com")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-For", forwarded)
	r.Header.Set("X-Real-IP", "203.0.113.99")
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, r)
	return w.Code
}
