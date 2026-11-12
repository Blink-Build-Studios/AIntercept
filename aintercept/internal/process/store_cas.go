package process

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ObjectInfo is an object's size and the store's time it was written.
type ObjectInfo struct {
	Key      string
	Size     int64
	Modified time.Time
}

// The primitives the bucketed processor (internal/stream) needs beyond Store:
// a create-only put (the commit log's and the leases' compare-and-swap), the
// store's own time on an object, listings that carry it, and folder listings
// that start after a key, so nothing lists history.

// PutIfAbsent creates key only if it doesn't exist (false: it did).
func (d DirStore) PutIfAbsent(_ context.Context, key string, b []byte) (bool, error) {
	dst := d.path(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	// A hard link fails if the name exists: create-only, and readers never see
	// a partial file.
	if err := os.Link(tmp.Name(), dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (d DirStore) Stat(_ context.Context, key string) (ObjectInfo, error) {
	fi, err := os.Stat(d.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: fi.Size(), Modified: fi.ModTime()}, nil
}

// ListInfo lists every object under prefix (recursively), in key order.
func (d DirStore) ListInfo(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	objs, err := d.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]ObjectInfo, 0, len(objs))
	for _, o := range objs {
		fi, err := os.Stat(d.path(o.Key))
		if err != nil {
			continue // removed while listing
		}
		out = append(out, ObjectInfo{Key: o.Key, Size: fi.Size(), Modified: fi.ModTime()})
	}
	return out, nil
}

// ListDirsAfter is ListDirs, only the folders sorting after `after`.
func (d DirStore) ListDirsAfter(ctx context.Context, prefix, after string) ([]string, error) {
	all, err := d.ListDirs(ctx, prefix)
	if err != nil {
		return nil, err
	}
	sort.Strings(all)
	i := sort.SearchStrings(all, after)
	for i < len(all) && all[i] <= after {
		i++
	}
	return all[i:], nil
}

func (s *S3Store) PutIfAbsent(ctx context.Context, key string, b []byte) (bool, error) {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: bytes.NewReader(b), IfNoneMatch: aws.String("*")})
	if err != nil {
		var ae smithy.APIError
		if errors.As(err, &ae) && (ae.ErrorCode() == "PreconditionFailed" || ae.ErrorCode() == "ConditionalRequestConflict") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *S3Store) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	h, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		var nf *s3types.NotFound
		var ae smithy.APIError
		if errors.As(err, &nf) || (errors.As(err, &ae) && (ae.ErrorCode() == "NotFound" || ae.ErrorCode() == "NoSuchKey")) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: aws.ToInt64(h.ContentLength), Modified: aws.ToTime(h.LastModified)}, nil
}

func (s *S3Store) ListInfo(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &prefix})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			out = append(out, ObjectInfo{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size), Modified: aws.ToTime(o.LastModified)})
		}
	}
	return out, nil
}

func (s *S3Store) ListDirsAfter(ctx context.Context, prefix, after string) ([]string, error) {
	in := &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &prefix, Delimiter: aws.String("/")}
	if after != "" {
		// StartAfter compares whole keys. A folder named by `after` itself
		// (ending in "/") is skipped with everything in it; a bare name keeps
		// the folder that starts with it.
		sa := after
		if strings.HasSuffix(after, "/") {
			sa += "\uffff"
		}
		in.StartAfter = &sa
	}
	var out []string
	p := s3.NewListObjectsV2Paginator(s.client, in)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, cp := range page.CommonPrefixes {
			if k := aws.ToString(cp.Prefix); k > after {
				out = append(out, k)
			}
		}
	}
	return out, nil
}

// CreateBucketIfMissing makes the bucket (tests against an emulator).
func (s *S3Store) CreateBucketIfMissing(ctx context.Context) error {
	_, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &s.bucket})
	var owned *s3types.BucketAlreadyOwnedByYou
	var exists *s3types.BucketAlreadyExists
	if err == nil || errors.As(err, &owned) || errors.As(err, &exists) {
		return nil
	}
	return err
}
