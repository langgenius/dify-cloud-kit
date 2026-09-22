package huaweiobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestOIDCProvider(t *testing.T, server *httptest.Server) *oidcCredentialsProvider {
	t.Helper()

	tokenFile := filepath.Join(t.TempDir(), "oidc-token")
	if err := os.WriteFile(tokenFile, []byte("  test-id-token  "), 0600); err != nil {
		t.Fatalf("write OIDC token file: %v", err)
	}

	provider, err := newOIDCCredentialsProvider("test-idp", tokenFile)
	if err != nil {
		t.Fatalf("newOIDCCredentialsProvider() error = %v", err)
	}
	provider.iamEndpoint = server.URL
	provider.httpClient = server.Client()
	return provider
}

func TestOIDCCredentialsProviderExchangesIDToken(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost {
			t.Errorf("request method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json;charset=utf8" {
			t.Errorf("Content-Type = %q", got)
		}

		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}

		switch r.URL.Path {
		case "/v3.0/OS-AUTH/id-token/tokens":
			if got := r.Header.Get("X-Idp-Id"); got != "test-idp" {
				t.Errorf("X-Idp-Id = %q, want test-idp", got)
			}
			auth, ok := request["auth"].(map[string]any)
			if !ok {
				t.Fatalf("auth is missing")
			}
			idToken, ok := auth["id_token"].(map[string]any)
			if !ok {
				t.Fatalf("id_token is missing")
			}
			if got := idToken["id"]; got != "test-id-token" {
				t.Errorf("id_token.id = %v, want test-id-token", got)
			}
			w.Header().Set("X-Subject-Token", "test-federation-token")
			w.WriteHeader(http.StatusCreated)
		case "/v3.0/OS-CREDENTIAL/securitytokens":
			if got := r.Header.Get("X-Auth-Token"); got != "test-federation-token" {
				t.Errorf("X-Auth-Token = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(w).Encode(map[string]any{
				"credential": map[string]string{
					"access":        "test-access",
					"secret":        "test-secret",
					"securitytoken": "test-security-token",
					"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339Nano),
				},
			}); err != nil {
				t.Errorf("encode response: %v", err)
			}
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newTestOIDCProvider(t, server)
	credentials, err := provider.currentCredentials()
	if err != nil {
		t.Fatalf("credentials() error = %v", err)
	}
	if credentials.accessKey != "test-access" || credentials.secretKey != "test-secret" || credentials.securityToken != "test-security-token" {
		t.Fatalf("credentials = %+v, want temporary credentials", credentials)
	}
	if credentials.expiresAt.Before(provider.nowFunc()) {
		t.Fatalf("expiresAt = %v, want a future time", credentials.expiresAt)
	}

	cached, err := provider.currentCredentials()
	if err != nil {
		t.Fatalf("cached credentials() error = %v", err)
	}
	if cached.accessKey != credentials.accessKey ||
		cached.secretKey != credentials.secretKey ||
		cached.securityToken != credentials.securityToken {
		t.Fatalf("cached credentials = %+v, want %+v", cached, credentials)
	}
	if calls != 2 {
		t.Fatalf("IAM calls = %d, want 2", calls)
	}
}

func TestOIDCCredentialsProviderRefreshesBeforeExpiration(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v3.0/OS-AUTH/id-token/tokens" {
			w.Header().Set("X-Subject-Token", "test-federation-token")
			w.WriteHeader(http.StatusCreated)
			return
		}
		if r.URL.Path != "/v3.0/OS-CREDENTIAL/securitytokens" {
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"credential": map[string]string{
				"access":        "new-access",
				"secret":        "new-secret",
				"securitytoken": "new-security-token",
				"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339Nano),
			},
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	provider := newTestOIDCProvider(t, server)
	provider.credentials = oidcCredentials{
		accessKey:     "old-access",
		secretKey:     "old-secret",
		securityToken: "old-security-token",
		expiresAt:     provider.nowFunc().Add(refreshBefore - time.Minute),
	}

	credentials, err := provider.currentCredentials()
	if err != nil {
		t.Fatalf("credentials() error = %v", err)
	}
	if credentials.accessKey != "new-access" {
		t.Fatalf("accessKey = %q, want new-access", credentials.accessKey)
	}
	if calls != 2 {
		t.Fatalf("IAM calls = %d, want 2", calls)
	}
}

func TestOIDCCredentialsProviderDoesNotRetryClientErrors(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `{"error_code":"IAM.0002","error_msg":"invalid token"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	provider := newTestOIDCProvider(t, server)
	_, err := provider.currentCredentials()
	if err == nil {
		t.Fatal("credentials() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "IAM.0002") {
		t.Fatalf("error = %v, want IAM error detail", err)
	}
	if calls != 1 {
		t.Fatalf("IAM calls = %d, want 1", calls)
	}
}

func TestOIDCCredentialsProviderRequiresIDToken(t *testing.T) {
	missingTokenFile := filepath.Join(t.TempDir(), "missing-oidc-token")
	provider, err := newOIDCCredentialsProvider("test-idp", missingTokenFile)
	if err != nil {
		t.Fatalf("newOIDCCredentialsProvider() error = %v", err)
	}
	provider.iamEndpoint = "https://iam.example.com"

	if _, err := provider.currentCredentials(); err == nil {
		t.Fatal("credentials() error = nil, want missing token error")
	}
}

func TestNewOIDCCredentialsProviderRequiresIdpID(t *testing.T) {
	if _, err := newOIDCCredentialsProvider("", "/tmp/oidc-token"); err == nil {
		t.Fatalf("newOIDCCredentialsProvider() error = %v, want idp error", err)
	}
}
