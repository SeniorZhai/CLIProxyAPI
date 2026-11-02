package api

import (
	"encoding/json"
	"maps"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	// A reading this recent was just made. Magpie treats asOf as a cached
	// reading: it draws the yellow "as of" mark and does not keep the point.
	magpieQuotaFreshFor = 90 * time.Second
	magpieHistKept      = 45 * 24 * time.Hour
	magpieHistMax       = 6000
	magpieHistGap       = 5 * time.Minute
	magpieHistSame      = 30 * time.Second
	magpieHistSlack     = 5 * time.Minute
)

// magpieQuotaPoint is one reading of a window: at At, Left percent remained.
// The window began at Start and starts again at ResetsAt, when those are known.
type magpieQuotaPoint struct {
	At       time.Time  `json:"at"`
	Left     float64    `json:"left"`
	Start    *time.Time `json:"start,omitempty"`
	ResetsAt *time.Time `json:"resetsAt,omitempty"`
}

// magpieQuotaLine is one window's points, oldest first.
type magpieQuotaLine struct {
	Name   string             `json:"name"`
	Points []magpieQuotaPoint `json:"points"`
}

// magpieQuotaHistory is one account's windows over time. User is lowercased,
// which is how Magpie matches a history to a card.
type magpieQuotaHistory struct {
	Provider string            `json:"provider"`
	User     string            `json:"user"`
	Lines    []magpieQuotaLine `json:"lines"`
}

// magpieHistFile is the on-disk history: "provider|user" → window → points.
// The figures only. No token and nothing else from the auth file.
type magpieHistFile map[string]map[string][]magpieQuotaPoint

type magpieQuotaSample struct {
	Card magpieQuotaCard
	At   time.Time
}

var magpieHistMu sync.Mutex

var magpieSpanWords = regexp.MustCompile(`(?i)(\d+)\s*-?\s*(h|hr|hour|d|day|w|wk|week|month)s?\b`)

// magpieQuotaIcon is the icon id Magpie draws on a quota card. Codex's mark
// is codex-color; the provider id "codex" is not in that set. xAI's is xai.
func magpieQuotaIcon(provider string) string {
	if provider == "codex" {
		return "codex-color"
	}
	return provider
}

// magpieQuotaAsOf is the card's asOf. A reading just made leaves it nil so
// Magpie does not mark the card as a cached "as of" snapshot.
func magpieQuotaAsOf(quota auth.QuotaState) *string {
	observed, ok := quotaObservedAt(quota)
	if !ok {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, observed)
	if err == nil && time.Since(parsed) <= magpieQuotaFreshFor {
		return nil
	}
	return &observed
}

func magpieQuotaSampleAt(quota auth.QuotaState, readAt string) time.Time {
	if observed, ok := quotaObservedAt(quota); ok {
		if parsed, err := time.Parse(time.RFC3339, observed); err == nil {
			return parsed
		}
	}
	if parsed, err := time.Parse(time.RFC3339, readAt); err == nil {
		return parsed
	}
	return time.Now().UTC()
}

func (s *Server) magpieQuotaHistoryPath() string {
	return filepath.Join(filepath.Dir(s.configFilePath), "magpie-quota-history.json")
}

// magpieQuotaHistory serves GET /v1/magpie/quotas/history from disk.
// It does not ask Codex or xAI. Magpie draws a remote card's curve from this.
func (s *Server) magpieQuotaHistory(c *gin.Context) {
	now := time.Now().UTC()
	data := s.magpieQuotaHistories(magpieHistorySince(c.Query("days"), now), c.Query("provider"), c.Query("user"))
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"data": data})
}

