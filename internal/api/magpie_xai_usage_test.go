package api

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestFetchMagpieXAIUsage(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		status      int
		wantError   string
		wantSignals map[string]string
	}{
		{name: "weekly with on-demand", status: 200,
			body:        `{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-10-28T03:38:26.217+00:00","end":"2026-11-04T03:38:26.217+00:00"},"creditUsagePercent":34.0,"onDemandCap":{"val":200},"onDemandUsed":{"val":25},"billingPeriodEnd":"2026-11-30T00:00:00Z"}}`,
			wantSignals: map[string]string{"X-XAI-Window-Name": "7 days", "X-XAI-Used-Percent": "34", "X-XAI-Reset-At": "2026-11-04T03:38:26Z", "X-XAI-On-Demand-Used-Percent": "12.5"}},
		{name: "billing period only", status: 200,
			body:        `{"config":{"creditUsagePercent":5.5,"billingPeriodEnd":"2026-11-30T00:00:00+08:00","onDemandCap":{"val":"200"},"prepaidBalance":{"val":0}}}`,
			wantSignals: map[string]string{"X-XAI-Window-Name": "Allowance", "X-XAI-Used-Percent": "5.5", "X-XAI-Reset-At": "2026-11-29T16:00:00Z"}},
		{name: "unknown period", status: 200,
			body:        `{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_HOURLY","end":"soon"}}}`,
			wantSignals: map[string]string{"X-XAI-Window-Name": "Allowance", "X-XAI-Used-Percent": "0"}},
		{name: "invalid json", status: 200, body: `secret token`, wantError: "Invalid quota response"},
		{name: "unauthorized", status: 401, body: `secret token`, wantError: "Quota query returned HTTP 401"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: magpieUsageTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.String() != "https://cli-chat-proxy.grok.com/v1/billing?format=credits" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Accept") != "application/json" {
					t.Errorf("unexpected usage request")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			quota, errText := fetchMagpieXAIUsage(context.Background(), client, "test-token")
			if errText != tc.wantError {
				t.Fatalf("error = %q", errText)
			}
			if tc.wantSignals != nil && (quota.ObservedAt.IsZero() || !reflect.DeepEqual(quota.Signals, tc.wantSignals)) {
				t.Fatalf("signals = %v", quota.Signals)
			}
			if tc.wantSignals == nil && len(quota.Signals) != 0 {
				t.Fatalf("failed query kept signals: %v", quota.Signals)
			}
		})
	}
}
