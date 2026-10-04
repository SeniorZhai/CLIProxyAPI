package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"golang.org/x/crypto/bcrypt"
)

const (
	SessionLifetime = 30 * 24 * time.Hour
	cookieName      = "cpa_admin_session"
	maxSessions     = 128
	maxAttempts     = 4096
	lockoutDuration = 30 * time.Minute
)

type session struct {
	Username  string    `json:"username"`
	CSRF      string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
	BaseURL   string    `json:"base_url"`
}

type attempt struct {
	Failures int
	Until    time.Time
}

type Manager struct {
	mu       sync.Mutex
	record   account
	dir      string
	origin   string
	secure   bool
	sessions map[[32]byte]session
	attempts map[string]attempt
	now      func() time.Time
}

func Open(cfg config.AdminConfig, configPath string) (*Manager, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("administrator console is disabled")
	}
	origin, errValidate := cfg.Origin()
	if errValidate != nil {
		return nil, errValidate
	}
	dir := StateDir(cfg, configPath)
	record, errLoad := loadAccount(dir)
	if errLoad != nil {
		return nil, errLoad
	}
	return &Manager{
		record: record, dir: dir, origin: origin, secure: strings.HasPrefix(origin, "https://"),
		sessions: make(map[[32]byte]session), attempts: make(map[string]attempt), now: time.Now,
	}, nil
}

func (m *Manager) Login(c *gin.Context) {
	if !m.checkOrigin(c) {
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(c, &input) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.pruneLocked(now)
	clientIP := c.ClientIP()
	previous := m.attempts[clientIP]
	if previous.Failures >= 5 || (len(m.attempts) >= maxAttempts && previous.Failures == 0) {
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "login_throttled"})
		return
	}
	errPassword := bcrypt.CompareHashAndPassword([]byte(m.record.PasswordHash), []byte(input.Password))
	if errPassword != nil || subtle.ConstantTimeCompare([]byte(input.Username), []byte(m.record.Username)) != 1 {
		m.attempts[clientIP] = attempt{Failures: previous.Failures + 1, Until: now.Add(lockoutDuration)}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_credentials"})
		return
	}
	delete(m.attempts, clientIP)
	if len(m.sessions) >= maxSessions {
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too_many_sessions"})
		return
	}
	token, errToken := randomToken(32)
	csrf, errCSRF := randomToken(32)
	if errToken != nil || errCSRF != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "session_failed"})
		return
	}
	if errRemove := os.Remove(filepath.Join(m.dir, initialCredentialsFile)); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "state_write_failed"})
		return
	}
	if old, errCookie := c.Cookie(cookieName); errCookie == nil {
		delete(m.sessions, sha256.Sum256([]byte(old)))
	}
	current := session{Username: m.record.Username, CSRF: csrf, ExpiresAt: now.Add(SessionLifetime), BaseURL: m.origin}
	m.sessions[sha256.Sum256([]byte(token))] = current
	m.setCookie(c, token, current.ExpiresAt, int(SessionLifetime.Seconds()))
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, current)
}

func (m *Manager) Session(c *gin.Context) {
	current, ok := m.authenticate(c)
	if !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, current)
}

func (m *Manager) Authorize(c *gin.Context) bool {
	current, ok := m.authenticate(c)
	if !ok {
		return false
	}
	unsafe := c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead
	if unsafe || strings.HasSuffix(c.Request.URL.Path, "/oauth/auth-url") {
		if unsafe && !m.checkOrigin(c) {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(c.GetHeader("X-CSRF-Token")), []byte(current.CSRF)) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "invalid_csrf"})
			return false
		}
	}
	c.Header("Cache-Control", "no-store")
	return true
}

func (m *Manager) Logout(c *gin.Context) {
	if !m.Authorize(c) {
		return
	}
	token, _ := c.Cookie(cookieName)
	m.mu.Lock()
	delete(m.sessions, sha256.Sum256([]byte(token)))
	m.mu.Unlock()
	m.setCookie(c, "", time.Unix(1, 0), -1)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (m *Manager) ChangePassword(c *gin.Context) {
	if !m.Authorize(c) {
		return
	}
	var input struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !readJSON(c, &input) {
		return
	}
	if errValidate := validatePassword(input.New); errValidate != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_password", "message": errValidate.Error()})
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if errPassword := bcrypt.CompareHashAndPassword([]byte(m.record.PasswordHash), []byte(input.Current)); errPassword != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_credentials"})
		return
	}
	hash, errHash := bcrypt.GenerateFromPassword([]byte(input.New), bcrypt.DefaultCost)
	if errHash != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "state_write_failed"})
		return
	}
	next := account{Username: m.record.Username, PasswordHash: string(hash)}
	data, errJSON := json.Marshal(next)
	if errJSON != nil || writePrivateFile(filepath.Join(m.dir, "account.json"), data, true) != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "state_write_failed"})
		return
	}
	m.record = next
	clear(m.sessions)
	m.setCookie(c, "", time.Unix(1, 0), -1)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (m *Manager) authenticate(c *gin.Context) (session, bool) {
	token, errCookie := c.Cookie(cookieName)
	if errCookie != nil || len(token) != 43 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "session_expired"})
		return session{}, false
	}
	id := sha256.Sum256([]byte(token))
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.sessions[id]
	if !ok || !m.now().Before(current.ExpiresAt) {
		delete(m.sessions, id)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "session_expired"})
		return session{}, false
	}
	return current, true
}

func (m *Manager) checkOrigin(c *gin.Context) bool {
	if c.GetHeader("Origin") != m.origin {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "invalid_origin"})
		return false
	}
	return true
}

func (m *Manager) setCookie(c *gin.Context, value string, expires time.Time, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: cookieName, Value: value, Path: "/v8/management", HttpOnly: true, Secure: m.secure,
		SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge,
	})
}

func (m *Manager) pruneLocked(now time.Time) {
	for key, current := range m.sessions {
		if !now.Before(current.ExpiresAt) {
			delete(m.sessions, key)
		}
	}
	for key, current := range m.attempts {
		if !now.Before(current.Until) {
			delete(m.attempts, key)
		}
	}
}

func readJSON(c *gin.Context, target any) bool {
	c.Header("Cache-Control", "no-store")
	if c.Request.Body == nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(target); errDecode != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return false
	}
	if errTrailing := decoder.Decode(new(any)); errTrailing != io.EOF {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return false
	}
	return true
}
