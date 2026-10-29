package token

import (
	"events-stocks/models"
	"github.com/golang-jwt/jwt/v5"
	"testing"
)

func TestDedicatedPoolsBindIssuerAudienceTenantAndStableSubject(t *testing.T) {
	cfg := &models.Config{CognitoAwsRegion: "us-east-1", CognitoUserPoolId: "us-east-1_Old", CognitoTenantClientMap: "old=eventiapp", CognitoTenantPools: `{"itbem":{"poolId":"us-east-2_ITBEM","clientId":"new-itbem"}}`}
	claims := jwt.MapClaims{"iss": "https://cognito-idp.us-east-2.amazonaws.com/us-east-2_ITBEM", "aud": "new-itbem", "sub": "22222222-2222-4222-8222-222222222222", "cognito:username": "11111111-1111-4111-8111-111111111111", "custom:legacy_sub": "11111111-1111-4111-8111-111111111111", "token_use": "id"}
	subject, aud, tenant, err := validateIdentityClaims(claims, cfg)
	if err != nil || subject != claims["custom:legacy_sub"] || aud != "new-itbem" || tenant != "itbem" {
		t.Fatalf("unexpected binding: %s %s %s %v", subject, aud, tenant, err)
	}
	claims["iss"] = "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_Old"
	if _, _, _, err := validateIdentityClaims(claims, cfg); err == nil {
		t.Fatal("accepted new audience from old pool")
	}
	claims["aud"] = "old"
	if _, _, _, err := validateIdentityClaims(claims, cfg); err != nil {
		t.Fatal("legacy sessions must remain usable", err)
	}
	claims["iss"] = "https://attacker.example"
	if _, _, _, err := validateIdentityClaims(claims, cfg); err == nil {
		t.Fatal("accepted untrusted issuer")
	}
}
