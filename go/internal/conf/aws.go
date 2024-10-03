package conf

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"premai.io/Ayup/go/internal/terror"
)

func LoadConfigFromAWS(ctx context.Context) (string, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithEC2IMDSRegion())
	if err != nil {
		return "", terror.Errorf(ctx, "config LoadDefaultConfig: %w", err)
	}

	svc := secretsmanager.NewFromConfig(cfg)

	secretValue, err := getSecretValue(ctx, svc, "ayup-preauth-conf")
	if err != nil {
		return "", err
	}

	return secretValue, nil
}

// getSecretValue retrieves the value of the specified secret
func getSecretValue(ctx context.Context, svc *secretsmanager.Client, secretName string) (string, error) {
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretName),
	}

	result, err := svc.GetSecretValue(ctx, input)
	if err != nil {
		return "", terror.Errorf(ctx, "svc GetSecretValue: %w", err)
	}

	if result.SecretString != nil {
		return *result.SecretString, nil
	}

	return "", terror.Errorf(ctx, "secret %s does not contain a SecretString", secretName)
}
