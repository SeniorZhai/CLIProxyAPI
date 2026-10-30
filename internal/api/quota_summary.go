package api

import (
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type quotaSummaryWindow struct {
	Name             string   `json:"name"`
	UsedPercent      *float64 `json:"used_percent"`
	RemainingPercent *float64 `json:"remaining_percent"`
	WindowMinutes    int64    `json:"window_minutes"`
	ResetAt          *string  `json:"reset_at"`
}

type quotaSummaryAccount struct {
	ObservedAt              *string              `json:"observed_at"`
	PrimaryRemainingPercent *float64             `json:"primary_remaining_percent"`
	Windows                 []quotaSummaryWindow `json:"windows"`
}

func (s *Server) quotaSummary(c *gin.Context) {
	accounts := make([]quotaSummaryAccount, 0)
	snapshots := s.handlers.AuthManager.List()
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].ID < snapshots[j].ID })
	for _, snapshot := range snapshots {
		if snapshot.Provider != "codex" || snapshot.Disabled {
			continue
		}
		switch snapshot.Status {
		case auth.StatusActive, auth.StatusError, auth.StatusRefreshing:
		default:
			continue
		}
		account := quotaSummaryAccount{Windows: codexQuotaWindows(snapshot.Quota, "X-Codex-")}
		if observed, ok := quotaObservedAt(snapshot.Quota); ok {
			account.ObservedAt = &observed
		}
		for _, window := range account.Windows {
			if window.Name == "primary" {
				account.PrimaryRemainingPercent = window.RemainingPercent
			}
		}
		accounts = append(accounts, account)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"providers": gin.H{"codex": gin.H{"accounts": accounts}}})
}

func quotaObservedAt(quota auth.QuotaState) (string, bool) {
	observed := quota.ObservedAt
	if observed.IsZero() || observed.Year() < 1 || observed.Year() > 9999 {
		return "", false
	}
	return observed.UTC().Format(time.RFC3339), true
}

func codexQuotaWindows(quota auth.QuotaState, group string) []quotaSummaryWindow {
	windows := make([]quotaSummaryWindow, 0)
	if _, ok := quotaObservedAt(quota); !ok {
		return windows
	}
	observed := quota.ObservedAt
	for _, name := range []string{"primary", "secondary"} {
		prefix := group + strings.ToUpper(name[:1]) + name[1:] + "-"
		signals := quota.Signals
		minutes, err := strconv.ParseInt(strings.TrimSpace(signals[prefix+"Window-Minutes"]), 10, 64)
		if err != nil || minutes <= 0 {
			continue
		}
		window := quotaSummaryWindow{Name: name, WindowMinutes: minutes}
		used, err := strconv.ParseFloat(strings.TrimSpace(signals[prefix+"Used-Percent"]), 64)
		if err == nil && !math.IsNaN(used) && !math.IsInf(used, 0) && used >= 0 && used <= 100 {
			remaining := 100 - used
			window.UsedPercent = &used
			window.RemainingPercent = &remaining
		}
		const maxResetUnix = int64(253402300799)
		resetRaw := strings.TrimSpace(signals[prefix+"Reset-At"])
		reset, err := strconv.ParseInt(resetRaw, 10, 64)
		if resetRaw == "" {
			after, afterErr := strconv.ParseInt(strings.TrimSpace(signals[prefix+"Reset-After-Seconds"]), 10, 64)
			if afterErr == nil && after >= 0 && observed.Unix() > 0 && after <= maxResetUnix-observed.Unix() {
				reset, err = observed.Unix()+after, nil
			}
		}
		if err == nil && reset > 0 && reset <= maxResetUnix {
			formatted := time.Unix(reset, 0).UTC().Format(time.RFC3339)
			window.ResetAt = &formatted
		}
		windows = append(windows, window)
	}
	return windows
}

type magpieQuotaWindow struct {
	Name     string  `json:"name"`
	Used     float64 `json:"used"`
	ResetsAt *string `json:"resetsAt,omitempty"`
}

type magpieQuotaCard struct {
	Provider string              `json:"provider"`
	Name     string              `json:"name"`
	Icon     string              `json:"icon"`
	Kind     string              `json:"kind"`
	Plan     *string             `json:"plan"`
	User     *string             `json:"user"`
	Windows  []magpieQuotaWindow `json:"windows"`
	ReadAt   string              `json:"readAt"`
	AsOf     *string             `json:"asOf"`
	Error    string              `json:"error,omitempty"`
}

