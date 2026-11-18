package sheets

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const scope = "https://www.googleapis.com/auth/spreadsheets"

// TokenSource hands out OAuth access tokens.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a pre-obtained access token (e.g. from gcloud).
type StaticToken string

func (s StaticToken) Token(context.Context) (string, error) { return string(s), nil }

// ServiceAccount exchanges a signed JWT for access tokens, caching them
// until shortly before expiry. Uses only the standard library.
type ServiceAccount struct {
	Email    string
	TokenURI string
	key      *rsa.PrivateKey
	http     *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func LoadServiceAccount(path string, hc *http.Client) (*ServiceAccount, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("google credentials: %w", err)
	}
	var f struct {
		Type        string `json:"type"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("google credentials: %w", err)
	}
	if f.Type != "service_account" {
		return nil, fmt.Errorf("google credentials: expected a service_account key file, got type %q", f.Type)
	}
	block, _ := pem.Decode([]byte(f.PrivateKey))
	if block == nil {
		return nil, errors.New("google credentials: private_key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("google credentials: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("google credentials: private key is not RSA")
	}
	if f.TokenURI == "" {
		f.TokenURI = "https://oauth2.googleapis.com/token"
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &ServiceAccount{Email: f.ClientEmail, TokenURI: f.TokenURI, key: rk, http: hc}, nil
}

func (s *ServiceAccount) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Until(s.expires) > time.Minute {
		return s.token, nil
	}
	now := time.Now()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss": s.Email, "scope": scope, "aud": s.TokenURI,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	unsigned := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {unsigned + "." + enc.EncodeToString(sig)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("google token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("google token: HTTP %d: %s", resp.StatusCode, body)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("google token: %w", err)
	}
	s.token = tr.AccessToken
	s.expires = now.Add(time.Duration(tr.ExpiresIn) * time.Second)
	return s.token, nil
}
