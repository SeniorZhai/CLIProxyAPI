package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

const magpieUsageTTL = time.Minute

type magpieUsageCache struct {
	mu      sync.Mutex
	entries map[string]*magpieUsageEntry
}

type magpieUsageEntry struct {
	mu        sync.Mutex
	version   uint64
	attempted time.Time
	quota     auth.QuotaState
	error     string
}

// The cache never writes observations or credentials back to the auth manager.
func (s *Server) probeMagpieUsage(ctx context.Context, snapshot *auth.Auth, force bool) (auth.QuotaState, string) {
	token, _ := snapshot.Metadata["access_token"].(string)
	if strings.TrimSpace(token) == "" {
		return snapshot.Quota, ""
	}
	requestedAt := time.Now()
	s.magpieUsage.mu.Lock()
	if s.magpieUsage.entries == nil {
		s.magpieUsage.entries = make(map[string]*magpieUsageEntry)
	}
	entry := s.magpieUsage.entries[snapshot.ID]
	if entry == nil {
		entry = &magpieUsageEntry{}
		s.magpieUsage.entries[snapshot.ID] = entry
	}
	s.magpieUsage.mu.Unlock()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.version != snapshot.CredentialVersion {
		entry.version = snapshot.CredentialVersion
		entry.attempted = time.Time{}
		entry.quota = auth.QuotaState{}
		entry.error = ""
	}
	if snapshot.Quota.ObservedAt.After(entry.quota.ObservedAt) {
		entry.quota = snapshot.Quota.Clone()
		entry.error = ""
	}
	now := time.Now()
	if entry.attempted.After(requestedAt) || (force && !entry.attempted.IsZero() && now.Sub(entry.attempted) < 30*time.Second) {
		return entry.quota, entry.error
	}
	if !force && ((!entry.attempted.IsZero() && now.Sub(entry.attempted) < magpieUsageTTL) ||
		(!entry.quota.ObservedAt.IsZero() && now.Sub(entry.quota.ObservedAt) < magpieUsageTTL)) {
		return entry.quota, entry.error
	}
	s.cfgMu.RLock()
	proxyURL := s.cfg.ProxyURL
	s.cfgMu.RUnlock()
	if snapshot.ProxyURL != "" {
		proxyURL = snapshot.ProxyURL
	}
	transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err != nil {
		entry.error = "Quota query proxy configuration is invalid"
	} else {
		if transport == nil {
			transport = proxyutil.NewDirectTransport()
		}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		var quota auth.QuotaState
		var queryError string
		if snapshot.Provider == "xai" {
			quota, queryError = fetchMagpieXAIUsage(ctx, client, token)
		} else {
			accountID, _ := snapshot.Metadata["account_id"].(string)
			quota, queryError = fetchMagpieCodexUsage(ctx, client, token, accountID)
		}
		entry.error = queryError
		if queryError == "" {
			entry.quota = quota
		}
	}
	entry.attempted = time.Now()
	return entry.quota, entry.error
}

type magpieCodexWindow struct {
	Used       *float64 `json:"used_percent"`
	Seconds    *int64   `json:"limit_window_seconds"`
	ResetAt    *int64   `json:"reset_at"`
	ResetAfter *int64   `json:"reset_after_seconds"`
}

type magpieCodexRateLimit struct {
	Primary   *magpieCodexWindow `json:"primary_window"`
	Secondary *magpieCodexWindow `json:"secondary_window"`
}

func fetchMagpieCodexUsage(ctx context.Context, client *http.Client, token, accountID string) (auth.QuotaState, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", nil)
	if err != nil {
		return auth.QuotaState{}, "Failed to build quota query"
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
	response, err := client.Do(req)
	if err != nil {
		return auth.QuotaState{}, "Quota query failed"
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return auth.QuotaState{}, fmt.Sprintf("Quota query returned HTTP %d", response.StatusCode)
	}
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return auth.QuotaState{}, "Failed to read quota response"
	}
	var data struct {
		Plan       string                `json:"plan_type"`
		Limit      *magpieCodexRateLimit `json:"rate_limit"`
		CodeReview *magpieCodexRateLimit `json:"code_review_rate_limit"`
		Additional []struct {
			Name  string                `json:"limit_name"`
			Limit *magpieCodexRateLimit `json:"rate_limit"`
		} `json:"additional_rate_limits"`
	}
	if json.Unmarshal(body, &data) != nil {
		return auth.QuotaState{}, "Invalid quota response"
	}
	quota := auth.QuotaState{ObservedAt: time.Now().UTC(), Signals: make(map[string]string)}
	add := func(prefix string, limit *magpieCodexRateLimit) {
		if limit == nil {
			return
		}
		for i, window := range []*magpieCodexWindow{limit.Primary, limit.Secondary} {
			if window == nil || window.Used == nil || window.Seconds == nil ||
				math.IsNaN(*window.Used) || math.IsInf(*window.Used, 0) || *window.Used < 0 || *window.Used > 100 || *window.Seconds < 60 {
				continue
			}
			windowPrefix := prefix + []string{"Primary-", "Secondary-"}[i]
			quota.Signals[windowPrefix+"Used-Percent"] = strconv.FormatFloat(*window.Used, 'f', -1, 64)
			quota.Signals[windowPrefix+"Window-Minutes"] = strconv.FormatInt(*window.Seconds/60, 10)
			if window.ResetAt != nil {
				quota.Signals[windowPrefix+"Reset-At"] = strconv.FormatInt(*window.ResetAt, 10)
			} else if window.ResetAfter != nil {
				quota.Signals[windowPrefix+"Reset-After-Seconds"] = strconv.FormatInt(*window.ResetAfter, 10)
			}
		}
	}
	add("X-Codex-", data.Limit)
	add("X-Codex-Code-Review-", data.CodeReview)
	for i, additional := range data.Additional {
		if i >= 8 {
			break
		}
		prefix := fmt.Sprintf("X-Codex-Additional-%d-", i+1)
		add(prefix, additional.Limit)
		if additional.Name != "" {
			quota.Signals[prefix+"Limit-Name"] = additional.Name
		}
	}
	hasWindows := false
	for key := range quota.Signals {
		if strings.HasSuffix(key, "Used-Percent") {
			hasWindows = true
			break
		}
	}
	if !hasWindows {
		return auth.QuotaState{}, "Quota response contains no valid windows"
	}
	if data.Plan != "" {
		quota.Signals["X-Codex-Plan-Type"] = data.Plan
	}
	return quota, ""
}
