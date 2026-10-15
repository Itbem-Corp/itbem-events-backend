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

func TestIdentityFlagsParseAndRefuseRepeats(t *testing.T) {
	var identities identityFlags
	require.NoError(t, identities.Set("org-a:qa-org-a:org-a@local.invalid"))
	require.NoError(t, identities.Set("org-b:qa-org-b:org-b@local.invalid"))
	require.Equal(t, extraIdentity{name: "org-b", subject: "qa-org-b", email: "org-b@local.invalid"}, identities[1])
	for _, bad := range []string{"org-a:qa-x:x@local.invalid", "org-c:qa-org-a:c@local.invalid", "Org C:s:e", "org-c:s", "org-c::e", "../x:s:e"} {
		require.Error(t, identities.Set(bad), bad)
	}
	require.Len(t, identities, 2)
}

func TestIdentityTokensLiveNextToThePrimaryTokenWithoutOverwritingIt(t *testing.T) {
	dir := t.TempDir()
	token, ready := filepath.Join(dir, "id.jwt"), filepath.Join(dir, "ready.json")
	require.Equal(t, filepath.Join(dir, "org-b.jwt"), identityTokenPath(token, "org-b"))
	require.NoError(t, validateIdentityPaths(token, ready, "qa-root", identityFlags{{name: "org-b", subject: "qa-org-b", email: "b@local.invalid"}}))
	require.Error(t, validateIdentityPaths(token, ready, "qa-root", identityFlags{{name: "id", subject: "qa-org-b", email: "b@local.invalid"}}))
	require.Error(t, validateIdentityPaths(filepath.Join(dir, "x"), filepath.Join(dir, "org-b.jwt"), "qa-root", identityFlags{{name: "org-b", subject: "qa-org-b", email: "b@local.invalid"}}))
	require.Error(t, validateIdentityPaths(token, ready, "qa-root", identityFlags{{name: "org-b", subject: "qa-root", email: "b@local.invalid"}}))
}

func TestIdentityTokensAreRemovedWhenALaterOneCannotBeWritten(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	dir := t.TempDir()
	token := filepath.Join(dir, "id.jwt")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "org-b.jwt"), []byte("stale"), 0o600))
	identities := identityFlags{{name: "org-a", subject: "qa-org-a", email: "a@local.invalid"}, {name: "org-b", subject: "qa-org-b", email: "b@local.invalid"}}
	_, err = writeIdentityTokens(key, "http://127.0.0.1:19090", "local-client", token, identities, time.Now(), time.Minute)
	require.ErrorContains(t, err, "org-b")
	_, statErr := os.Stat(filepath.Join(dir, "org-a.jwt"))
	require.True(t, os.IsNotExist(statErr), "the token written before the failure is removed")

	require.NoError(t, os.Remove(filepath.Join(dir, "org-b.jwt")))
	paths, err := writeIdentityTokens(key, "http://127.0.0.1:19090", "local-client", token, identities, time.Now(), time.Minute)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(dir, "org-a.jwt"), filepath.Join(dir, "org-b.jwt")}, paths)
}
