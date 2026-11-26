package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"nori/internal/state"
	"nori/internal/users"
)

type Store struct {
	api            *sdk.Client
	bucket, prefix string
	legacy         bool
}

var _ state.ArtifactStore = (*Store)(nil)

type Client struct{ raw *sdk.Client }

func OpenClient(ctx context.Context, region string) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return &Client{raw: sdk.NewFromConfig(cfg)}, nil
}

func (c *Client) ForUser(bucket string, user users.User) (*Store, error) {
	return New(c.raw, bucket, user)
}

func Open(ctx context.Context, region, bucket string, user users.User) (*Store, error) {
	if err := user.Validate(); err != nil {
		return nil, err
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return New(sdk.NewFromConfig(cfg), bucket, user)
}
func New(api *sdk.Client, bucket string, user users.User) (*Store, error) {
	if err := user.Validate(); err != nil {
		return nil, err
	}
	return &Store{api: api, bucket: bucket, prefix: "artifacts/" + user.FilePrefix(), legacy: user.ID == users.LegacyID}, nil
}
func code(err error, want string) bool {
	var e smithy.APIError
	return errors.As(err, &e) && e.ErrorCode() == want
}
func (s *Store) put(ctx context.Context, key, media string, data []byte) error {
	if s.bucket == "" {
		return errors.New("ARTIFACT_BUCKET must be configured")
	}
	_, err := s.api.PutObject(ctx, &sdk.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + key), Body: bytes.NewReader(data), ContentType: aws.String(media), CacheControl: aws.String("private, no-store"), IfNoneMatch: aws.String("*")})
	if code(err, "PreconditionFailed") {
		return state.ErrFileExists
	}
	return err
}
func (s *Store) PutPNG(ctx context.Context, run, id string, data []byte) error {
	return s.put(ctx, run+"/"+id+".png", "image/png", data)
}
func (s *Store) PutFile(ctx context.Context, key, media string, data []byte) error {
	return s.put(ctx, key, media, data)
}
func (s *Store) get(ctx context.Context, key string, artifact bool) ([]byte, error) {
	out, err := s.api.GetObject(ctx, &sdk.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + key)})
	if code(err, "NoSuchKey") {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	if artifact {
		return state.ReadArtifact(out.Body)
	}
	return state.ReadFile(out.Body)
}
func (s *Store) GetPNG(ctx context.Context, run, id string) ([]byte, error) {
	return s.get(ctx, run+"/"+id+".png", true)
}
func (s *Store) GetFile(ctx context.Context, key string) ([]byte, error) {
	return s.get(ctx, key, false)
}
func (s *Store) DeleteFile(ctx context.Context, key string) error {
	_, err := s.api.DeleteObject(ctx, &sdk.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + key)})
	return err
}

func (s *Store) purgeObjectAllowed(key string) (bool, error) {
	if !strings.HasPrefix(key, s.prefix) {
		return false, errors.New("object listing returned a key outside the user's prefix")
	}
	return !s.legacy || !strings.HasPrefix(key, s.prefix+"users/"), nil
}

func (s *Store) Purge(ctx context.Context) error {
	if s.bucket == "" {
		return errors.New("ARTIFACT_BUCKET must be configured")
	}
	pages := sdk.NewListObjectVersionsPaginator(s.api, &sdk.ListObjectVersionsInput{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(s.prefix),
	})
	var versions, markers []types.ObjectIdentifier
	appendObject := func(objects *[]types.ObjectIdentifier, key, version *string) error {
		allowed, err := s.purgeObjectAllowed(aws.ToString(key))
		if err != nil || !allowed {
			return err
		}
		if aws.ToString(version) == "" {
			return errors.New("object listing returned a missing version ID")
		}
		*objects = append(*objects, types.ObjectIdentifier{Key: key, VersionId: version})
		return nil
	}
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, version := range page.Versions {
			if err := appendObject(&versions, version.Key, version.VersionId); err != nil {
				return err
			}
		}
		for _, marker := range page.DeleteMarkers {
			if err := appendObject(&markers, marker.Key, marker.VersionId); err != nil {
				return err
			}
		}
	}
	for _, objects := range [][]types.ObjectIdentifier{versions, markers} {
		for len(objects) > 0 {
			count := min(len(objects), 1000)
			result, err := s.api.DeleteObjects(ctx, &sdk.DeleteObjectsInput{
				Bucket: aws.String(s.bucket),
				Delete: &types.Delete{Objects: objects[:count], Quiet: aws.Bool(true)},
			})
			if err != nil {
				return err
			}
			if len(result.Errors) > 0 {
				return fmt.Errorf("artifact deletion failed: %s", aws.ToString(result.Errors[0].Code))
			}
			objects = objects[count:]
		}
	}
	return nil
}
func (s *Store) Close() error { return nil }
