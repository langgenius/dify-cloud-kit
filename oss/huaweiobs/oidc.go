package huaweiobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/huaweicloud/huaweicloud-sdk-go-obs/obs"
)

const (
	defaultIAMEndpoint    = "https://iam.myhuaweicloud.com"
	defaultOIDCTokenFile  = "/var/run/secrets/tokens/oidc-token"
	credentialExpiresSecs = 86400
	refreshBefore         = 5 * time.Minute
	maxAuthRetries        = 3
)

type oidcCredentials struct {
	accessKey     string
	secretKey     string
	securityToken string
	expiresAt     time.Time
}

type oidcCredentialsProvider struct {
	idpID       string
	tokenFile   string
	iamEndpoint string
	httpClient  *http.Client
	nowFunc     func() time.Time

	lock        sync.Mutex
	credentials oidcCredentials
}

func newOIDCCredentialsProvider(idpID, tokenFile string) (*oidcCredentialsProvider, error) {
	if strings.TrimSpace(idpID) == "" {
		return nil, fmt.Errorf("huawei cloud identity provider id is required")
	}
	return &oidcCredentialsProvider{
		idpID:       strings.TrimSpace(idpID),
		tokenFile:   strings.TrimSpace(tokenFile),
		iamEndpoint: defaultIAMEndpoint,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		nowFunc:     time.Now,
	}, nil
}

func (p *oidcCredentialsProvider) applyTo(client *obs.ObsClient) error {
	credentials, err := p.currentCredentials()
	if err != nil {
		return err
	}
	client.Refresh(credentials.accessKey, credentials.secretKey, credentials.securityToken)
	return nil
}

func (p *oidcCredentialsProvider) currentCredentials() (oidcCredentials, error) {
	p.lock.Lock()
	defer p.lock.Unlock()

	if p.credentialsValid() {
		return p.credentials, nil
	}

	idToken, err := p.readIDToken()
	if err != nil {
		return oidcCredentials{}, err
	}

	federationToken, err := p.getFederationToken(idToken)
	if err != nil {
		return oidcCredentials{}, err
	}

	credentials, err := p.getTemporaryCredentials(federationToken)
	if err != nil {
		return oidcCredentials{}, err
	}
	p.credentials = credentials

	return credentials, nil
}

func (p *oidcCredentialsProvider) credentialsValid() bool {
	if p.credentials.accessKey == "" || p.credentials.secretKey == "" || p.credentials.securityToken == "" {
		return false
	}
	return p.nowFunc().Before(p.credentials.expiresAt.Add(-refreshBefore))
}

func (p *oidcCredentialsProvider) readIDToken() (string, error) {
	tokenFile := p.tokenFile
	if tokenFile == "" {
		tokenFile = defaultOIDCTokenFile
	}

	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", fmt.Errorf("read OIDC token file %q: %w", tokenFile, err)
	}

	idToken := strings.TrimSpace(string(data))
	if idToken == "" {
		return "", fmt.Errorf("OIDC token file %q is empty", tokenFile)
	}
	return idToken, nil
}

type federationTokenRequest struct {
	Auth struct {
		IDToken struct {
			ID string `json:"id"`
		} `json:"id_token"`
	} `json:"auth"`
}

type temporaryCredentialsRequest struct {
	Auth struct {
		Identity struct {
			Methods []string `json:"methods"`
			Token   struct {
				DurationSeconds int `json:"duration_seconds"`
			} `json:"token"`
		} `json:"identity"`
	} `json:"auth"`
}

type temporaryCredentialsResponse struct {
	Credential struct {
		Access        string `json:"access"`
		Secret        string `json:"secret"`
		SecurityToken string `json:"securitytoken"`
		ExpiresAt     string `json:"expires_at"`
	} `json:"credential"`
}

func (p *oidcCredentialsProvider) getFederationToken(idToken string) (string, error) {
	requestBody := federationTokenRequest{}
	requestBody.Auth.IDToken.ID = idToken

	headers := map[string]string{
		"Content-Type": "application/json;charset=utf8",
		"X-Idp-Id":     p.idpID,
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("marshal OIDC federation token request: %w", err)
	}

	response, err := p.doWithRetry(http.MethodPost, p.iamEndpoint+"/v3.0/OS-AUTH/id-token/tokens", headers, body)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	federationToken := response.Header.Get("X-Subject-Token")
	if federationToken == "" {
		return "", fmt.Errorf("IAM response does not contain X-Subject-Token")
	}
	return federationToken, nil
}

func (p *oidcCredentialsProvider) getTemporaryCredentials(federationToken string) (oidcCredentials, error) {
	requestBody := temporaryCredentialsRequest{}
	requestBody.Auth.Identity.Methods = []string{"token"}
	requestBody.Auth.Identity.Token.DurationSeconds = credentialExpiresSecs

	headers := map[string]string{
		"Content-Type": "application/json;charset=utf8",
		"X-Auth-Token": federationToken,
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return oidcCredentials{}, fmt.Errorf("marshal temporary credentials request: %w", err)
	}

	response, err := p.doWithRetry(http.MethodPost, p.iamEndpoint+"/v3.0/OS-CREDENTIAL/securitytokens", headers, body)
	if err != nil {
		return oidcCredentials{}, err
	}
	defer response.Body.Close()

	var output temporaryCredentialsResponse
	if err := json.NewDecoder(response.Body).Decode(&output); err != nil {
		return oidcCredentials{}, fmt.Errorf("decode temporary credentials response: %w", err)
	}

	expiresAt, err := time.Parse(time.RFC3339Nano, output.Credential.ExpiresAt)
	if err != nil {
		return oidcCredentials{}, fmt.Errorf("parse temporary credentials expiration %q: %w", output.Credential.ExpiresAt, err)
	}
	if output.Credential.Access == "" || output.Credential.Secret == "" || output.Credential.SecurityToken == "" {
		return oidcCredentials{}, fmt.Errorf("IAM returned incomplete temporary credentials")
	}

	return oidcCredentials{
		accessKey:     output.Credential.Access,
		secretKey:     output.Credential.Secret,
		securityToken: output.Credential.SecurityToken,
		expiresAt:     expiresAt,
	}, nil
}

type errorResponse struct {
	ErrorCode string `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`
	Error     struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (p *oidcCredentialsProvider) doWithRetry(method, endpoint string, headers map[string]string, body []byte) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < maxAuthRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
		}

		response, err := p.do(method, endpoint, headers, body)
		if err != nil {
			lastErr = err
			continue
		}
		if response.StatusCode < 400 {
			return response, nil
		}

		responseBody, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("read IAM response: %w", readErr)
		} else {
			lastErr = fmt.Errorf("IAM returned HTTP %d: %s", response.StatusCode, iamErrorMessage(responseBody))
		}
		if response.StatusCode < 500 {
			break
		}
	}
	return nil, lastErr
}

func (p *oidcCredentialsProvider) do(method, endpoint string, headers map[string]string, body []byte) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return p.httpClient.Do(request)
}

func iamErrorMessage(data []byte) string {
	var output errorResponse
	if err := json.Unmarshal(data, &output); err != nil {
		message := strings.TrimSpace(string(data))
		if message == "" {
			return "empty response"
		}
		if len(message) > 512 {
			message = message[:512]
		}
		return message
	}
	if output.ErrorCode != "" || output.ErrorMsg != "" {
		return fmt.Sprintf("%s: %s", output.ErrorCode, output.ErrorMsg)
	}
	if output.Error.Code != "" || output.Error.Message != "" {
		return fmt.Sprintf("%s: %s", output.Error.Code, output.Error.Message)
	}
	return "unknown IAM error"
}
