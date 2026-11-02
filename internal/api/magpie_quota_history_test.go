package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func readMagpieQuotaHistory(t *testing.T, server *Server, path string) ([]magpieQuotaHistory, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response struct {
		Data []magpieQuotaHistory `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data, rr.Body.String()
}

func TestMagpieQuotaHistoryAuthentication(t *testing.T) {
	server := newTestServer(t)
	for _, token := range []string{"", "invalid-key"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/magpie/quotas/history", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		body := rr.Body.String()
		if rr.Code != http.StatusUnauthorized || strings.Contains(body, "points") || strings.Contains(body, "user@example.com") {
			t.Fatalf("token %q: status = %d body = %s", token, rr.Code, body)
		}
	}
	history, body := readMagpieQuotaHistory(t, server, "/v1/magpie/quotas/history")
	if len(history) != 0 || !strings.Contains(body, `"data":[]`) {
		t.Fatalf("empty history = %s", body)
	}
}

func TestMagpieQuotaHistoryFromCards(t *testing.T) {
	server := newTestServer(t)
	observed := time.Now().UTC().Truncate(time.Second)
	reset := observed.Add(3 * 24 * time.Hour).Truncate(time.Second)
	resetUnix := strconv.FormatInt(reset.Unix(), 10)
	if _, err := server.handlers.AuthManager.Register(context.Background(), &auth.Auth{
		ID: "codex-fresh", Provider: "codex", Status: auth.StatusActive,
		Metadata: map[string]any{"email": "User@Example.com", "refresh_token": "secret-refresh"},
		Quota: auth.QuotaState{ObservedAt: observed, Signals: map[string]string{
			"X-Codex-Primary-Used-Percent": "90", "X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-At": resetUnix,
			"X-Codex-Credits-Balance": "secret-credits",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.handlers.AuthManager.Register(context.Background(), &auth.Auth{
		ID: "codex-off", Provider: "codex", Status: auth.StatusDisabled, Disabled: true,
		Metadata: map[string]any{"email": "disabled@example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	cards, body := readMagpieQuotaCards(t, server, http.MethodGet, "/v1/magpie/quotas/cards")
	if len(cards) != 2 || cards[0].Icon != "codex-color" || cards[0].AsOf != nil {
		t.Fatalf("fresh card = %s", body)
	}
	if strings.Contains(body, "secret") {
		t.Fatalf("card leaked a secret: %s", body)
	}
	history, histBody := readMagpieQuotaHistory(t, server, "/v1/magpie/quotas/history")
	if len(history) != 1 || history[0].Provider != "codex" || history[0].User != "user@example.com" {
		t.Fatalf("history = %s", histBody)
	}
	if len(history[0].Lines) != 1 || history[0].Lines[0].Name != "7d" || len(history[0].Lines[0].Points) != 1 {
		t.Fatalf("lines = %s", histBody)
	}
	point := history[0].Lines[0].Points[0]
	if point.Left != 10 || !point.At.Equal(observed) || point.ResetsAt == nil || !point.ResetsAt.Equal(reset) {
		t.Fatalf("point = %+v", point)
	}
	if point.Start == nil || !point.Start.Equal(reset.Add(-7*24*time.Hour)) {
		t.Fatalf("start = %+v", point.Start)
	}
	for _, secret := range []string{"secret", "refresh", "credits", "codex-fresh"} {
		if strings.Contains(histBody, secret) {
			t.Fatalf("history leaked %q: %s", secret, histBody)
		}
	}
	filtered, _ := readMagpieQuotaHistory(t, server, "/v1/magpie/quotas/history?provider=codex&user=USER@example.com&days=7")
	if len(filtered) != 1 || len(filtered[0].Lines[0].Points) != 1 {
		t.Fatalf("filtered = %+v", filtered)
	}
	other, _ := readMagpieQuotaHistory(t, server, "/v1/magpie/quotas/history?provider=xai")
	if len(other) != 0 {
		t.Fatalf("xai history = %+v", other)
	}
	disabled, _ := readMagpieQuotaHistory(t, server, "/v1/magpie/quotas/history?user=disabled@example.com")
	if len(disabled) != 0 {
		t.Fatalf("disabled history = %+v", disabled)
	}
}

func TestMagpieQuotaHistorySampling(t *testing.T) {
	server := newTestServer(t)
	user := "a@x.com"
	reset := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	resetText := reset.Format(time.RFC3339)
	card := magpieQuotaCard{
		Provider: "xai", User: &user,
		Windows: []magpieQuotaWindow{{Name: "7 days", Used: 20, ResetsAt: &resetText}},
	}
	base := time.Now().UTC().Truncate(time.Second).Add(-10 * 24 * time.Hour)
	stale := time.Now().UTC().Add(-50 * 24 * time.Hour).Truncate(time.Second)
	server.noteMagpieQuotaHistory([]magpieQuotaSample{{Card: card, At: stale}})
	server.noteMagpieQuotaHistory([]magpieQuotaSample{{Card: card, At: base}})
	card.Windows[0].Used = 30
	server.noteMagpieQuotaHistory([]magpieQuotaSample{{Card: card, At: base.Add(31 * time.Second)}})
	card.Windows[0].Used = 40
	server.noteMagpieQuotaHistory([]magpieQuotaSample{{Card: card, At: base.Add(40 * time.Second)}})
	card.Error = "Quota query failed"
	card.Windows[0].Used = 99
	server.noteMagpieQuotaHistory([]magpieQuotaSample{{Card: card, At: base.Add(time.Hour)}})

	history, body := readMagpieQuotaHistory(t, server, "/v1/magpie/quotas/history")
	if len(history) != 1 || history[0].Provider != "xai" || history[0].User != "a@x.com" {
		t.Fatalf("history = %s", body)
	}
	points := history[0].Lines[0].Points
	if len(points) != 2 || points[0].Left != 80 || points[1].Left != 60 {
		t.Fatalf("points = %+v", points)
	}
	if points[1].At.Sub(points[0].At) != 40*time.Second {
		t.Fatalf("near reading was kept separately: %+v", points)
	}
	if points[0].Start == nil || !points[0].Start.Equal(reset.Add(-7*24*time.Hour)) {
		t.Fatalf("span start = %+v", points[0].Start)
	}

	kept, _ := readMagpieQuotaHistory(t, server, "/v1/magpie/quotas/history")
	for _, line := range kept[0].Lines {
		for _, point := range line.Points {
			if point.At.Equal(stale.Truncate(time.Second)) {
				t.Fatalf("kept a point older than 45 days: %+v", point)
			}
		}
	}
}
