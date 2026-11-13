package process

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // S3's own ETag for a single-part object: an identity, not a security check
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ETags and conditional writes: the fencing primitive of docs/PIPELINE.md §3.1.
// ETag(key) is the object's current ETag (a HEAD); PutIf(key, b, etag) writes
// only if the object's ETag is still etag (If-Match), or, with etag "", only if
// there's no object (If-None-Match: *). A refused write is ErrPrecondition.

// ErrPrecondition is a conditional write the store refused: the object changed
// (or appeared, or vanished) since its ETag was captured.
var ErrPrecondition = errors.New("precondition failed")

// NormalizeETag strips the quotes S3 puts around an ETag. Every ETag the stores
// return and every one they send goes through it: DO Spaces 412s a quoted
// If-Match even when it's current, and AWS accepts it unquoted.
func NormalizeETag(e string) string { return strings.Trim(e, `"`) }

// ContentETag is the local stores' ETag: S3's for a single-part upload (the
// MD5 of the bytes). Like S3's, it names content, not a write: rewriting the
// same bytes keeps it.
func ContentETag(b []byte) string {
	h := md5.Sum(b) //nolint:gosec // see the import
	return hex.EncodeToString(h[:])
}

func (d DirStore) ETag(_ context.Context, key string) (string, error) {
	b, err := os.ReadFile(d.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return ContentETag(b), nil
}

// PutIf checks and writes under an exclusive flock on a hidden lock file
// beside the object, so racers in any process see one winner.
func (d DirStore) PutIf(_ context.Context, key string, b []byte, etag string) (string, error) {
	etag = NormalizeETag(etag)
	dst := d.path(key)
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "."+filepath.Base(dst)+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", err
	}
	defer lock.Close() // releases the flock
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	cur, err := os.ReadFile(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if etag != "" {
			return "", fmt.Errorf("put %s: %w", key, ErrPrecondition)
		}
	case err != nil:
		return "", err
	case etag == "" || ContentETag(cur) != etag:
		return "", fmt.Errorf("put %s: %w", key, ErrPrecondition)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if etag == "" {
		// Create-only against PutIfAbsent too, which takes no lock.
		if err := os.Link(tmp.Name(), dst); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return "", fmt.Errorf("put %s: %w", key, ErrPrecondition)
			}
			return "", err
		}
	} else if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return ContentETag(b), nil
}

func (s *S3Store) ETag(ctx context.Context, key string) (string, error) {
	h, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		if isNotFound(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	return NormalizeETag(aws.ToString(h.ETag)), nil
}

func (s *S3Store) PutIf(ctx context.Context, key string, b []byte, etag string) (string, error) {
	in := &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: bytes.NewReader(b)}
	if etag = NormalizeETag(etag); etag != "" {
		in.IfMatch = aws.String(etag)
	} else {
		in.IfNoneMatch = aws.String("*")
	}
	out, err := s.client.PutObject(ctx, in)
	if err != nil {
		var ae smithy.APIError
		if errors.As(err, &ae) {
			switch ae.ErrorCode() {
			// 412; 409 is a concurrent conditional write (AWS: retry, i.e. a lost race).
			case "PreconditionFailed", "ConditionalRequestConflict":
				return "", fmt.Errorf("put %s: %w", key, ErrPrecondition)
			case "NoSuchKey": // AWS's answer to If-Match on a missing object (DO Spaces 412s)
				if etag != "" {
					return "", fmt.Errorf("put %s: %w", key, ErrPrecondition)
				}
			}
		}
		return "", err
	}
	return NormalizeETag(aws.ToString(out.ETag)), nil
}

func isNotFound(err error) bool {
	var nf *s3types.NotFound
	var nk *s3types.NoSuchKey
	var ae smithy.APIError
	return errors.As(err, &nf) || errors.As(err, &nk) ||
		(errors.As(err, &ae) && (ae.ErrorCode() == "NotFound" || ae.ErrorCode() == "NoSuchKey"))
}