func magpieWindowSpan(name string) time.Duration {
	unit := map[string]time.Duration{
		"h": time.Hour, "hr": time.Hour, "hour": time.Hour,
		"d": 24 * time.Hour, "day": 24 * time.Hour,
		"w": 7 * 24 * time.Hour, "wk": 7 * 24 * time.Hour, "week": 7 * 24 * time.Hour,
		"month": 30 * 24 * time.Hour,
	}
	if m := magpieSpanWords.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return time.Duration(n) * unit[strings.ToLower(m[2])]
		}
	}
	switch n := strings.ToLower(name); {
	case strings.Contains(n, "daily"):
		return 24 * time.Hour
	case strings.Contains(n, "weekly"):
		return 7 * 24 * time.Hour
	case strings.Contains(n, "monthly"):
		return 30 * 24 * time.Hour
	}
	return 0
}

func magpieNewCycle(a, b magpieQuotaPoint) bool {
	if a.ResetsAt == nil && b.ResetsAt == nil {
		return b.Left > a.Left+5
	}
	if a.ResetsAt == nil || b.ResetsAt == nil {
		return true
	}
	if !a.ResetsAt.After(b.At) {
		return true
	}
	moved := b.ResetsAt.Sub(*a.ResetsAt)
	rolling := moved > 0 && (moved-b.At.Sub(a.At)).Abs() <= magpieHistSlack
	if !rolling && moved.Abs() <= magpieHistSlack && b.Left > a.Left+5 {
		return true
	}
	return moved > b.At.Sub(a.At)+magpieHistSlack || moved < -magpieHistSlack
}

func magpiePoint(w magpieQuotaWindow, at time.Time) (magpieQuotaPoint, bool) {
	if w.Name == "" || math.IsNaN(w.Used) || math.IsInf(w.Used, 0) {
		return magpieQuotaPoint{}, false
	}
	p := magpieQuotaPoint{
		At:   at.UTC().Truncate(time.Second),
		Left: math.Round(min(100, max(0, 100-w.Used))*100) / 100,
	}
	if w.ResetsAt != nil {
		if reset, err := time.Parse(time.RFC3339, *w.ResetsAt); err == nil {
			r := reset.UTC().Truncate(time.Second)
			p.ResetsAt = &r
			if span := magpieWindowSpan(w.Name); span > 0 {
				start := r.Add(-span)
				p.Start = &start
			}
		}
	}
	return p, true
}

func magpieAddPoint(pts []magpieQuotaPoint, n magpieQuotaPoint) []magpieQuotaPoint {
	k := len(pts)
	if k > 0 && !n.At.After(pts[k-1].At) {
		return pts
	}
	if k >= 2 {
		p1, p2 := pts[k-2], pts[k-1]
		if !magpieNewCycle(p1, p2) && !magpieNewCycle(p2, n) {
			flat := math.Abs(n.Left-p2.Left) < 0.05 && math.Abs(p2.Left-p1.Left) < 0.05
			if flat || n.At.Sub(p1.At) < magpieHistGap {
				pts[k-1] = n
				return pts
			}
		}
	}
	if k >= 1 && n.At.Sub(pts[k-1].At) < magpieHistSame && !magpieNewCycle(pts[k-1], n) {
		pts[k-1] = n
		return pts
	}
	return append(pts, n)
}

func (h magpieHistFile) prune(now time.Time) {
	cut := now.Add(-magpieHistKept)
	for key, windows := range h {
		for name, pts := range windows {
			i, _ := slices.BinarySearchFunc(pts, cut, func(p magpieQuotaPoint, t time.Time) int { return p.At.Compare(t) })
			pts = pts[i:]
			if len(pts) > magpieHistMax {
				pts = pts[len(pts)-magpieHistMax:]
			}
			if len(pts) == 0 {
				delete(windows, name)
			} else {
				windows[name] = slices.Clip(pts)
			}
		}
		if len(windows) == 0 {
			delete(h, key)
		}
	}
}

func magpieSamePoint(x, y magpieQuotaPoint) bool {
	eq := func(a, b *time.Time) bool {
		return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b))
	}
	return x.At.Equal(y.At) && x.Left == y.Left && eq(x.Start, y.Start) && eq(x.ResetsAt, y.ResetsAt)
}

