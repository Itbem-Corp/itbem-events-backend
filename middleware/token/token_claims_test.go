package token

import (
	"events-stocks/models"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestValidateCognitoIdentityClaimsBindsAudienceToTenant(t *testing.T) {
	cfg := &models.Config{
		CognitoAwsRegion:       "us-east-1",
		CognitoUserPoolId:      "us-east-1_pool",
		CognitoTenantClientMap: "client-eventi=eventiapp,client-itbem=itbem",
	}
	claims := jwt.MapClaims{
		"iss":       "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_pool",
		"token_use": "id",
		"aud":       "client-itbem",
		"sub":       "user-123",
	}

	subject, audience, tenant, err := validateCognitoIdentityClaims(claims, cfg)
	if err != nil || subject != "user-123" || audience != "client-itbem" || tenant != "itbem" {
		t.Fatalf("unexpected result: sub=%q aud=%q tenant=%q err=%v", subject, audience, tenant, err)
	}
}

func TestValidateCognitoIdentityClaimsRejectsUnsafeTokens(t *testing.T) {
	cfg := &models.Config{
		CognitoAwsRegion:       "us-east-1",
		CognitoUserPoolId:      "us-east-1_pool",
		CognitoTenantClientMap: "client-itbem=itbem",
	}
	issuer := "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_pool"
	tests := map[string]jwt.MapClaims{
		"access token":   {"iss": issuer, "token_use": "access", "aud": "client-itbem", "sub": "user"},
		"unknown client": {"iss": issuer, "token_use": "id", "aud": "client-other", "sub": "user"},
		"wrong issuer":   {"iss": "https://attacker.example", "token_use": "id", "aud": "client-itbem", "sub": "user"},
	}
	for name, claims := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := validateCognitoIdentityClaims(claims, cfg); err == nil {
				t.Fatal("expected token to be rejected")
			}
		})
	}
}
