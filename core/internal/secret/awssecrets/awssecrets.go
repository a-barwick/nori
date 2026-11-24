package awssecrets

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	sdk "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"nori/internal/secret"
	"time"
)

type Lookup struct{ Region string }

var _ secret.Lookup = Lookup{}

func (l Lookup) Secret(ctx context.Context, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(l.Region))
	if err != nil {
		return "", err
	}
	return Read(ctx, sdk.NewFromConfig(cfg), ref)
}
func Read(ctx context.Context, api *sdk.Client, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("secret name or ARN is required")
	}
	out, err := api.GetSecretValue(ctx, &sdk.GetSecretValueInput{SecretId: aws.String(ref)})
	if err != nil {
		return "", err
	}
	if out.SecretString != nil {
		return *out.SecretString, nil
	}
	if len(out.SecretBinary) > 0 {
		return string(out.SecretBinary), nil
	}
	return "", errors.New("secret has no payload")
}
