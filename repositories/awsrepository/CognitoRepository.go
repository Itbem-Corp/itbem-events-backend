package awsrepository

import (
	"context"
	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/internal/products"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
)

func GetCognitoUser(ctx context.Context, sub string) (*dtos.AuthUser, error) {
	cfg := configuration.LoadConfig()
	client := configuration.GetCognitoClient()

	output, err := client.AdminGetUser(ctx, &cognitoidentityprovider.AdminGetUserInput{
		UserPoolId: aws.String(cfg.CognitoUserPoolId),
		Username:   aws.String(sub),
	})
	if err != nil {
		return nil, err
	}

	user := &dtos.AuthUser{
		Sub:      *output.Username,
		IsActive: output.Enabled,
	}

	for _, attr := range output.UserAttributes {
		val := *attr.Value
		switch *attr.Name {
		case "email":
			user.Email = val
		case "given_name":
			user.FirstName = val
		case "family_name":
			user.LastName = val
		}
	}

	return user, nil
}

func UpdateCognitoUserAttributes(ctx context.Context, sub string, attributes map[string]string) error {
	cfg := configuration.LoadConfig()
	client := configuration.GetCognitoClient()

	var awsAttrs []types.AttributeType
	for key, val := range attributes {
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

	_, err := client.AdminUpdateUserAttributes(ctx, &cognitoidentityprovider.AdminUpdateUserAttributesInput{
		UserPoolId:     aws.String(cfg.CognitoUserPoolId),
		Username:       aws.String(sub),
		UserAttributes: awsAttrs,
	})

	return err
}

func DeleteCognitoUser(ctx context.Context, sub string) error {
	cfg := configuration.LoadConfig()
	client := configuration.GetCognitoClient()

	_, err := client.AdminDeleteUser(ctx, &cognitoidentityprovider.AdminDeleteUserInput{
		UserPoolId: aws.String(cfg.CognitoUserPoolId),
		Username:   aws.String(sub),
	})

	return err
}

func CreateCognitoUser(ctx context.Context, req dtos.CreateAuthUserRequest) (*dtos.AuthUser, error) {
	cfg := configuration.LoadConfig()
	client := configuration.GetCognitoClient()

	output, err := client.AdminCreateUser(ctx, &cognitoidentityprovider.AdminCreateUserInput{
		UserPoolId:        aws.String(cfg.CognitoUserPoolId),
		Username:          aws.String(req.Email),
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

	newSub := *output.User.Username

	_, err = client.AdminSetUserPassword(ctx, &cognitoidentityprovider.AdminSetUserPasswordInput{
		UserPoolId: aws.String(cfg.CognitoUserPoolId),
		Username:   aws.String(req.Email),
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
	cfg := configuration.LoadConfig()
	client := configuration.GetCognitoClient()

	_, err := client.AdminDisableUser(ctx, &cognitoidentityprovider.AdminDisableUserInput{
		UserPoolId: aws.String(cfg.CognitoUserPoolId),
		Username:   aws.String(sub),
	})

	return err
}

func EnableCognitoUser(ctx context.Context, sub string) error {
	cfg := configuration.LoadConfig()
	client := configuration.GetCognitoClient()

	_, err := client.AdminEnableUser(ctx, &cognitoidentityprovider.AdminEnableUserInput{
		UserPoolId: aws.String(cfg.CognitoUserPoolId),
		Username:   aws.String(sub),
	})

	return err
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

	out, err := client.AdminCreateUser(ctx, &cognitoidentityprovider.AdminCreateUserInput{
		UserPoolId: aws.String(cfg.CognitoUserPoolId),
		Username:   aws.String(email),
		UserAttributes: []types.AttributeType{
			{Name: aws.String("email"), Value: aws.String(email)},
			{Name: aws.String("given_name"), Value: aws.String(firstName)},
			{Name: aws.String("family_name"), Value: aws.String(lastName)},
			{Name: aws.String("email_verified"), Value: aws.String("true")},
			// This attribute only selects the branding of the initial invitation.
			// Authorization continues to come from application memberships and
			// the signed app-client audience, never from this mutable attribute.
			{Name: aws.String("custom:invited_via"), Value: aws.String(tenantCode)},
		},
	})

	if err != nil {
		return nil, err
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
