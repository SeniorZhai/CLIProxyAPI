package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// magpieXAIPeriods names the billing period the way Magpie's Grok plugin
// (@magpie-community/opencode-grok-auth) does; any other period is "Allowance".
var magpieXAIPeriods = map[string]string{"DAILY": "1 day", "WEEKLY": "7 days", "MONTHLY": "Month"}

// fetchMagpieXAIUsage reads the same billing endpoint as the Grok CLI's /usage:
// the current period's credits, plus on-demand spending against its cap.
func fetchMagpieXAIUsage(ctx context.Context, client *http.Client, token string) (auth.QuotaState, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xaiauth.CLIChatProxyBaseURL+"/billing?format=credits", nil)
	if err != nil {
		return auth.QuotaState{}, "Failed to build quota query"
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
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
	type amount struct {
		Val any `json:"val"`
	}
	var data struct {
		Config struct {
			UsedPercent any `json:"creditUsagePercent"`
			Period      *struct {
				Type any `json:"type"`
				End  any `json:"end"`
			} `json:"currentPeriod"`
			PeriodEnd    any    `json:"billingPeriodEnd"`
			OnDemandCap  amount `json:"onDemandCap"`
			OnDemandUsed amount `json:"onDemandUsed"`
		} `json:"config"`
	}
	if json.Unmarshal(body, &data) != nil {
		return auth.QuotaState{}, "Invalid quota response"
	}
	// Like the plugin, a value that is not a number reads as 0.
	number := func(value any) float64 {
		n, _ := value.(float64)
		return n
	}
	config := data.Config
	name, end := "Allowance", config.PeriodEnd
	if config.Period != nil {
		periodType, _ := config.Period.Type.(string)
		if period, ok := magpieXAIPeriods[strings.TrimPrefix(periodType, "USAGE_PERIOD_TYPE_")]; ok {
			name = period
		}
		end = config.Period.End
	}
	quota := auth.QuotaState{ObservedAt: time.Now().UTC(), Signals: map[string]string{
		"X-XAI-Window-Name":  name,
		"X-XAI-Used-Percent": strconv.FormatFloat(number(config.UsedPercent), 'f', -1, 64),
	}}
	if endText, ok := end.(string); ok {
		if reset, err := time.Parse(time.RFC3339, endText); err == nil {
			quota.Signals["X-XAI-Reset-At"] = reset.UTC().Format(time.RFC3339)
		}
	}
	if limit := number(config.OnDemandCap.Val); limit > 0 {
		quota.Signals["X-XAI-On-Demand-Used-Percent"] = strconv.FormatFloat(100*number(config.OnDemandUsed.Val)/limit, 'f', -1, 64)
	}
	return quota, ""
}

func magpieXAIWindows(quota auth.QuotaState) []magpieQuotaWindow {
	windows := make([]magpieQuotaWindow, 0)
	signals := quota.Signals
	var resetsAt *string
	if reset := signals["X-XAI-Reset-At"]; reset != "" {
		resetsAt = &reset
	}
	for _, window := range []struct{ name, key string }{{signals["X-XAI-Window-Name"], "X-XAI-Used-Percent"}, {"On-demand", "X-XAI-On-Demand-Used-Percent"}} {
		used, err := strconv.ParseFloat(signals[window.key], 64)
		if window.name == "" || err != nil {
			continue
		}
		windows = append(windows, magpieQuotaWindow{Name: window.name, Used: used, ResetsAt: resetsAt})
	}
	return windows
}
