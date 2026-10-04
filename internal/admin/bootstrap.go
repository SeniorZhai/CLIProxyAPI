package admin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

type BootstrapOptions struct {
	PublicURL      string
	Username       string
	Password       string
	TrustedProxies []string
}

func Bootstrap(configPath string, options BootstrapOptions) (string, error) {
	configPath, errAbs := filepath.Abs(configPath)
	if errAbs != nil {
		return "", fmt.Errorf("resolve configuration path: %w", errAbs)
	}
	if _, errStat := os.Stat(configPath); errors.Is(errStat, os.ErrNotExist) {
		if options.PublicURL == "" {
			options.PublicURL = "http://localhost:8318"
		}
		adminCfg := config.AdminConfig{Enabled: true, StateDir: "admin", PublicURL: options.PublicURL}
		if errValidate := adminCfg.Validate(); errValidate != nil {
			return "", errValidate
		}
		dir := filepath.Dir(configPath)
		if errMkdir := os.MkdirAll(dir, 0700); errMkdir != nil {
			return "", fmt.Errorf("create configuration directory: %w", errMkdir)
		}
		data, errYAML := yaml.Marshal(map[string]any{
			"config-version": 8,
			"server":         map[string]any{"host": "", "port": 8317, "trusted-proxies": options.TrustedProxies},
			"management":     map[string]any{"admin": adminCfg, "allow-remote": true, "disable-control-panel": true},
			"access":         map[string]any{"api-keys": []string{}},
			"oauth":          map[string]any{"auth-dir": filepath.Join(dir, "auths")},
		})
		if errYAML != nil {
			return "", fmt.Errorf("encode initial configuration: %w", errYAML)
		}
		if _, errValidate := config.ParseConfigBytes(data); errValidate != nil {
			return "", fmt.Errorf("validate initial configuration: %w", errValidate)
		}
		if errWrite := writePrivateFile(configPath, data, false); errWrite != nil {
			return "", errWrite
		}
	} else if errStat != nil {
		return "", fmt.Errorf("inspect configuration: %w", errStat)
	}
	cfg, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		return "", errLoad
	}
	return Initialize(cfg.RemoteManagement.Admin, configPath, options.Username, options.Password)
}
