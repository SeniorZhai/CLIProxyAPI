package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type AdminConfig struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	StateDir  string `yaml:"state-dir,omitempty" json:"state-dir,omitempty"`
	PublicURL string `yaml:"public-url,omitempty" json:"public-url,omitempty"`
}

func (a AdminConfig) Validate() error {
	if !a.Enabled {
		return nil
	}
	_, err := a.Origin()
	return err
}

func (a AdminConfig) Origin() (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(a.PublicURL), "/"))
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("management.admin.public-url must be an origin such as https://proxy.example.com")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if port := u.Port(); port != "" {
		number, errPort := strconv.Atoi(port)
		if errPort != nil || number < 1 || number > 65535 {
			return "", fmt.Errorf("management.admin.public-url has an invalid port")
		}
		if (u.Scheme == "https" && number == 443) || (u.Scheme == "http" && number == 80) {
			u.Host = strings.TrimSuffix(u.Host, ":"+port)
		}
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return u.String(), nil
	}
	return "", fmt.Errorf("management.admin.public-url requires HTTPS except on localhost")
}
