package observer

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

type fakeSecrets map[string]string

func (f fakeSecrets) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	v, ok := f[aws.ToString(in.SecretId)]
	if !ok {
		return nil, errors.New("ResourceNotFoundException")
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: aws.String(v)}, nil
}

func TestFetchSalt(t *testing.T) {
	sm := fakeSecrets{"plain": " s3cr3t\n", "kv": `{"salt":"abc","other":"x"}`, "empty": "", "kv-missing": `{"other":"x"}`}
	for id, want := range map[string]string{"plain": "s3cr3t", "kv": "abc"} {
		if got, err := fetchSalt(context.Background(), sm, id); err != nil || got != want {
			t.Errorf("fetchSalt(%s) = %q, %v; want %q", id, got, err, want)
		}
	}
	for _, id := range []string{"empty", "kv-missing", "absent"} {
		if _, err := fetchSalt(context.Background(), sm, id); err == nil {
			t.Errorf("fetchSalt(%s): want an error", id)
		}
	}
}

// TestFetchSaltAgainstEmulator reads a real Secrets Manager API (`make
// test-aws` starts moto and sets OBSERVER_SM_TEST_ENDPOINT).
func TestFetchSaltAgainstEmulator(t *testing.T) {
	endpoint := os.Getenv("OBSERVER_SM_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("OBSERVER_SM_TEST_ENDPOINT not set")
	}
	sm := secretsmanager.New(secretsmanager.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	name := "aintercept-test-salt-" + t.Name()
	_, _ = sm.DeleteSecret(context.Background(), &secretsmanager.DeleteSecretInput{SecretId: aws.String(name), ForceDeleteWithoutRecovery: aws.Bool(true)})
	if _, err := sm.CreateSecret(context.Background(), &secretsmanager.CreateSecretInput{Name: aws.String(name), SecretString: aws.String(`{"salt":"from-sm"}`)}); err != nil {
		t.Fatal(err)
	}
	if got, err := fetchSalt(context.Background(), sm, name); err != nil || got != "from-sm" {
		t.Fatalf("got %q, %v", got, err)
	}
}
