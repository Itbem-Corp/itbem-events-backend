package cognitoidentity

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"events-stocks/models"
)

type Pool struct {
	PoolID   string `json:"poolId"`
	ClientID string `json:"clientId"`
}

var poolPattern = regexp.MustCompile(`^([a-z]{2}(?:-[a-z]+)+-\d)_[A-Za-z0-9]+$`)
var subjectPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (p Pool) Region() string { return strings.SplitN(p.PoolID, "_", 2)[0] }
func (p Pool) Issuer() string {
	return "https://cognito-idp." + p.Region() + ".amazonaws.com/" + p.PoolID
}

func Parse(cfg *models.Config) (map[string]Pool, error) {
	result := map[string]Pool{}
	if cfg == nil {
		return nil, fmt.Errorf("missing Cognito configuration")
	}
	if strings.TrimSpace(cfg.CognitoTenantPools) == "" {
		return result, nil
	}
	if err := json.Unmarshal([]byte(cfg.CognitoTenantPools), &result); err != nil {
		return nil, fmt.Errorf("invalid Cognito tenant pool map: %w", err)
	}
	seenPools, seenClients := map[string]bool{}, map[string]bool{}
	for tenant, p := range result {
		if tenant != "eventiapp" && tenant != "itbem" && tenant != "cafettonhouse" {
			return nil, fmt.Errorf("unknown Cognito tenant")
		}
		if !poolPattern.MatchString(p.PoolID) || p.ClientID == "" || seenPools[p.PoolID] || seenClients[p.ClientID] || p.PoolID == cfg.CognitoUserPoolId {
			return nil, fmt.Errorf("invalid or repeated dedicated Cognito pool/client")
		}
		seenPools[p.PoolID] = true
		seenClients[p.ClientID] = true
	}
	return result, nil
}

// A migrated user keeps its original database identity. Only the migration
// Lambda can assign the immutable legacy_sub attribute; public app clients
// must not have write access. The migration also fixes username to legacy_sub.
func CanonicalSubject(p Pool, subject, username, legacy string) (string, error) {
	if !subjectPattern.MatchString(subject) {
		return "", fmt.Errorf("invalid Cognito subject")
	}
	if legacy != "" {
		if !subjectPattern.MatchString(legacy) || username != legacy {
			return "", fmt.Errorf("invalid migrated Cognito identity")
		}
		return legacy, nil
	}
	if username == "" || strings.Contains(username, "|") {
		return "", fmt.Errorf("invalid Cognito username")
	}
	return p.PoolID + "|" + username, nil
}
