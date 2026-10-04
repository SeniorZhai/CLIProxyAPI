package admin

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

const initialCredentialsFile = "initial-credentials.txt"

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.@-]{1,64}$`)

type account struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

func StateDir(cfg config.AdminConfig, configPath string) string {
	dir := strings.TrimSpace(cfg.StateDir)
	if dir == "" {
		dir = "admin"
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(configPath), dir)
	}
	return dir
}

func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func validatePassword(password string) error {
	if len(password) < 12 || len(password) > 72 || strings.ContainsAny(password, "\r\n") {
		return fmt.Errorf("password must contain 12 to 72 bytes and no line breaks")
	}
	return nil
}

func loadAccount(dir string) (account, error) {
	var result account
	data, errRead := os.ReadFile(filepath.Join(dir, "account.json"))
	if errRead != nil {
		return result, fmt.Errorf("read administrator account: %w", errRead)
	}
	if errJSON := json.Unmarshal(data, &result); errJSON != nil || !usernamePattern.MatchString(result.Username) {
		return result, fmt.Errorf("invalid administrator account file")
	}
	if _, errHash := bcrypt.Cost([]byte(result.PasswordHash)); errHash != nil {
		return result, fmt.Errorf("invalid administrator password hash")
	}
	return result, nil
}

func Initialize(cfg config.AdminConfig, configPath, username, password string) (string, error) {
	if errValidate := cfg.Validate(); errValidate != nil {
		return "", errValidate
	}
	if !cfg.Enabled {
		return "", fmt.Errorf("management.admin.enabled must be true")
	}
	dir := StateDir(cfg, configPath)
	if _, errLoad := loadAccount(dir); errLoad == nil {
		return "", nil
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		return "", errLoad
	}
	username = strings.TrimSpace(username)
	if username == "" {
		username = "admin"
	}
	if !usernamePattern.MatchString(username) {
		return "", fmt.Errorf("administrator username must use 1 to 64 letters, digits, or . _ @ -")
	}
	if password == "" {
		var errRandom error
		password, errRandom = randomToken(24)
		if errRandom != nil {
			return "", errRandom
		}
	}
	if errValidate := validatePassword(password); errValidate != nil {
		return "", errValidate
	}
	hash, errHash := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if errHash != nil {
		return "", fmt.Errorf("hash administrator password: %w", errHash)
	}
	if errMkdir := os.MkdirAll(dir, 0700); errMkdir != nil {
		return "", fmt.Errorf("create administrator state directory: %w", errMkdir)
	}
	record := account{Username: username, PasswordHash: string(hash)}
	data, errJSON := json.Marshal(record)
	if errJSON != nil {
		return "", errJSON
	}
	if errWrite := writePrivateFile(filepath.Join(dir, "account.json"), data, false); errWrite != nil {
		return "", errWrite
	}
	path := filepath.Join(dir, initialCredentialsFile)
	if errWrite := writePrivateFile(path, []byte("username: "+username+"\npassword: "+password+"\n"), true); errWrite != nil {
		return "", fmt.Errorf("write initial credentials (use --reset-admin-password to recover): %w", errWrite)
	}
	return path, nil
}

func ResetPassword(cfg config.AdminConfig, configPath string) (string, error) {
	if !cfg.Enabled {
		return "", fmt.Errorf("administrator console is disabled")
	}
	dir := StateDir(cfg, configPath)
	record, errLoad := loadAccount(dir)
	if errLoad != nil {
		return "", errLoad
	}
	password, errRandom := randomToken(24)
	if errRandom != nil {
		return "", errRandom
	}
	hash, errHash := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if errHash != nil {
		return "", fmt.Errorf("hash administrator password: %w", errHash)
	}
	record.PasswordHash = string(hash)
	data, errJSON := json.Marshal(record)
	if errJSON != nil {
		return "", errJSON
	}
	if errWrite := writePrivateFile(filepath.Join(dir, "account.json"), data, true); errWrite != nil {
		return "", errWrite
	}
	path := filepath.Join(dir, initialCredentialsFile)
	if errWrite := writePrivateFile(path, []byte("username: "+record.Username+"\npassword: "+password+"\n"), true); errWrite != nil {
		return "", errWrite
	}
	return path, nil
}

func writePrivateFile(path string, data []byte, replace bool) error {
	f, errCreate := os.CreateTemp(filepath.Dir(path), ".admin-*")
	if errCreate != nil {
		return fmt.Errorf("create private file: %w", errCreate)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil && !errors.Is(errClose, os.ErrClosed) {
			log.WithError(errClose).Warn("close administrator state file")
		}
		if errRemove := os.Remove(f.Name()); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.WithError(errRemove).Warn("remove temporary administrator state file")
		}
	}()
	if _, errWrite := f.Write(data); errWrite != nil {
		return fmt.Errorf("write private file: %w", errWrite)
	}
	if errSync := f.Sync(); errSync != nil {
		return fmt.Errorf("sync private file: %w", errSync)
	}
	if errClose := f.Close(); errClose != nil {
		return fmt.Errorf("close private file: %w", errClose)
	}
	if replace {
		if errRename := os.Rename(f.Name(), path); errRename != nil {
			return fmt.Errorf("replace private file: %w", errRename)
		}
	} else if errLink := os.Link(f.Name(), path); errLink != nil {
		return fmt.Errorf("create private file without overwriting existing data: %w", errLink)
	}
	return nil
}
