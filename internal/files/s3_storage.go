package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/runixer/laplaced/internal/storage"
)

// S3Options carries the connection parameters for an S3-compatible bucket
// (Yandex Object Storage in this deployment). Kept free of the config package
// so the files layer stays decoupled — the wiring translates config into these.
type S3Options struct {
	Endpoint  string // e.g. https://storage.yandexcloud.net
	Region    string // e.g. ru-central1
	Bucket    string // e.g. laplaced-dev
	AccessKey string
	SecretKey string
}

// S3Storage persists artifacts in an S3-compatible bucket. Object keys match the
// relative keys used by the local backend (user_{scope}/YYYY-MM/{uuid}{ext}), so
// artifact.FilePath is portable across backends with no migration.
type S3Storage struct {
	client *s3.Client
	bucket string
	logger *slog.Logger
}

var _ Storage = (*S3Storage)(nil)

// NewS3Storage builds an S3-backed store with static credentials and a custom
// endpoint (virtual-hosted addressing, which Yandex Object Storage supports).
func NewS3Storage(_ context.Context, opts S3Options, logger *slog.Logger) (*S3Storage, error) {
	if opts.Bucket == "" {
		return nil, fmt.Errorf("s3 storage: bucket is required")
	}
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("s3 storage: endpoint is required")
	}

	awsCfg := aws.Config{
		Region:      opts.Region,
		Credentials: credentials.NewStaticCredentialsProvider(opts.AccessKey, opts.SecretKey, ""),
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(opts.Endpoint)
	})

	return &S3Storage{
		client: client,
		bucket: opts.Bucket,
		logger: logger.With("component", "s3_storage", "bucket", opts.Bucket),
	}, nil
}

// SaveFile buffers the reader (artifacts are size-capped, ≤20 MB), computes the
// SHA256, and uploads under a freshly generated key. The returned key is the
// same relative form the local backend uses.
func (s *S3Storage) SaveFile(
	ctx context.Context,
	userID storage.ScopeID,
	reader io.Reader,
	filename string,
) (*SavedFile, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	sum := sha256.Sum256(data)
	contentHash := hex.EncodeToString(sum[:])

	yearMonth := time.Now().Format("2006-01")
	ext := filepath.Ext(filename)
	// Use forward slashes — this is an object key, not a filesystem path.
	key := fmt.Sprintf("user_%s/%s/%s%s", userID, yearMonth, uuid.New().String(), ext)

	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	}); err != nil {
		return nil, fmt.Errorf("s3 put %q: %w", key, err)
	}

	size := int64(len(data))
	s.logger.Info("file saved",
		"user_id", userID,
		"key", key,
		"size", size,
		"hash", contentHash[:16]+"...",
	)

	return &SavedFile{
		Path:        key,
		ContentHash: contentHash,
		Size:        size,
	}, nil
}

// ReadFile downloads the object at key and returns its full contents.
func (s *S3Storage) ReadFile(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get %q: %w", key, err)
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("s3 read body %q: %w", key, err)
	}
	return data, nil
}

// DeleteFile removes the object at key. A missing key is not an error (S3
// DeleteObject is idempotent); a NoSuchKey is treated as success defensively.
func (s *S3Storage) DeleteFile(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil
		}
		return fmt.Errorf("s3 delete %q: %w", key, err)
	}
	return nil
}
