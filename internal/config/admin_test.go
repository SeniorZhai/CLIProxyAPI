package config

import "testing"

func TestAdminOrigin(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://Proxy.Example.com:443/", "https://proxy.example.com"},
		{"https://proxy.example.com:8443", "https://proxy.example.com:8443"},
		{"http://localhost:80", "http://localhost"},
		{"http://127.0.0.1:8318", "http://127.0.0.1:8318"},
		{"http://[::1]:80", "http://[::1]"},
		{"http://proxy.example.com", ""}, {"https://proxy.example.com/subpath", ""},
		{"https://admin:password@proxy.example.com", ""}, {"https://proxy.example.com?", ""},
		{"https://proxy.example.com:99999", ""}, {"", ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			cfg := AdminConfig{Enabled: true, PublicURL: tc.input}
			got, err := cfg.Origin()
			if tc.want == "" {
				if err == nil {
					t.Fatal("invalid origin accepted")
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("origin=%q error=%v", got, err)
			}
		})
	}
}
