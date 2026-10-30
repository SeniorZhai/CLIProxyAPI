package access

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestEmptyProvidersRequireCredentialsOnlyWhenEnabled(t *testing.T) {
	m := NewManager()
	r := httptest.NewRequest("GET", "/v1/models", nil)
	if _, err := m.Authenticate(context.Background(), r); err != nil {
		t.Fatal("legacy empty provider configuration must remain compatible")
	}
	m.SetRequireCredentials(true)
	for _, key := range []string{"", "invalid-key"} {
		r.Header.Set("Authorization", key)
		if _, err := m.Authenticate(context.Background(), r); !IsAuthErrorCode(err, AuthErrorCodeNoCredentials) {
			t.Fatal("empty key list must deny all inference requests")
		}
	}
}
