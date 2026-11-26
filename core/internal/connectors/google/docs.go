package google

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"nori/internal/invoke"
)

const docsBase = "https://docs.googleapis.com/v1/documents/"

func prepareDocReplacement(ctx context.Context, client *http.Client, id string, args map[string]any, spec map[string]string) error {
	find, hasFind := args["find"].(string)
	replace, hasReplace := args["replace"].(string)
	if !hasFind && !hasReplace {
		return nil
	}
	if !hasFind || find == "" || !hasReplace {
		return fmt.Errorf("%w: provide both text to find and its replacement", invoke.ErrInvalidArguments)
	}
	if spec["name"] != "" || spec["content_hash"] != "" || spec["upload_id"] != "" {
		return fmt.Errorf("%w: make text replacements and other document changes separately", invoke.ErrInvalidArguments)
	}
	var doc struct {
		RevisionID string `json:"revisionId"`
	}
	if err := jsonRequest(ctx, client, "GET", docsBase+pathID(id)+"?fields=revisionId", nil, nil, &doc); err != nil {
		return err
	}
	if doc.RevisionID == "" {
		return errors.New("Google did not return a document revision")
	}
	spec["find"], spec["replace"], spec["doc_revision"], spec["targeted_update"] = find, replace, doc.RevisionID, "true"
	return nil
}

func executeDocReplacement(ctx context.Context, client *http.Client, id string, spec map[string]string) (map[string]any, error) {
	body := map[string]any{
		"requests":     []any{map[string]any{"replaceAllText": map[string]any{"containsText": map[string]any{"text": spec["find"], "matchCase": true}, "replaceText": spec["replace"]}}},
		"writeControl": map[string]string{"requiredRevisionId": spec["doc_revision"]},
	}
	var result map[string]any
	err := jsonRequest(ctx, client, "POST", docsBase+pathID(id)+":batchUpdate", body, nil, &result)
	return result, err
}
