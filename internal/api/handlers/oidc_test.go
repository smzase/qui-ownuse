// Copyright (c) 2025-2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package handlers

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/autobrr/qui/internal/auth"
	"github.com/autobrr/qui/internal/domain"
	"github.com/autobrr/qui/internal/models"
	"github.com/autobrr/qui/internal/testutil/testdb"
)

type discoveryDocument struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	UserInfoEndpoint              string   `json:"userinfo_endpoint"`
	JWKSURI                       string   `json:"jwks_uri"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported,omitempty"`
}

// fakeOIDCProvider serves discovery, a JWKS, a token endpoint that returns a
// signed ID token, and a user-info endpoint.
type fakeOIDCProvider struct {
	codeChallenges []string
	// idClaims are added to every ID token, overriding the defaults.
	idClaims   map[string]any
	identities map[string]map[string]any
	// userInfo is the user-info response; nil makes the endpoint fail.
	userInfo map[string]any
}

// fakeOIDCUserInfoError is the body of a failed user-info response. It must
// stay in the log and never reach the browser.
const fakeOIDCUserInfoError = "provider unavailable: internal.example.lan"

var fakeOIDCKey = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
})

func newOIDCDiscoveryServer(t *testing.T, codeChallenges []string) *httptest.Server {
	return startFakeOIDCProvider(t, &fakeOIDCProvider{codeChallenges: codeChallenges})
}

