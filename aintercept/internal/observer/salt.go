package observer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// secretGetter is the Secrets Manager call the salt loader needs.
type secretGetter interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// fetchSalt reads the key-hash salt from Secrets Manager. The secret is either
// the salt itself or a JSON object with a "salt" field (a key/value secret).
func fetchSalt(ctx context.Context, sm secretGetter, secretID string) (string, error) {
	out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretID)})
	if err != nil {
		return "", fmt.Errorf("get secret %s: %w", secretID, err)
	}
	v := strings.TrimSpace(aws.ToString(out.SecretString))
	var kv struct {
		Salt string `json:"salt"`
	}
	if strings.HasPrefix(v, "{") && json.Unmarshal([]byte(v), &kv) == nil {
		v = strings.TrimSpace(kv.Salt)
	}
	if v == "" {
		return "", fmt.Errorf("secret %s has no salt value (want a string, or JSON with a \"salt\" field)", secretID)
	}
	return v, nil
}
