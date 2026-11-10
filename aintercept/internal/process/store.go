package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Object is one listed file.
type Object struct {
	Key  string
	Size int64
}

// Store is where raw partitions are read from and shaped tables written to: a
// local directory or an S3-compatible bucket. Keys are slash-separated.
type Store interface {
	List(ctx context.Context, prefix string) ([]Object, error)
	// ListDirs returns the folders directly under prefix (which ends in "/"),
	// each as a prefix ending in "/".
	ListDirs(ctx context.Context, prefix string) ([]string, error)
	// ListAfter returns the objects directly under prefix (a folder, ending in
	// "/") whose keys sort after `after` (empty: all), in key order.
	ListAfter(ctx context.Context, prefix, after string) ([]Object, error)
	Get(ctx context.Context, key string) ([]byte, error)
	// Put writes the whole object at once; readers never see a partial one.
	Put(ctx context.Context, key string, b []byte) error
	Delete(ctx context.Context, key string) error
}

// ErrNotFound is returned by Get for a missing key.
var ErrNotFound = errors.New("not found")

// DirStore keeps objects as files under Root.
type DirStore struct{ Root string }

func (d DirStore) path(key string) string { return filepath.Join(d.Root, filepath.FromSlash(key)) }

func (d DirStore) List(_ context.Context, prefix string) ([]Object, error) {
	// Walk from the deepest directory the prefix names, then filter on the rest.
	dir := prefix
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		dir = dir[:i]
	} else {
		dir = ""
	}
	var out []Object
	err := filepath.WalkDir(d.path(dir), func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") { // dot files are in-progress writes
			return nil
		}
		rel, err := filepath.Rel(d.Root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil // removed while listing
		}
		out = append(out, Object{Key: key, Size: info.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, err
}

func (d DirStore) ListDirs(_ context.Context, prefix string) ([]string, error) {
	es, err := os.ReadDir(d.path(prefix))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var out []string
	for _, e := range es {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, prefix+e.Name()+"/")
		}
	}
	return out, err
}

func (d DirStore) ListAfter(_ context.Context, prefix, after string) ([]Object, error) {
	es, err := os.ReadDir(d.path(prefix)) // sorted by name
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Object
	for _, e := range es {
		key := prefix + e.Name()
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || key <= after {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // removed while listing
		}
		out = append(out, Object{Key: key, Size: info.Size()})
	}
	return out, nil
}

func (d DirStore) Get(_ context.Context, key string) ([]byte, error) {
	b, err := os.ReadFile(d.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

func (d DirStore) Put(_ context.Context, key string, b []byte) error {
	dst := d.path(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func (d DirStore) Delete(_ context.Context, key string) error {
	err := os.Remove(d.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// S3Config addresses a bucket: AWS (default credential chain) or an
// S3-compatible store (Endpoint plus static keys).
type S3Config struct {
	Bucket, Region, Endpoint     string
	AccessKeyID, SecretAccessKey string
	ForcePathStyle               bool
}

// S3Store keeps objects in a bucket.
type S3Store struct {
	client *s3.Client
	bucket string
}

func NewS3Store(ctx context.Context, cfg S3Config) (*S3Store, error) {
	if cfg.Bucket == "" || cfg.Region == "" {
		return nil, fmt.Errorf("s3 store: bucket and region are required")
	}
	opts := []func(*config.LoadOptions) error{config.WithRegion(cfg.Region)}
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3 store: load aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.ForcePathStyle
		// S3-compatible stores (DO Spaces) return no checksums; reads are gzip and
		// Parquet, which check themselves. Don't log it on every read.
		o.DisableLogOutputChecksumValidationSkipped = true
	})
	return &S3Store{client: client, bucket: cfg.Bucket}, nil
}

func (s *S3Store) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &prefix})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			out = append(out, Object{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size)})
		}
	}
	return out, nil
}

func (s *S3Store) ListDirs(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &prefix, Delimiter: aws.String("/")})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, cp := range page.CommonPrefixes {
			out = append(out, aws.ToString(cp.Prefix))
		}
	}
	return out, nil
}

func (s *S3Store) ListAfter(ctx context.Context, prefix, after string) ([]Object, error) {
	in := &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &prefix, Delimiter: aws.String("/")}
	if after != "" {
		in.StartAfter = &after
	}
	var out []Object
	p := s3.NewListObjectsV2Paginator(s.client, in)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			out = append(out, Object{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size)})
		}
	}
	return out, nil
}

func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	r, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		var nk *s3types.NoSuchKey
		if errors.As(err, &nk) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func (s *S3Store) Put(ctx context.Context, key string, b []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: bytes.NewReader(b)})
	return err
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	return err
}
