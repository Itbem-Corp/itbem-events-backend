package awsrepository

import (
	"context"
	"errors"
	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/internal/products"
	"fmt"
	"github.com/gofrs/uuid"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
)

func GetCognitoUser(ctx context.Context, sub string) (*dtos.AuthUser, error) {
	cfg := configuration.LoadConfig()
	target, targetErr := cognitoTargetForSubject(cfg, sub)
	if targetErr != nil {
		return nil, targetErr
	}
	client := target.client

	output, err := client.AdminGetUser(ctx, &cognitoidentityprovider.AdminGetUserInput{
		UserPoolId: aws.String(target.pool),
		Username:   aws.String(target.username),
	})
	if err != nil {
		return nil, err
	}

	user := &dtos.AuthUser{
		Sub:      *output.Username,
		IsActive: output.Enabled,
		Username: aws.ToString(output.Username),
		Status:   string(output.UserStatus),
	}

	for _, attr := range output.UserAttributes {
		val := aws.ToString(attr.Value)
		switch aws.ToString(attr.Name) {
		case "email":
			user.Email = val
		case "given_name":
			user.FirstName = val
		case "family_name":
			user.LastName = val
		case "sub":
			user.Subject = val
		case "email_verified":
			user.EmailVerified = strings.EqualFold(val, "true")
		case "custom:legacy_sub":
			user.LegacySub = val
		}
	}

	if target.qualified {
		target.username = aws.ToString(output.Username)
		canonical, err := cognitoSubject(target, output.UserAttributes)
		if err != nil {
			return nil, err
		}
		user.Sub = canonical
	}
	return user, nil
}

func UpdateCognitoUserAttributes(ctx context.Context, sub string, attributes map[string]string) error {
	var awsAttrs []types.AttributeType
	for key, val := range attributes {
		if key == "custom:legacy_sub" {
			return fmt.Errorf("migration identity is immutable")
		}
		awsName := key
		if key == "first_name" {
			awsName = "given_name"
		}
		if key == "last_name" {
			awsName = "family_name"
		}

		awsAttrs = append(awsAttrs, types.AttributeType{
			Name:  aws.String(awsName),
			Value: aws.String(val),
		})
	}

	return mutateCognitoIdentity(ctx, sub, func(target cognitoTarget) error {
		_, err := target.client.AdminUpdateUserAttributes(ctx, &cognitoidentityprovider.AdminUpdateUserAttributesInput{UserPoolId: aws.String(target.pool), Username: aws.String(target.username), UserAttributes: awsAttrs})
		return err
	})
}

func DeleteCognitoUser(ctx context.Context, sub string) error {
	return mutateCognitoIdentity(ctx, sub, func(target cognitoTarget) error {
		_, err := target.client.AdminDeleteUser(ctx, &cognitoidentityprovider.AdminDeleteUserInput{UserPoolId: aws.String(target.pool), Username: aws.String(target.username)})
		return err
	})
}

func CreateCognitoUser(ctx context.Context, req dtos.CreateAuthUserRequest) (*dtos.AuthUser, error) {
	cfg := configuration.LoadConfig()
	client := configuration.GetCognitoClient()
	target, targetErr := cognitoTargetForTenant(cfg, "eventiapp", req.Email)
	if targetErr != nil {
		return nil, targetErr
	}
	if target.qualified {
		target.username = uuid.Must(uuid.NewV4()).String()
	}
	client = target.client

	output, err := client.AdminCreateUser(ctx, &cognitoidentityprovider.AdminCreateUserInput{
		UserPoolId:        aws.String(target.pool),
		Username:          aws.String(target.username),
		TemporaryPassword: aws.String(req.Password),
		MessageAction:     types.MessageActionTypeSuppress,
		UserAttributes: []types.AttributeType{
			{Name: aws.String("email"), Value: aws.String(req.Email)},
			{Name: aws.String("given_name"), Value: aws.String(req.FirstName)},
			{Name: aws.String("family_name"), Value: aws.String(req.LastName)},
			{Name: aws.String("email_verified"), Value: aws.String("true")},
		},
	})

	if err != nil {
		return nil, err
	}

	newSub, subjectErr := cognitoSubject(target, output.User.Attributes)
	if subjectErr != nil {
		return nil, subjectErr
	}

	_, err = client.AdminSetUserPassword(ctx, &cognitoidentityprovider.AdminSetUserPasswordInput{
		UserPoolId: aws.String(target.pool),
		Username:   aws.String(target.username),
		Password:   aws.String(req.Password),
		Permanent:  true,
	})

	if err != nil {
		return nil, fmt.Errorf("error setting password: %w", err)
	}

	return &dtos.AuthUser{
		Sub:       newSub,
		Email:     req.Email,
		FirstName: req.FirstName,
		LastName:  req.LastName,
		IsActive:  true,
	}, nil
}

func DisableCognitoUser(ctx context.Context, sub string) error {
	return mutateCognitoIdentity(ctx, sub, func(target cognitoTarget) error {
		_, err := target.client.AdminDisableUser(ctx, &cognitoidentityprovider.AdminDisableUserInput{UserPoolId: aws.String(target.pool), Username: aws.String(target.username)})
		return err
	})
}

func EnableCognitoUser(ctx context.Context, sub string) error {
	return mutateCognitoIdentity(ctx, sub, func(target cognitoTarget) error {
		_, err := target.client.AdminEnableUser(ctx, &cognitoidentityprovider.AdminEnableUserInput{UserPoolId: aws.String(target.pool), Username: aws.String(target.username)})
		return err
	})
}