// magpieQuotaCards combines passive observations with cached read-only usage probes.
func (s *Server) magpieQuotaCards(c *gin.Context) {
	displayNames := map[string]string{"codex": "Codex", "xai": "xAI"}
	readAt := time.Now().UTC().Format(time.RFC3339)
	cards := make([]magpieQuotaCard, 0)
	snapshots := s.handlers.AuthManager.List()
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].Provider != snapshots[j].Provider {
			return snapshots[i].Provider < snapshots[j].Provider
		}
		return snapshots[i].ID < snapshots[j].ID
	})
	for _, snapshot := range snapshots {
		displayName, ok := displayNames[snapshot.Provider]
		if !ok {
			continue
		}
		var probeError string
		if snapshot.Provider == "codex" && !snapshot.Disabled && snapshot.Status != auth.StatusDisabled {
			snapshot.Quota, probeError = s.magpieCodexUsage(c.Request.Context(), snapshot, c.Request.Method == http.MethodPost)
		}
		card := magpieQuotaCard{
			Provider: snapshot.Provider,
			Name:     displayName,
			Icon:     snapshot.Provider,
			Kind:     "subscription",
			Windows:  make([]magpieQuotaWindow, 0),
			ReadAt:   readAt,
		}
		plan := strings.TrimSpace(snapshot.Quota.Signals["X-Codex-Plan-Type"])
		if plan == "" {
			plan = strings.TrimSpace(snapshot.Attributes["plan_type"])
		}
		if plan != "" {
			card.Plan = &plan
		}
		user, _ := snapshot.Metadata["email"].(string)
		if user = strings.TrimSpace(user); user == "" {
			user = strings.TrimSpace(snapshot.Label)
		}
		if user != "" {
			card.User = &user
		}
		if observed, ok := quotaObservedAt(snapshot.Quota); ok {
			card.AsOf = &observed
		}
		if snapshot.Provider == "codex" {
			// Each limit (base, code review, additional) is a signal group
			// sharing the "<group>Primary-*" / "<group>Secondary-*" layout.
			groups := make([]string, 0)
			for key := range snapshot.Quota.Signals {
				for _, suffix := range []string{"Primary-Window-Minutes", "Secondary-Window-Minutes"} {
					if group, ok := strings.CutSuffix(key, suffix); ok && strings.HasPrefix(group, "X-Codex-") && !slices.Contains(groups, group) {
						groups = append(groups, group)
					}
				}
			}
			rank := func(group string) int {
				switch group {
				case "X-Codex-":
					return 0
				case "X-Codex-Code-Review-":
					return 1
				default:
					return 2
				}
			}
			sort.Slice(groups, func(i, j int) bool {
				if rank(groups[i]) != rank(groups[j]) {
					return rank(groups[i]) < rank(groups[j])
				}
				return groups[i] < groups[j]
			})
			for _, group := range groups {
				label := ""
				switch group {
				case "X-Codex-":
				case "X-Codex-Code-Review-":
					label = "Code review "
				default:
					limitName := strings.TrimSpace(snapshot.Quota.Signals[group+"Limit-Name"])
					if limitName == "" {
						limitName = strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(group, "X-Codex-"), "-"), "Additional-")
					}
					label = limitName + " "
				}
				for _, window := range codexQuotaWindows(snapshot.Quota, group) {
					if window.UsedPercent == nil {
						continue
					}
					name := fmt.Sprintf("%dm", window.WindowMinutes)
					if window.WindowMinutes%1440 == 0 {
						name = fmt.Sprintf("%dd", window.WindowMinutes/1440)
					} else if window.WindowMinutes%60 == 0 {
						name = fmt.Sprintf("%dh", window.WindowMinutes/60)
					}
					card.Windows = append(card.Windows, magpieQuotaWindow{Name: label + name, Used: *window.UsedPercent, ResetsAt: window.ResetAt})
				}
			}
		}
		switch {
		case snapshot.Disabled || snapshot.Status == auth.StatusDisabled:
			card.Error = "Account disabled"
		case probeError != "":
			card.Error = probeError
		case len(card.Windows) == 0:
			card.Error = "No quota observation yet"
		}
		cards = append(cards, card)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"data": cards})
}
