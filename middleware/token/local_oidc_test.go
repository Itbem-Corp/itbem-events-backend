package token

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestResolveIdentityProviderEndpointsDefaultsToCognito(t *testing.T) {
	endpoints, err := resolveIdentityProviderEndpoints(&models.Config{
		CognitoAwsRegion:  "us-east-1",
		CognitoUserPoolId: "us-east-1_pool",
	}, "production")
	require.NoError(t, err)
	require.Equal(t, "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_pool", endpoints.issuer)
	require.Equal(t, endpoints.issuer+"/.well-known/jwks.json", endpoints.jwksURL)
}

func TestResolveIdentityProviderEndpointsAllowsOnlyPairedLocalLoopbackOverrides(t *testing.T) {
	credentialBearingJWKS := (&url.URL{
		Scheme: "http",
		User:   url.UserPassword("fixture-user", "fixture-password"),
		Host:   "127.0.0.1:19090",
		Path:   "/jwks",
	}).String()
	tests := map[string]struct {
		issuer      string
		jwks        string
		environment string
		wantError   string
	}{
		"paired local endpoints": {
			issuer: "http://127.0.0.1:19090", jwks: "http://127.0.0.1:19090/.well-known/jwks.json", environment: "local",
		},
		"one-sided override": {
			issuer: "http://127.0.0.1:19090", environment: "local", wantError: "must be configured together",
		},
		"deployed override": {
			issuer: "http://127.0.0.1:19090", jwks: "http://127.0.0.1:19090/jwks", environment: "production", wantError: "restricted to ENV=local",
		},
		"remote issuer": {
			issuer: "https://identity.example.com", jwks: "http://127.0.0.1:19090/jwks", environment: "local", wantError: "loopback",
		},
		"credential-bearing JWKS": {
			issuer: "http://127.0.0.1:19090", jwks: credentialBearingJWKS, environment: "local", wantError: "must not contain credentials",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			endpoints, err := resolveIdentityProviderEndpoints(&models.Config{
				CognitoAwsRegion:  "us-east-1",
				CognitoUserPoolId: "us-east-1_pool",
				OIDCIssuerURL:     test.issuer,
				OIDCJWKSURL:       test.jwks,
			}, test.environment)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, strings.TrimRight(test.issuer, "/"), endpoints.issuer)
			require.Equal(t, test.jwks, endpoints.jwksURL)
		})
	}
}

func TestAuthenticationAcceptsSignedLoopbackOIDCOnlyInLocalEnvironment(t *testing.T) {
	t.Setenv("ENV", "local")
	resetJWKSCacheForTest(t)

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyID := "qualification-key"
	jwksDocument := map[string]any{"keys": []map[string]string{{
		"kty": "RSA",
		"kid": keyID,
		"use": "sig",
		"alg": "RS256",
		"n":   base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
	}}}

	issuer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/.well-known/jwks.json" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(response).Encode(jwksDocument))
	}))
	t.Cleanup(issuer.Close)

	cfg := &models.Config{
		CognitoAwsRegion:       "us-east-1",
		CognitoUserPoolId:      "unused-in-local-qualification",
		CognitoTenantClientMap: "local-client=itbem",
		TenantHostMap:          "api.itbem.localhost=itbem",
		TenantBucketMap:        "itbem=qualification-bucket",
		OIDCIssuerURL:          issuer.URL,
		OIDCJWKSURL:            issuer.URL + "/.well-known/jwks.json",
	}
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": issuer.URL, "token_use": "id", "aud": "local-client",
		"sub": "qa-root", "email": "qa@local.invalid",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	})
	token.Header["kid"] = keyID
	signed, err := token.SignedString(privateKey)
	require.NoError(t, err)

	e := echo.New()
	e.GET("/protected", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"subject": c.Get("cognito_sub"),
			"tenant":  c.Get("tenant_code"),
			"email":   c.Get("user_email"),
		})
	}, Autenticacion(cfg))
	request := httptest.NewRequest(http.MethodGet, "http://api.itbem.localhost/protected", nil)
	request.Host = "api.itbem.localhost"
	request.Header.Set(echo.HeaderAuthorization, "Bearer "+signed)
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{"subject":"qa-root","tenant":"itbem","email":"qa@local.invalid"}`, response.Body.String())
}

func resetJWKSCacheForTest(t *testing.T) {
	t.Helper()
	jwksMu.Lock()
	if jwks != nil {
		jwks.EndBackground()
	}
	jwks = nil
	jwksSourceURL = ""
	jwksMu.Unlock()
	t.Cleanup(func() {
		jwksMu.Lock()
		if jwks != nil {
			jwks.EndBackground()
		}
		jwks = nil
		jwksSourceURL = ""
		jwksMu.Unlock()
	})
}
