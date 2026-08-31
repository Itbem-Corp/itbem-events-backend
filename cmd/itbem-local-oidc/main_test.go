package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestValidateLoopbackListenAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:19090", "[::1]:19090", "localhost:19090"} {
		require.NoError(t, validateLoopbackListenAddress(address), address)
	}
	for _, address := range []string{"0.0.0.0:19090", "example.com:19090", "127.0.0.1", ":19090"} {
		require.Error(t, validateLoopbackListenAddress(address), address)
	}
}

func TestValidatePrivateOutputPathsRejectsMissingOrSharedPaths(t *testing.T) {
	require.Error(t, validatePrivateOutputPaths("", "ready.json"))
	shared := filepath.Join(t.TempDir(), "shared")
	require.Error(t, validatePrivateOutputPaths(shared, shared))
	require.NoError(t, validatePrivateOutputPaths(shared+".token", shared+".json"))
}

func TestWritePrivateFileIsExclusiveAndOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "token")
	require.NoError(t, writePrivateFile(path, []byte("opaque")))
	require.Error(t, writePrivateFile(path, []byte("replacement")))
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, []byte("opaque"), contents)
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestJWKAndSignedTokenUseTheSameEphemeralKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	now := time.Now().Truncate(time.Second)
	signed, err := signIdentityToken(key, "http://127.0.0.1:19090", "local-client", "qa-root", "qa@local.invalid", now, 5*time.Minute)
	require.NoError(t, err)

	document := jwkDocument(&key.PublicKey)
	keys := document["keys"].([]map[string]string)
	require.Len(t, keys, 1)
	require.Equal(t, base64.RawURLEncoding.EncodeToString(key.N.Bytes()), keys[0]["n"])
	require.Equal(t, qualificationKeyID, keys[0]["kid"])

	parsed, err := jwt.Parse(signed, func(token *jwt.Token) (any, error) {
		require.Equal(t, qualificationKeyID, token.Header["kid"])
		return &key.PublicKey, nil
	})
	require.NoError(t, err)
	require.True(t, parsed.Valid)
	claims := parsed.Claims.(jwt.MapClaims)
	require.Equal(t, "qa-root", claims["sub"])
	require.Equal(t, "local-client", claims["aud"])
}
