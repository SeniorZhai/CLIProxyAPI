package api

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type magpieUsageTransport func(*http.Request) (*http.Response, error)

func (f magpieUsageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchMagpieCodexUsage(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  string
		count      int
	}{
		{name: "all windows", status: 200, count: 4, body: `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":92,"limit_window_seconds":604800,"reset_at":1793774127},"secondary_window":{"used_percent":0,"limit_window_seconds":18000,"reset_after_seconds":3600}},"code_review_rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":604800}},"additional_rate_limits":[{"limit_name":"Spark","rate_limit":{"primary_window":{"used_percent":40,"limit_window_seconds":18000}}}]}`},
		{name: "missing used", status: 200, body: `{"rate_limit":{"primary_window":{"limit_window_seconds":300}}}`, wantError: "Quota response contains no valid windows"},
		{name: "null used", status: 200, body: `{"rate_limit":{"primary_window":{"used_percent":null,"limit_window_seconds":300}}}`, wantError: "Quota response contains no valid windows"},
		{name: "over used", status: 200, body: `{"rate_limit":{"primary_window":{"used_percent":101,"limit_window_seconds":300}}}`, wantError: "Quota response contains no valid windows"},
		{name: "negative used", status: 200, body: `{"rate_limit":{"primary_window":{"used_percent":-1,"limit_window_seconds":300}}}`, wantError: "Quota response contains no valid windows"},
		{name: "zero window", status: 200, body: `{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":0}}}`, wantError: "Quota response contains no valid windows"},
		{name: "nan", status: 200, body: `{"rate_limit":{"primary_window":{"used_percent":NaN,"limit_window_seconds":300}}}`, wantError: "Invalid quota response"},
		{name: "invalid json", status: 200, body: `secret token`, wantError: "Invalid quota response"},
		{name: "unauthorized", status: 401, body: `secret token`, wantError: "Quota query returned HTTP 401"},
		{name: "limited", status: 429, body: `secret token`, wantError: "Quota query returned HTTP 429"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: magpieUsageTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.String() != "https://chatgpt.com/backend-api/wham/usage" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Chatgpt-Account-Id") != "test-account" || r.Header.Get("Accept") != "application/json" {
					t.Errorf("unexpected usage request")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			quota, errText := fetchMagpieCodexUsage(context.Background(), client, "test-token", "test-account")
			if errText != tc.wantError {
				t.Fatalf("error = %q", errText)
			}
			count := 0
			for key := range quota.Signals {
				if strings.HasSuffix(key, "Used-Percent") {
					count++
				}
			}
			if count != tc.count {
				t.Fatalf("windows = %d", count)
			}
			if tc.count > 0 {
				if quota.ObservedAt.IsZero() || quota.Signals["X-Codex-Plan-Type"] != "pro" || quota.Signals["X-Codex-Primary-Window-Minutes"] != "10080" || quota.Signals["X-Codex-Additional-1-Limit-Name"] != "Spark" {
					t.Fatal("incorrect quota normalization")
				}
			}
		})
	}
}

func TestMagpieCodexUsageCache(t *testing.T) {
	var calls atomic.Int32
	var status atomic.Int32
	status.Store(200)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/backend-api/wham/usage" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("unexpected upstream request")
		}
		if code := int(status.Load()); code != 200 {
			w.Header().Set("Location", "https://other.example/secret")
			w.WriteHeader(code)
			return
		}
		io.WriteString(w, `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":92,"limit_window_seconds":604800,"reset_at":1793774127}}}`)
	}))
	defer upstream.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", upstream.Listener.Addr().String())
	}}
	defer func() { http.DefaultTransport = oldTransport }()
	server := newTestServer(t)
	a := &auth.Auth{ID: "one", Provider: "codex", Status: auth.StatusActive, CredentialVersion: 1, Metadata: map[string]any{"access_token": "test-token", "account_id": "test-account"}}
	original := a.Clone()
	first, errText := server.magpieCodexUsage(context.Background(), a, false)
	if errText != "" || first.Signals["X-Codex-Primary-Used-Percent"] != "92" || calls.Load() != 1 {
		t.Fatal("cold fetch failed")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); server.magpieCodexUsage(context.Background(), a, false) }()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("cache miss caused duplicate requests")
	}
	entry := server.magpieUsage.entries[a.ID]
	entry.attempted = time.Now().Add(-29 * time.Second)
	throttled, errText := server.magpieCodexUsage(context.Background(), a, true)
	if calls.Load() != 1 || errText != "" || !reflect.DeepEqual(throttled, first) {
		t.Fatal("refresh within 30 seconds must return cached observation")
	}
	entry.attempted = time.Now().Add(-31 * time.Second)
	server.magpieCodexUsage(context.Background(), a, true)
	if calls.Load() != 2 {
		t.Fatal("refresh did not request usage")
	}
	status.Store(401)
	entry.attempted = time.Now().Add(-31 * time.Second)
	kept, errText := server.magpieCodexUsage(context.Background(), a, true)
	if errText != "Quota query returned HTTP 401" || !reflect.DeepEqual(first.Signals, kept.Signals) || kept.ObservedAt.IsZero() {
		t.Fatal("failure discarded observation")
	}
	before := calls.Load()
	_, errText = server.magpieCodexUsage(context.Background(), a, false)
	if calls.Load() != before || errText == "" {
		t.Fatal("failure was not cached")
	}
	_, errText = server.magpieCodexUsage(context.Background(), a, true)
	if calls.Load() != before || errText != "Quota query returned HTTP 401" {
		t.Fatal("failed refresh must also be throttled for 30 seconds")
	}
	status.Store(302)
	entry.attempted = time.Now().Add(-31 * time.Second)
	_, errText = server.magpieCodexUsage(context.Background(), a, true)
	if errText != "Quota query returned HTTP 302" {
		t.Fatalf("redirect followed: %s", errText)
	}
	status.Store(200)
	entry.attempted = time.Now().Add(-2 * time.Minute)
	entry.quota.ObservedAt = time.Now().Add(-2 * time.Minute)
	server.magpieCodexUsage(context.Background(), a, false)
	if calls.Load() != before+2 {
		t.Fatal("expired cache did not fetch")
	}
	a.Quota = auth.QuotaState{ObservedAt: time.Now().Add(time.Second), Signals: map[string]string{"X-Codex-Primary-Used-Percent": "95"}}
	newer, errText := server.magpieCodexUsage(context.Background(), a, false)
	if errText != "" || newer.Signals["X-Codex-Primary-Used-Percent"] != "95" {
		t.Fatal("newer passive quota ignored")
	}
	a.Quota = auth.QuotaState{}
	if !reflect.DeepEqual(a, original) {
		t.Fatal("query mutated credential")
	}
	a.CredentialVersion++
	before = calls.Load()
	server.magpieCodexUsage(context.Background(), a, false)
	if calls.Load() != before+1 {
		t.Fatal("credential replacement reused stale cache")
	}
}
