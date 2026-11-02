package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func readQuotaSummary(t *testing.T, server *Server) ([]quotaSummaryAccount, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/quota-summary", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("quota response must not be cached")
	}
	var response struct {
		Providers map[string]struct {
			Accounts []quotaSummaryAccount `json:"accounts"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Providers) != 1 {
		t.Fatalf("unexpected providers: %v", response.Providers)
	}
	return response.Providers["codex"].Accounts, rr.Body.String()
}

func TestQuotaSummaryAuthentication(t *testing.T) {
	server := newTestServer(t)
	for _, token := range []string{"", "invalid-key"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/quota-summary", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: status = %d, want 401", token, rr.Code)
		}
		if strings.Contains(rr.Body.String(), "providers") {
			t.Fatal("unauthorized response contains quota data")
		}
	}
	accounts, body := readQuotaSummary(t, server)
	if len(accounts) != 0 || !strings.Contains(body, `"accounts":[]`) {
		t.Fatalf("empty accounts = %s", body)
	}
}

func TestQuotaSummaryAccountsAndWindows(t *testing.T) {
	server := newTestServer(t)
	observed := time.Date(2026, 10, 29, 12, 0, 0, 0, time.FixedZone("offset", 3600))
	for _, entry := range []*auth.Auth{
		{ID: "b-private", Provider: "codex", Status: auth.StatusActive},
		{ID: "a-private", Index: "sensitive-index", FileName: "private-file", Label: "private-label", Provider: "codex", Status: auth.StatusError, Unavailable: true,
			Metadata:   map[string]any{"email": "private@example.com", "access_token": "private-token"},
			Attributes: map[string]string{"api_key": "private-key"},
			Quota: auth.QuotaState{ObservedAt: observed, Signals: map[string]string{
				"X-Codex-Primary-Used-Percent": "90", "X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-At": "1793275200",
				"X-Codex-Secondary-Used-Percent": "12.5", "X-Codex-Secondary-Window-Minutes": "300", "X-Codex-Secondary-Reset-After-Seconds": "3600",
				"X-Codex-Plan-Type": "private-plan", "X-Codex-Credits-Balance": "private-credits", "X-Codex-Additional-Private-Used-Percent": "20",
			}}},
		{ID: "disabled-flag", Provider: "codex", Status: auth.StatusActive, Disabled: true},
		{ID: "disabled-status", Provider: "codex", Status: auth.StatusDisabled},
		{ID: "pending", Provider: "codex", Status: auth.StatusPending},
		{ID: "unknown", Provider: "codex", Status: auth.StatusUnknown},
		{ID: "xai", Provider: "xai", Status: auth.StatusActive},
	} {
		if _, err := server.handlers.AuthManager.Register(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	before := server.handlers.AuthManager.List()
	accounts, body := readQuotaSummary(t, server)
	if len(accounts) != 2 {
		t.Fatalf("accounts = %s", body)
	}
	first := accounts[0]
	if first.ObservedAt == nil || *first.ObservedAt != "2026-10-29T11:00:00Z" || first.PrimaryRemainingPercent == nil || *first.PrimaryRemainingPercent != 10 {
		t.Fatalf("first account = %s", body)
	}
	if len(first.Windows) != 2 {
		t.Fatalf("windows = %s", body)
	}
	primary, secondary := first.Windows[0], first.Windows[1]
	if primary.Name != "primary" || primary.WindowMinutes != 10080 || primary.UsedPercent == nil || *primary.UsedPercent != 90 || primary.RemainingPercent == nil || *primary.RemainingPercent != 10 || primary.ResetAt == nil || *primary.ResetAt != time.Unix(1793275200, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("primary = %s", body)
	}
	if secondary.Name != "secondary" || secondary.WindowMinutes != 300 || secondary.RemainingPercent == nil || *secondary.RemainingPercent != 87.5 || secondary.ResetAt == nil || *secondary.ResetAt != "2026-10-29T12:00:00Z" {
		t.Fatalf("secondary = %s", body)
	}
	if accounts[1].ObservedAt != nil || accounts[1].PrimaryRemainingPercent != nil || len(accounts[1].Windows) != 0 || !strings.Contains(body, `"observed_at":null,"primary_remaining_percent":null,"windows":[]`) {
		t.Fatalf("unknown account = %s", body)
	}
	for _, secret := range []string{"private", "sensitive-index", "signals", "email", "api_key", "access_token", "credits", "auth_index"} {
		if strings.Contains(body, secret) {
			t.Fatalf("leaked %q: %s", secret, body)
		}
	}
	_, again := readQuotaSummary(t, server)
	if again != body {
		t.Fatal("account order is unstable")
	}
	after := server.handlers.AuthManager.List()
	byID := make(map[string]*auth.Auth)
	for _, a := range before {
		byID[a.ID] = a
	}
	for _, a := range after {
		if !reflect.DeepEqual(byID[a.ID], a) {
			t.Fatalf("read mutated auth %s", a.ID)
		}
	}
	snapshots := server.handlers.AuthManager.List()
	for _, a := range snapshots {
		if a.ID == "a-private" {
			a.Quota.Signals["X-Codex-Primary-Used-Percent"] = "1"
		}
	}
	_, again = readQuotaSummary(t, server)
	if again != body {
		t.Fatal("List does not isolate quota signals")
	}
}

func TestQuotaSummaryInvalidFields(t *testing.T) {
	server := newTestServer(t)
	observed := time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC)
	tests := []struct {
		name, used, minutes, reset, after string
		observed                          time.Time
		wantWindows                       int
		wantRemaining                     *float64
		wantReset                         string
	}{
		{name: "missing", observed: observed},
		{name: "zero window", observed: observed, used: "0", minutes: "0"},
		{name: "negative window", observed: observed, used: "10", minutes: "-1"},
		{name: "invalid window", observed: observed, used: "10", minutes: "NaN"},
		{name: "fractional window", observed: observed, used: "10", minutes: "0.5"},
		{name: "overflow window", observed: observed, used: "10", minutes: "9223372036854775808"},
		{name: "missing used", observed: observed, minutes: "300", wantWindows: 1},
		{name: "invalid used", observed: observed, used: "invalid", minutes: "300", wantWindows: 1},
		{name: "nan", observed: observed, used: "NaN", minutes: "300", wantWindows: 1},
		{name: "infinity", observed: observed, used: "+Inf", minutes: "300", wantWindows: 1},
		{name: "negative infinity", observed: observed, used: "-Inf", minutes: "300", wantWindows: 1},
		{name: "negative used", observed: observed, used: "-1", minutes: "300", wantWindows: 1},
		{name: "over used", observed: observed, used: "100.01", minutes: "300", wantWindows: 1},
		{name: "empty quota", observed: time.Time{}, used: "10", minutes: "300"},
		{name: "invalid observation", observed: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), used: "10", minutes: "300"},
		{name: "zero used", observed: observed, used: "0", minutes: "300", wantWindows: 1, wantRemaining: newFloat64(100)},
		{name: "full used", observed: observed, used: "100", minutes: "300", wantWindows: 1, wantRemaining: newFloat64(0)},
		{name: "whitespace", observed: observed, used: " 90 ", minutes: " 300 ", wantWindows: 1, wantRemaining: newFloat64(10)},
		{name: "invalid reset", observed: observed, minutes: "300", reset: "invalid", after: "1", wantWindows: 1},
		{name: "zero reset", observed: observed, minutes: "300", reset: "0", wantWindows: 1},
		{name: "negative reset", observed: observed, minutes: "300", reset: "-1", wantWindows: 1},
		{name: "overflow reset", observed: observed, minutes: "300", reset: "9223372036854775808", wantWindows: 1},
		{name: "out of range reset", observed: observed, minutes: "300", reset: "253402300800", wantWindows: 1},
		{name: "negative after", observed: observed, minutes: "300", after: "-1", wantWindows: 1},
		{name: "overflow after", observed: observed, minutes: "300", after: "9223372036854775807", wantWindows: 1},
		{name: "zero after", observed: observed, minutes: "300", after: "0", wantWindows: 1, wantReset: "2026-10-29T11:00:00Z"},
		{name: "past reset", observed: observed, minutes: "300", reset: "1", wantWindows: 1, wantReset: "1970-01-01T00:00:01Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := &auth.Auth{ID: "test", Provider: "codex", Status: auth.StatusActive, Quota: auth.QuotaState{ObservedAt: tc.observed, Signals: map[string]string{
				"X-Codex-Primary-Used-Percent": tc.used, "X-Codex-Primary-Window-Minutes": tc.minutes,
				"X-Codex-Primary-Reset-At": tc.reset, "X-Codex-Primary-Reset-After-Seconds": tc.after,
			}}}
			if _, err := server.handlers.AuthManager.Register(context.Background(), entry); err != nil {
				t.Fatal(err)
			}
			accounts, body := readQuotaSummary(t, server)
			if len(accounts) != 1 || len(accounts[0].Windows) != tc.wantWindows || !reflect.DeepEqual(accounts[0].PrimaryRemainingPercent, tc.wantRemaining) {
				t.Fatalf("unexpected summary = %s", body)
			}
			if tc.wantWindows == 0 {
				return
			}
			window := accounts[0].Windows[0]
			if !reflect.DeepEqual(window.RemainingPercent, tc.wantRemaining) || (window.UsedPercent == nil) != (tc.wantRemaining == nil) {
				t.Fatalf("invalid percentages = %s", body)
			}
			if tc.wantReset == "" {
				if window.ResetAt != nil {
					t.Fatalf("reset should be null: %s", body)
				}
			} else if window.ResetAt == nil || *window.ResetAt != tc.wantReset {
				t.Fatalf("reset = %s", body)
			}
		})
	}
}

func newFloat64(value float64) *float64 { return &value }

func readMagpieQuotaCards(t *testing.T, server *Server, method, path string) ([]magpieQuotaCard, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response struct {
		Data []magpieQuotaCard `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data, rr.Body.String()
}

func TestMagpieQuotaCardsAuthentication(t *testing.T) {
	server := newTestServer(t)
	for _, route := range []struct{ method, path string }{{http.MethodGet, "/v1/magpie/quotas/cards"}, {http.MethodPost, "/v1/magpie/quotas/refresh"}} {
		for _, token := range []string{"", "invalid-key"} {
			req := httptest.NewRequest(route.method, route.path, nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized || strings.Contains(rr.Body.String(), "data") {
				t.Fatalf("%s %s token %q: status = %d body = %s", route.method, route.path, token, rr.Code, rr.Body.String())
			}
		}
		cards, body := readMagpieQuotaCards(t, server, route.method, route.path)
		if len(cards) != 0 || !strings.Contains(body, `"data":[]`) {
			t.Fatalf("empty cards = %s", body)
		}
	}
}

func TestMagpieQuotaCards(t *testing.T) {
	server := newTestServer(t)
	observed := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for _, entry := range []*auth.Auth{
		{ID: "codex-b", Provider: "codex", Status: auth.StatusActive, Label: "label-user", Attributes: map[string]string{"plan_type": "plus"}},
		{ID: "codex-a", Index: "sensitive-index", FileName: "secret-file.json", Provider: "codex", Status: auth.StatusError,
			Metadata:   map[string]any{"email": "user@example.com", "refresh_token": "secret-refresh"},
			Attributes: map[string]string{"plan_type": "plus"},
			Quota: auth.QuotaState{ObservedAt: observed, Signals: map[string]string{
				"X-Codex-Plan-Type":            "pro",
				"X-Codex-Primary-Used-Percent": "90", "X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-At": "1793275200",
				"X-Codex-Secondary-Used-Percent": "12.5", "X-Codex-Secondary-Window-Minutes": "300", "X-Codex-Secondary-Reset-After-Seconds": "3600",
				"X-Codex-Credits-Balance":                  "secret-credits",
				"X-Codex-Code-Review-Primary-Used-Percent": "5", "X-Codex-Code-Review-Primary-Window-Minutes": "10080", "X-Codex-Code-Review-Primary-Reset-At": "1793275200",
				"X-Codex-Additional-Gpt-Spark-Primary-Used-Percent": "40", "X-Codex-Additional-Gpt-Spark-Primary-Window-Minutes": "300", "X-Codex-Additional-Gpt-Spark-Limit-Name": "GPT-Spark",
				"X-Codex-Bengalfox-Secondary-Used-Percent": "1", "X-Codex-Bengalfox-Secondary-Window-Minutes": "60",
				"X-Codex-Broken-Primary-Used-Percent": "101", "X-Codex-Broken-Primary-Window-Minutes": "300",
			}}},
		{ID: "codex-c", Provider: "codex", Status: auth.StatusDisabled, Disabled: true, Metadata: map[string]any{"email": "disabled@example.com"}},
		{ID: "codex-d", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{"api_key": "secret-api-key"},
			Quota: auth.QuotaState{ObservedAt: observed, Signals: map[string]string{"X-Codex-Primary-Used-Percent": "NaN", "X-Codex-Primary-Window-Minutes": "45"}}},
		{ID: "xai-a", Provider: "xai", Status: auth.StatusActive, Metadata: map[string]any{"email": "xai@example.com"}},
		{ID: "xai-b", Provider: "xai", Status: auth.StatusActive, Metadata: map[string]any{"email": "grok@example.com"},
			Quota: auth.QuotaState{ObservedAt: observed, Signals: map[string]string{
				"X-XAI-Window-Name": "7 days", "X-XAI-Used-Percent": "34", "X-XAI-Reset-At": "2026-11-04T03:38:26Z", "X-XAI-On-Demand-Used-Percent": "12.5",
			}}},
		{ID: "claude-a", Provider: "claude", Status: auth.StatusActive},
	} {
		if _, err := server.handlers.AuthManager.Register(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	cards, body := readMagpieQuotaCards(t, server, http.MethodGet, "/v1/magpie/quotas/cards")
	if len(cards) != 6 {
		t.Fatalf("cards = %s", body)
	}
	pro, plan, label := "pro", "plus", "label-user"
	email, disabledEmail, xaiEmail, grokEmail := "user@example.com", "disabled@example.com", "xai@example.com", "grok@example.com"
	xaiReset := "2026-11-04T03:38:26Z"
	asOf := "2026-10-29T12:00:00Z"
	weeklyReset := time.Unix(1793275200, 0).UTC().Format(time.RFC3339)
	shortReset := "2026-10-29T13:00:00Z"
	readAt := cards[0].ReadAt
	if _, err := time.Parse(time.RFC3339, readAt); err != nil {
		t.Fatalf("readAt = %q", readAt)
	}
	want := []magpieQuotaCard{
		{Provider: "codex", Name: "Codex", Icon: "codex-color", Kind: "subscription", Plan: &pro, User: &email, ReadAt: readAt, AsOf: &asOf,
			Windows: []magpieQuotaWindow{{Name: "7d", Used: 90, ResetsAt: &weeklyReset}, {Name: "5h", Used: 12.5, ResetsAt: &shortReset},
				{Name: "Code review 7d", Used: 5, ResetsAt: &weeklyReset}, {Name: "GPT-Spark 5h", Used: 40}, {Name: "Bengalfox 1h", Used: 1}}},
		{Provider: "codex", Name: "Codex", Icon: "codex-color", Kind: "subscription", Plan: &plan, User: &label, ReadAt: readAt, Windows: []magpieQuotaWindow{}, Error: "No quota observation yet"},
		{Provider: "codex", Name: "Codex", Icon: "codex-color", Kind: "subscription", User: &disabledEmail, ReadAt: readAt, Windows: []magpieQuotaWindow{}, Error: "Account disabled"},
		{Provider: "codex", Name: "Codex", Icon: "codex-color", Kind: "subscription", ReadAt: readAt, AsOf: &asOf, Windows: []magpieQuotaWindow{}, Error: "No quota observation yet"},
		{Provider: "xai", Name: "xAI", Icon: "xai", Kind: "subscription", User: &xaiEmail, ReadAt: readAt, Windows: []magpieQuotaWindow{}, Error: "No quota observation yet"},
		{Provider: "xai", Name: "xAI", Icon: "xai", Kind: "subscription", User: &grokEmail, ReadAt: readAt, AsOf: &asOf,
			Windows: []magpieQuotaWindow{{Name: "7 days", Used: 34, ResetsAt: &xaiReset}, {Name: "On-demand", Used: 12.5, ResetsAt: &xaiReset}}},
	}
	if !reflect.DeepEqual(cards, want) {
		t.Fatalf("cards = %s", body)
	}
	for _, secret := range []string{"secret", "sensitive-index", "codex-a", "signals", "access_token", "api_key", "credits", "auth_index", "from"} {
		if strings.Contains(body, secret) {
			t.Fatalf("leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, `"plan":null,"user":null`) || strings.Contains(body, `"resetsAt":null`) {
		t.Fatalf("unexpected null handling: %s", body)
	}
	refreshed, _ := readMagpieQuotaCards(t, server, http.MethodPost, "/v1/magpie/quotas/refresh")
	for i := range refreshed {
		refreshed[i].ReadAt = readAt
	}
	if !reflect.DeepEqual(refreshed, want) {
		t.Fatal("refresh differs from cached cards")
	}
}

func TestMagpieLimit(t *testing.T) {
	server := newTestServer(t)
	for _, token := range []string{"", "invalid-key", "test-key"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/magpie/limit", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if token == "test-key" {
			if rr.Code != http.StatusOK || rr.Body.String() != `{"limited":false}` {
				t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
			}
		} else if rr.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: status = %d", token, rr.Code)
		}
	}
}