func magpieHistKey(provider, user string) string {
	return provider + "|" + strings.ToLower(strings.TrimSpace(user))
}

func readMagpieHistFile(path string) (magpieHistFile, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return magpieHistFile{}, true
		}
		return nil, false
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return magpieHistFile{}, true
	}
	h := magpieHistFile{}
	if json.Unmarshal(b, &h) != nil || h == nil {
		return nil, false
	}
	return h, true
}

func writeMagpieHistFile(path string, h magpieHistFile) error {
	if h == nil {
		h = magpieHistFile{}
	}
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".magpie-quota-history-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// noteMagpieQuotaHistory keeps a point for each window on a card that was
// read. A failed card is not a point. The history file sits beside the config.
func (s *Server) noteMagpieQuotaHistory(samples []magpieQuotaSample) {
	if len(samples) == 0 || strings.TrimSpace(s.configFilePath) == "" {
		return
	}
	magpieHistMu.Lock()
	defer magpieHistMu.Unlock()
	path := s.magpieQuotaHistoryPath()
	h, ok := readMagpieHistFile(path)
	if !ok {
		return
	}
	changed := false
	now := time.Now().UTC()
	for _, sample := range samples {
		card := sample.Card
		if card.Error != "" || card.Provider == "" || len(card.Windows) == 0 {
			continue
		}
		user := ""
		if card.User != nil {
			user = *card.User
		}
		for _, window := range card.Windows {
			point, good := magpiePoint(window, sample.At)
			if !good {
				continue
			}
			key := magpieHistKey(card.Provider, user)
			if h[key] == nil {
				h[key] = map[string][]magpieQuotaPoint{}
			}
			before := h[key][window.Name]
			k := len(before)
			var last magpieQuotaPoint
			if k > 0 {
				last = before[k-1]
			}
			after := magpieAddPoint(before, point)
			if len(after) != k || (k > 0 && !magpieSamePoint(after[k-1], last)) {
				changed = true
			}
			h[key][window.Name] = after
		}
	}
	if !changed {
		return
	}
	h.prune(now)
	_ = writeMagpieHistFile(path, h)
}

func magpieHistorySince(days string, now time.Time) time.Time {
	n, err := strconv.ParseFloat(strings.TrimSpace(days), 64)
	if err != nil || n <= 0 || n*24*float64(time.Hour) > float64(magpieHistKept) {
		return now.Add(-magpieHistKept)
	}
	return now.Add(-time.Duration(n * 24 * float64(time.Hour)))
}

func (s *Server) magpieQuotaHistories(since time.Time, provider, user string) []magpieQuotaHistory {
	magpieHistMu.Lock()
	defer magpieHistMu.Unlock()
	h, ok := readMagpieHistFile(s.magpieQuotaHistoryPath())
	if !ok || len(h) == 0 {
		return []magpieQuotaHistory{}
	}
	provider = strings.TrimSpace(provider)
	user = strings.ToLower(strings.TrimSpace(user))
	out := make([]magpieQuotaHistory, 0)
	for _, key := range slices.Sorted(maps.Keys(h)) {
		p, u, _ := strings.Cut(key, "|")
		if (provider != "" && p != provider) || (user != "" && u != user) {
			continue
		}
		account := magpieQuotaHistory{Provider: p, User: u, Lines: []magpieQuotaLine{}}
		windows := h[key]
		for _, name := range slices.Sorted(maps.Keys(windows)) {
			pts := windows[name]
			i, _ := slices.BinarySearchFunc(pts, since, func(p magpieQuotaPoint, t time.Time) int { return p.At.Compare(t) })
			if i > 0 {
				i--
			}
			if i < len(pts) {
				account.Lines = append(account.Lines, magpieQuotaLine{Name: name, Points: slices.Clone(pts[i:])})
			}
		}
		if len(account.Lines) > 0 {
			out = append(out, account)
		}
	}
	return out
}
