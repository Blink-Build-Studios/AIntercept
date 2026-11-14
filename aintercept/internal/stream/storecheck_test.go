package stream

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/process"
)

func TestStoreCheckPassesOnACorrectStore(t *testing.T) {
	rep, err := StoreCheck(context.Background(), NewMemStore(RealClock{}), "check/")
	if err != nil || !rep.OK() {
		t.Fatalf("%+v %v", rep, err)
	}
}

// A store that silently overwrites instead of refusing (no conditional writes)
// must fail the check: the design can't run on it.
func TestStoreCheckFailsWithoutCreateOnly(t *testing.T) {
	rep, err := StoreCheck(context.Background(), overwritingStore{NewMemStore(RealClock{})}, "check/")
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() || rep.CreateOnly {
		t.Fatalf("a store without create-only writes passed: %+v", rep)
	}
}

type overwritingStore struct{ *MemStore }

func (o overwritingStore) PutIfAbsent(ctx context.Context, key string, b []byte) (bool, error) {
	return true, o.Put(ctx, key, b)
}

// Against a real S3-compatible endpoint, when one is configured (moto in
// docker-compose.aws.yml: STORECHECK_ENDPOINT=http://localhost:5055).
func TestStoreCheckAgainstS3(t *testing.T) {
	ep := os.Getenv("STORECHECK_ENDPOINT")
	if ep == "" {
		t.Skip("STORECHECK_ENDPOINT not set")
	}
	ctx := context.Background()
	s, err := process.NewS3Store(ctx, process.S3Config{Bucket: envOr("STORECHECK_BUCKET", "storecheck"), Region: "us-east-1",
		Endpoint: ep, AccessKeyID: "test", SecretAccessKey: "test", ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucketIfMissing(ctx); err != nil {
		t.Fatal(err)
	}
	rep, err := StoreCheck(ctx, S3Store{s}, "storecheck-"+time.Now().Format("150405")+"/")
	if err != nil || !rep.OK() {
		t.Fatalf("%+v %v", rep, err)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
