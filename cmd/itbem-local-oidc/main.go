package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const qualificationKeyID = "itbem-local-qualification"

type readyDocument struct {
	IssuerURL string `json:"issuer_url"`
	JWKSURL   string `json:"jwks_url"`
}

func main() {
	listenAddress := flag.String("listen", "127.0.0.1:19090", "loopback address for the ephemeral issuer")
	audience := flag.String("audience", "local-itbem", "ID-token audience configured by the isolated API")
	subject := flag.String("subject", "qa-root", "ephemeral local qualification subject")
	email := flag.String("email", "qa@local.invalid", "allow-listed local bootstrap email")
	tokenFile := flag.String("token-file", "", "required private path receiving the short-lived ID token")
	readyFile := flag.String("ready-file", "", "required private path receiving non-secret issuer metadata")
	tokenTTL := flag.Duration("token-ttl", 15*time.Minute, "short-lived qualification token lifetime")
	flag.Parse()

	if !strings.EqualFold(strings.TrimSpace(os.Getenv("ENV")), "local") {
		log.Fatal("itbem-local-oidc is restricted to ENV=local")
	}
	if err := validateLoopbackListenAddress(*listenAddress); err != nil {
		log.Fatal(err)
	}
	if strings.TrimSpace(*audience) == "" || strings.TrimSpace(*subject) == "" || strings.TrimSpace(*email) == "" {
		log.Fatal("audience, subject, and email are required")
	}
	if *tokenTTL < time.Minute || *tokenTTL > time.Hour {
		log.Fatal("token-ttl must be between one minute and one hour")
	}
	if err := validatePrivateOutputPaths(*tokenFile, *readyFile); err != nil {
		log.Fatal(err)
	}

	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		log.Fatalf("listen for local OIDC qualification: %v", err)
	}
	defer listener.Close()
	issuerURL := "http://" + listener.Addr().String()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("generate ephemeral signing key: %v", err)
	}
	token, err := signIdentityToken(privateKey, issuerURL, strings.TrimSpace(*audience), strings.TrimSpace(*subject), strings.TrimSpace(*email), time.Now(), *tokenTTL)
	if err != nil {
		log.Fatalf("sign local qualification token: %v", err)
	}
	if err := writePrivateFile(*tokenFile, []byte(token)); err != nil {
		log.Fatalf("write local qualification token: %v", err)
	}
	defer os.Remove(*tokenFile)
	ready, err := json.Marshal(readyDocument{IssuerURL: issuerURL, JWKSURL: issuerURL + "/.well-known/jwks.json"})
	if err != nil {
		log.Fatalf("encode local issuer metadata: %v", err)
	}
	if err := writePrivateFile(*readyFile, ready); err != nil {
		log.Fatalf("write local issuer metadata: %v", err)
	}
	defer os.Remove(*readyFile)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/.well-known/jwks.json", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(jwkDocument(&privateKey.PublicKey))
	})
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	fmt.Printf("local OIDC qualification issuer ready at %s\n", issuerURL)
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve local OIDC qualification issuer: %v", err)
	}
}

func validateLoopbackListenAddress(address string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || strings.TrimSpace(port) == "" {
		return fmt.Errorf("listen must be an explicit loopback host:port")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	parsed := net.ParseIP(host)
	if parsed == nil || !parsed.IsLoopback() {
		return fmt.Errorf("listen must bind explicitly to loopback")
	}
	return nil
}

func validatePrivateOutputPaths(tokenFile, readyFile string) error {
	if strings.TrimSpace(tokenFile) == "" || strings.TrimSpace(readyFile) == "" {
		return fmt.Errorf("token-file and ready-file are required")
	}
	tokenPath, err := filepath.Abs(tokenFile)
	if err != nil {
		return fmt.Errorf("resolve token-file: %w", err)
	}
	readyPath, err := filepath.Abs(readyFile)
	if err != nil {
		return fmt.Errorf("resolve ready-file: %w", err)
	}
	if strings.EqualFold(tokenPath, readyPath) {
		return fmt.Errorf("token-file and ready-file must be different paths")
	}
	return nil
}

func writePrivateFile(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func jwkDocument(publicKey *rsa.PublicKey) map[string]any {
	return map[string]any{"keys": []map[string]string{{
		"kty": "RSA",
		"kid": qualificationKeyID,
		"use": "sig",
		"alg": "RS256",
		"n":   base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
	}}}
}

func signIdentityToken(privateKey *rsa.PrivateKey, issuer, audience, subject, email string, now time.Time, ttl time.Duration) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": issuer, "token_use": "id", "aud": audience,
		"sub": subject, "email": email,
		"iat": now.Unix(), "exp": now.Add(ttl).Unix(),
	})
	token.Header["kid"] = qualificationKeyID
	return token.SignedString(privateKey)
}