// ResendCognitoInvitation re-sends the pool's standard invitation message to
// an account that has not completed its first sign-in
// (AdminCreateUser MessageAction=RESEND). Cognito itself rejects the call for
// accounts that already changed their temporary password.
func ResendCognitoInvitation(ctx context.Context, username string) error {
	cfg := configuration.LoadConfig()
	targets, err := cognitoMutationTargets(ctx, cfg, username)
	if err != nil {
		return err
	}
	resent := false
	for _, target := range targets {
		user, err := target.client.AdminGetUser(ctx, &cognitoidentityprovider.AdminGetUserInput{UserPoolId: aws.String(target.pool), Username: aws.String(target.username)})
		if err != nil {
			return err
		}
		if user.UserStatus != types.UserStatusTypeForceChangePassword {
			continue
		}
		_, err = target.client.AdminCreateUser(ctx, &cognitoidentityprovider.AdminCreateUserInput{UserPoolId: aws.String(target.pool), Username: aws.String(target.username), MessageAction: types.MessageActionTypeResend})
		if err != nil {
			return err
		}
		resent = true
	}
	if !resent {
		return fmt.Errorf("user has no pending invitation")
	}
	return nil
}

func InviteCognitoUser(
	ctx context.Context,
	email, firstName, lastName string,
) (*dtos.AuthUser, error) {
	return InviteCognitoUserForTenant(ctx, email, firstName, lastName, "eventiapp")
}

func InviteCognitoUserForTenant(
	ctx context.Context,
	email, firstName, lastName, tenantCode string,
) (*dtos.AuthUser, error) {
	cfg := configuration.LoadConfig()
	client := configuration.GetCognitoClient()
	definition, known := products.Resolve(tenantCode)
	if !known {
		return nil, fmt.Errorf("unsupported invitation tenant %q", tenantCode)
	}
	tenantCode = definition.Code.String()
	target, targetErr := cognitoTargetForTenant(cfg, tenantCode, email)
	if targetErr != nil {
		return nil, targetErr
	}
	legacySub := ""
	if target.qualified {
		legacy, err := client.AdminGetUser(ctx, &cognitoidentityprovider.AdminGetUserInput{UserPoolId: aws.String(cfg.CognitoUserPoolId), Username: aws.String(email)})
		var missing *types.UserNotFoundException
		if err != nil && !errors.As(err, &missing) {
			return nil, err
		}
		if legacy != nil {
			if !legacy.Enabled {
				return nil, fmt.Errorf("legacy identity is disabled")
			}
			legacySub = attrValue(legacy.UserAttributes, "sub")
			if legacySub == "" {
				return nil, fmt.Errorf("legacy identity is incomplete")
			}
			target.username = legacySub
		} else {
			target.username = uuid.Must(uuid.NewV4()).String()
		}
		client = target.client
	}
	attributes := []types.AttributeType{
		{Name: aws.String("email"), Value: aws.String(email)},
		{Name: aws.String("given_name"), Value: aws.String(firstName)},
		{Name: aws.String("family_name"), Value: aws.String(lastName)},
		{Name: aws.String("email_verified"), Value: aws.String("true")},
		{Name: aws.String("custom:invited_via"), Value: aws.String(tenantCode)},
	}
	if legacySub != "" {
		attributes = append(attributes, types.AttributeType{Name: aws.String("custom:legacy_sub"), Value: aws.String(legacySub)})
	}

	out, err := client.AdminCreateUser(ctx, &cognitoidentityprovider.AdminCreateUserInput{
		UserPoolId:     aws.String(target.pool),
		Username:       aws.String(target.username),
		UserAttributes: attributes,
	})

	if err != nil {
		return nil, err
	}
	if target.qualified {
		if out.User == nil {
			return nil, fmt.Errorf("Cognito invitation returned no identity")
		}
		target.username = aws.ToString(out.User.Username)
		canonical, err := cognitoSubject(target, out.User.Attributes)
		if err != nil {
			return nil, err
		}
		return &dtos.AuthUser{Sub: canonical, Subject: attrValue(out.User.Attributes, "sub"), LegacySub: legacySub, Email: email, FirstName: firstName, LastName: lastName, IsActive: true}, nil
	}
	cognitoSub := ""
	if out.User != nil {
		for _, attribute := range out.User.Attributes {
			if aws.ToString(attribute.Name) == "sub" {
				cognitoSub = aws.ToString(attribute.Value)
				break
			}
		}
	}
	if cognitoSub == "" {
		// AdminCreateUser returns the sign-in username separately from the
		// immutable Cognito subject. Persisting the username here would make the
		// first JWT-backed sync create a second local identity.
		if out.User != nil && aws.ToString(out.User.Username) != "" {
			_, _ = client.AdminDeleteUser(ctx, &cognitoidentityprovider.AdminDeleteUserInput{
				UserPoolId: aws.String(cfg.CognitoUserPoolId),
				Username:   out.User.Username,
			})
		}
		return nil, fmt.Errorf("cognito invitation did not return a user subject")
	}

	return &dtos.AuthUser{
		Sub:       cognitoSub,
		Email:     email,
		FirstName: firstName,
		LastName:  lastName,
		IsActive:  true,
	}, nil
}
