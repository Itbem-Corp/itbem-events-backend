package awsrepository

import (
	"context"
	"events-stocks/configuration"
	"events-stocks/dtos"
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

	cfg := configuration.LoadConfig()
	client := configuration.GetCognitoClient()

	out, err := client.AdminCreateUser(ctx, &cognitoidentityprovider.AdminCreateUserInput{
		UserPoolId: aws.String(cfg.CognitoUserPoolId),
		Username:   aws.String(email),
		UserAttributes: []types.AttributeType{
			{Name: aws.String("email"), Value: aws.String(email)},
			{Name: aws.String("given_name"), Value: aws.String(firstName)},
			{Name: aws.String("family_name"), Value: aws.String(lastName)},
			{Name: aws.String("email_verified"), Value: aws.String("true")},
		},
	})

	if err != nil {
		return nil, err
	}

	return &dtos.AuthUser{
		Sub:       *out.User.Username,
		Email:     email,
		FirstName: firstName,
		LastName:  lastName,
		IsActive:  true,
	}, nil
}
