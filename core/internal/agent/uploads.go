package agent

import (
	"context"
	"errors"
	"strings"

	"nori/internal/prompts"
	"nori/internal/state"
)

var uploadNote = prompts.Runtime("upload-note", nil)

func (s *Service) taskUpload(ctx context.Context, run, id string) (state.Attachment, error) {
	record, err := s.Store.GetRun(ctx, run)
	if err != nil {
		return state.Attachment{}, err
	}
	for _, attachment := range record.Uploads {
		if attachment.ID == id {
			return attachment, nil
		}
	}
	return state.Attachment{}, errors.New(prompts.Runtime("tool-this-task-wasn-t-given-a-file-with-that-upload-id", nil))
}

func (s *Service) uploadBytes(ctx context.Context, attachment state.Attachment) ([]byte, error) {
	data, err := s.Store.GetFile(ctx, state.UploadKey(attachment.ID))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New(prompts.Runtime("tool-the-stored-file-is-unavailable-so-its-contents-weren-t-read", nil))
	}
	return data, nil
}

var inspectUploadInstruction = prompts.Runtime("upload-inspect", nil)

func (s *Service) uploadInspect(ctx context.Context, run, id, question string) (map[string]any, error) {
	attachment, err := s.taskUpload(ctx, run, id)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(attachment.MediaType, "image/") {
		return nil, errors.New(prompts.Runtime("tool-upload-inspect-reads-images-use-upload-open-for-a-pdf-or-csv", nil))
	}
	data, err := s.uploadBytes(ctx, attachment)
	if err != nil {
		return nil, err
	}
	result, err := s.inspect(ctx, "upload_id", id, inspectUploadInstruction, question, data, attachment.MediaType)
	if result != nil {
		result["note"] = uploadNote
	}
	return result, err
}

func (s *Service) TaskUpload(ctx context.Context, run, id string) (state.Attachment, []byte, error) {
	attachment, err := s.taskUpload(ctx, run, id)
	if err != nil {
		return attachment, nil, err
	}
	data, err := s.uploadBytes(ctx, attachment)
	return attachment, data, err
}
