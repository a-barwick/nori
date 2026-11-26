package s3

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	"nori/internal/users"
)

type testObject struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId"`
}

type resetHTTPClient struct {
	t            *testing.T
	listRequests int
	deletes      [][]testObject
	failDeletion bool
}

func (c *resetHTTPClient) Do(request *http.Request) (*http.Response, error) {
	c.t.Helper()
	var body string
	switch request.Method {
	case http.MethodGet:
		if request.Header.Get("x-goog-interop-list-objects-format") != "enabled" {
			c.t.Error("Google Storage version listing lacks the interoperability header")
		}
		if request.URL.Query().Get("prefix") != "artifacts/" {
			c.t.Error("listing used the wrong prefix")
		}
		c.listRequests++
		if c.listRequests == 1 {
			body = `<ListVersionsResult><IsTruncated>true</IsTruncated><NextKeyMarker>artifacts/uploads/one</NextKeyMarker><NextVersionIdMarker>v1</NextVersionIdMarker><Version><Key>artifacts/uploads/one</Key><VersionId>v1</VersionId></Version><Version><Key>artifacts/users/other/uploads/keep</Key><VersionId>v2</VersionId></Version><DeleteMarker><Key>artifacts/uploads/one</Key><VersionId>marker</VersionId></DeleteMarker></ListVersionsResult>`
		} else {
			if request.URL.Query().Get("key-marker") != "artifacts/uploads/one" || request.URL.Query().Get("version-id-marker") != "v1" {
				c.t.Error("listing did not follow both pagination markers")
			}
			body = `<ListVersionsResult><IsTruncated>false</IsTruncated><Version><Key>artifacts/results/two</Key><VersionId>v3</VersionId></Version></ListVersionsResult>`
		}
	case http.MethodPost:
		var deletion struct {
			Objects []testObject `xml:"Object"`
		}
		if err := xml.NewDecoder(request.Body).Decode(&deletion); err != nil {
			return nil, err
		}
		c.deletes = append(c.deletes, deletion.Objects)
		body = `<DeleteResult></DeleteResult>`
		if c.failDeletion {
			body = `<DeleteResult><Error><Key>artifacts/uploads/one</Key><Code>AccessDenied</Code></Error></DeleteResult>`
		}
	default:
		return nil, errors.New("unexpected HTTP method")
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func TestPurgeLegacyVersionsAndIsolation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "deletion failure"}[fail], func(t *testing.T) {
			httpClient := &resetHTTPClient{t: t, failDeletion: fail}
			client := sdk.New(sdk.Options{
				BaseEndpoint: aws.String("https://storage.googleapis.com"),
				Region:       "us-central1", Credentials: aws.AnonymousCredentials{}, HTTPClient: httpClient, UsePathStyle: true,
			})
			store, err := New(client, "test-bucket", users.User{ID: users.LegacyID, Email: "owner@example.com", TimeZone: "UTC"})
			if err != nil {
				t.Fatal(err)
			}
			err = store.Purge(context.Background())
			if (err != nil) != fail {
				t.Fatalf("Purge() error = %v, failDeletion = %v", err, fail)
			}
			if httpClient.listRequests != 2 {
				t.Fatalf("got %d listing requests", httpClient.listRequests)
			}
			if len(httpClient.deletes) == 0 || len(httpClient.deletes[0]) != 2 {
				t.Fatalf("unexpected deletion batches: %#v", httpClient.deletes)
			}
			if httpClient.deletes[0][0].VersionID != "v1" || httpClient.deletes[0][1].VersionID != "v3" {
				t.Fatalf("wrong versions selected: %#v", httpClient.deletes[0])
			}
			if fail {
				if len(httpClient.deletes) != 1 {
					t.Fatalf("delete marker was removed after version deletion failed: %#v", httpClient.deletes)
				}
			} else if len(httpClient.deletes) != 2 || len(httpClient.deletes[1]) != 1 || httpClient.deletes[1][0].VersionID != "marker" {
				t.Fatalf("delete marker was not removed last: %#v", httpClient.deletes)
			}
		})
	}
}
