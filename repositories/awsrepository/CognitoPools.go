package awsrepository

import (
	"context"
	"errors"
	"events-stocks/configuration"
	"events-stocks/internal/cognitoidentity"
	"events-stocks/models"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"strings"
)

type cognitoTarget struct {
	pool      string
	username  string
	client    *cognitoidentityprovider.Client
	qualified bool
}

func clientForCognitoPool(pool string) *cognitoidentityprovider.Client {
	client := configuration.GetCognitoClient()
	region := strings.SplitN(pool, "_", 2)[0]
	if client.Options().Region == region {
		return client
	}
	options := client.Options()
	options.Region = region
	return cognitoidentityprovider.New(options)
}

func cognitoTargetForSubject(cfg *models.Config, sub string) (cognitoTarget, error) {
	pool, username, qualified := strings.Cut(sub, "|")
	if !qualified {
		return cognitoTarget{cfg.CognitoUserPoolId, sub, configuration.GetCognitoClient(), false}, nil
	}
	pools, err := cognitoidentity.Parse(cfg)
	if err != nil {
		return cognitoTarget{}, err
	}
	for _, p := range pools {
		if p.PoolID == pool && username != "" && !strings.Contains(username, "|") {
			return cognitoTarget{pool, username, clientForCognitoPool(pool), true}, nil
		}
	}
	return cognitoTarget{}, fmt.Errorf("identity pool is not configured")
}

func cognitoTargetForTenant(cfg *models.Config, tenant, username string) (cognitoTarget, error) {
	pools, err := cognitoidentity.Parse(cfg)
	if err != nil {
		return cognitoTarget{}, err
	}
	if p, ok := pools[tenant]; ok {
		return cognitoTarget{p.PoolID, username, clientForCognitoPool(p.PoolID), true}, nil
	}
	if len(pools) > 0 {
		return cognitoTarget{}, fmt.Errorf("dedicated Cognito pool is missing for tenant")
	}
	return cognitoTarget{cfg.CognitoUserPoolId, username, configuration.GetCognitoClient(), false}, nil
}

func attrValue(attrs []types.AttributeType, name string) string {
	for _, a := range attrs {
		if aws.ToString(a.Name) == name {
			return aws.ToString(a.Value)
		}
	}
	return ""
}

func cognitoSubject(target cognitoTarget, attrs []types.AttributeType) (string, error) {
	subject := attrValue(attrs, "sub")
	if subject == "" {
		return "", fmt.Errorf("Cognito did not return an immutable subject")
	}
	if !target.qualified {
		return subject, nil
	}
	return cognitoidentity.CanonicalSubject(cognitoidentity.Pool{PoolID: target.pool}, subject, target.username, attrValue(attrs, "custom:legacy_sub"))
}

// Old identities can exist in multiple product pools during migration.
// Attribute/activation/deletion operations reach all authenticated copies.
// A same-named account without the immutable migration attribute is never linked.
func cognitoMutationTargets(ctx context.Context, cfg *models.Config, sub string) ([]cognitoTarget, error) {
	target, err := cognitoTargetForSubject(cfg, sub)
	if err != nil {
		return nil, err
	}
	if target.qualified {
		return []cognitoTarget{target}, nil
	}
	result := []cognitoTarget{target}
	pools, err := cognitoidentity.Parse(cfg)
	if err != nil {
		return nil, err
	}
	for _, p := range pools {
		client := clientForCognitoPool(p.PoolID)
		out, err := client.AdminGetUser(ctx, &cognitoidentityprovider.AdminGetUserInput{UserPoolId: aws.String(p.PoolID), Username: aws.String(sub)})
		var missing *types.UserNotFoundException
		if errors.As(err, &missing) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if attrValue(out.UserAttributes, "custom:legacy_sub") == sub && aws.ToString(out.Username) == sub {
			result = append(result, cognitoTarget{p.PoolID, sub, client, true})
		}
	}
	return result, nil
}

func mutateCognitoIdentity(ctx context.Context, sub string, mutate func(cognitoTarget) error) error {
	targets, err := cognitoMutationTargets(ctx, configuration.LoadConfig(), sub)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := mutate(target); err != nil {
			return err
		}
	}
	return nil
}