func startFakeOIDCProvider(t *testing.T, p *fakeOIDCProvider) *httptest.Server {
	var srv *httptest.Server
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.MarshalWrite(w, v); err != nil {
			t.Errorf("encode fake OIDC response: %v", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, discoveryDocument{
			Issuer:                        srv.URL,
			AuthorizationEndpoint:         srv.URL + "/authorize",
			TokenEndpoint:                 srv.URL + "/token",
			UserInfoEndpoint:              srv.URL + "/userinfo",
			JWKSURI:                       srv.URL + "/jwks",
			CodeChallengeMethodsSupported: p.codeChallenges,
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := fakeOIDCKey().PublicKey
		writeJSON(w, map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"alg": "RS256",
			"use": "sig",
			"kid": "test",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		claims := map[string]any{
			"iss":                srv.URL,
			"aud":                "client-id",
			"sub":                "user-1",
			"preferred_username": "alice",
			"iat":                time.Now().Unix(),
			"exp":                time.Now().Add(time.Minute).Unix(),
		}
		maps.Copy(claims, p.idClaims)
		if p.identities != nil {
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse fake OIDC token request: %v", err)
				http.Error(w, "invalid token request", http.StatusBadRequest)
				return
			}
			maps.Copy(claims, p.identities[r.Form.Get("code")])
		}
		writeJSON(w, map[string]any{
			"access_token": "access-token",
			"token_type":   "Bearer",
			"expires_in":   60,
			"id_token":     signFakeIDToken(t, claims),
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		if p.userInfo == nil {
			http.Error(w, fakeOIDCUserInfoError, http.StatusInternalServerError)
			return
		}
		writeJSON(w, p.userInfo)
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func signFakeIDToken(t *testing.T, claims map[string]any) string {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test"})
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, fakeOIDCKey(), crypto.SHA256, digest[:])
	require.NoError(t, err)

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func newTestOIDCHandler(t *testing.T, issuer string) *OIDCHandler {
	t.Helper()

	// Test-only OIDC client config values.
	cfg := &domain.Config{
		OIDCEnabled:     true,
		OIDCIssuer:      issuer,
		OIDCClientID:    "client-id",
		OIDCRedirectURL: "http://localhost/callback",
	}
	// Test-only placeholder used to satisfy OIDC config validation.
	cfg.OIDCClientSecret = "placeholder"

	handler, err := NewOIDCHandler(cfg, scs.New(), nil)
	require.NoError(t, err)

	return handler
}

func TestOIDCCallbackPreservesSeparateForkUsers(t *testing.T) {
	provider := startFakeOIDCProvider(t, &fakeOIDCProvider{identities: map[string]map[string]any{
		"alice": {"sub": "subject-alice", "preferred_username": "alice"},
		"bob":   {"sub": "subject-bob", "preferred_username": "bob"},
	}})
	handler := newTestOIDCHandler(t, provider.URL)
	handler.authService = auth.NewService(testdb.NewMigratedSQLite(t, "oidc-multi-user"))
	ids := make(map[string]int)
	for _, username := range []string{"alice", "bob", "alice"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/auth/oidc/callback?state=fixture-state&code="+username, nil)
		ctx, err := handler.sessionManager.Load(req.Context(), "")
		require.NoError(t, err)
		handler.sessionManager.Put(ctx, "oidc_state", "fixture-state")
		rec := httptest.NewRecorder()
		handler.handleCallback(rec, req.WithContext(ctx))
		require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
		id := handler.sessionManager.GetInt(ctx, "user_id")
		require.Positive(t, id)
		require.Equal(t, username, handler.sessionManager.GetString(ctx, "username"))
		if previous, ok := ids[username]; ok {
			require.Equal(t, previous, id)
		}
		ids[username] = id
	}
	require.NotEqual(t, ids["alice"], ids["bob"])
	users, err := handler.authService.ListUsers(t.Context())
	require.NoError(t, err)
	require.Len(t, users, 2)
	alice, err := handler.authService.GetUser(t.Context(), ids["alice"])
	require.NoError(t, err)
	require.Equal(t, models.UserRoleAdmin, alice.Role)
	bob, err := handler.authService.GetUser(t.Context(), ids["bob"])
	require.NoError(t, err)
	require.Equal(t, models.UserRoleUser, bob.Role)
}

func TestOIDCConfigDoesNotExposePKCEVerifier(t *testing.T) {
	discovery := newOIDCDiscoveryServer(t, []string{"S256"})
	handler := newTestOIDCHandler(t, discovery.URL)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/auth/oidc/config", nil)
	rec := httptest.NewRecorder()
	handler.sessionManager.LoadAndSave(http.HandlerFunc(handler.getConfig)).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var body OIDCConfigResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotContains(t, rec.Body.String(), `"state"`)

	var sessionCookie *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == handler.sessionManager.Cookie.Name {
			sessionCookie = cookie
			break
		}
	}
	require.NotNil(t, sessionCookie)

	secondReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/auth/oidc/config", nil)
	secondReq.AddCookie(sessionCookie)
	ctx, err := handler.sessionManager.Load(secondReq.Context(), sessionCookie.Value)
	require.NoError(t, err)

	storedVerifier := handler.sessionManager.GetString(ctx, "oidc_pkce_verifier")
	assert.NotEmpty(t, storedVerifier)
	storedState := handler.sessionManager.GetString(ctx, "oidc_state")
	assert.NotEmpty(t, storedState)

	authURL, err := url.Parse(body.AuthorizationURL)
	require.NoError(t, err)
	assert.Equal(t, storedState, authURL.Query().Get("state"))

	assert.Contains(t, body.AuthorizationURL, "code_challenge=")
	assert.NotContains(t, body.AuthorizationURL, storedVerifier)
	assert.NotContains(t, rec.Body.String(), `"pkceVerifier"`)
}

func TestOIDCConfigReturnsInternalServerErrorWhenStateGenerationFails(t *testing.T) {
	discovery := newOIDCDiscoveryServer(t, nil)
	handler := newTestOIDCHandler(t, discovery.URL)

	originalReadRandom := oidcReadRandom
	oidcReadRandom = func(_ []byte) error {
		return errors.New("entropy unavailable")
	}
	t.Cleanup(func() {
		oidcReadRandom = originalReadRandom
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/auth/oidc/config", nil)
	ctx, err := handler.sessionManager.Load(req.Context(), "")
	require.NoError(t, err)
	req = req.WithContext(ctx)
	handler.sessionManager.Put(req.Context(), "oidc_state", "stale-state")
	handler.sessionManager.Put(req.Context(), "oidc_pkce_verifier", "stale-verifier")

	rec := httptest.NewRecorder()
	handler.getConfig(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "failed to build OIDC config response")
	assert.Empty(t, handler.sessionManager.GetString(req.Context(), "oidc_state"))
	assert.Empty(t, handler.sessionManager.GetString(req.Context(), "oidc_pkce_verifier"))
}

func TestGeneratePKCEVerifierReturnsErrorWhenRandomReadFails(t *testing.T) {
	originalReadRandom := oidcReadRandom
	oidcReadRandom = func(_ []byte) error {
		return errors.New("entropy unavailable")
	}
	t.Cleanup(func() {
		oidcReadRandom = originalReadRandom
	})

	verifier, err := generatePKCEVerifier()
	require.Error(t, err)
	assert.Empty(t, verifier)
}

func TestGeneratePKCEVerifierEncodesRandomBytes(t *testing.T) {
	originalReadRandom := oidcReadRandom
	oidcReadRandom = func(b []byte) error {
		for i := range b {
			b[i] = byte(i)
		}
		return nil
	}
	t.Cleanup(func() {
		oidcReadRandom = originalReadRandom
	})

	verifier, err := generatePKCEVerifier()
	require.NoError(t, err)
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(func() []byte {
		b := make([]byte, 32)
		for i := range b {
			b[i] = byte(i)
		}
		return b
	}()), verifier)
}

func TestOIDCConfigReturnsInternalServerErrorWhenPKCEVerifierGenerationFails(t *testing.T) {
	discovery := newOIDCDiscoveryServer(t, []string{"S256"})
	handler := newTestOIDCHandler(t, discovery.URL)

	originalReadRandom := oidcReadRandom
	readCalls := 0
	oidcReadRandom = func(b []byte) error {
		readCalls++
		if readCalls == 1 {
			for i := range b {
				b[i] = byte(i)
			}
			return nil
		}
		return errors.New("entropy unavailable")
	}
	t.Cleanup(func() {
		oidcReadRandom = originalReadRandom
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/auth/oidc/config", nil)
	ctx, err := handler.sessionManager.Load(req.Context(), "")
	require.NoError(t, err)
	req = req.WithContext(ctx)
	handler.sessionManager.Put(req.Context(), "oidc_state", "stale-state")
	handler.sessionManager.Put(req.Context(), "oidc_pkce_verifier", "stale-verifier")

	rec := httptest.NewRecorder()
	handler.getConfig(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "failed to build OIDC config response")
	assert.Empty(t, handler.sessionManager.GetString(req.Context(), "oidc_state"))
	assert.Empty(t, handler.sessionManager.GetString(req.Context(), "oidc_pkce_verifier"))
}
