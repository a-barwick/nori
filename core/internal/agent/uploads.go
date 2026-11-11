package agent

import (
	"context"
	"errors"
	"strings"

	"nori/internal/computer"
	"nori/internal/prompts"
	"nori/internal/state"
)

var uploadNote = prompts.Runtime("upload-note", nil)

// taskUpload finds an upload this task was given. A task can't read a file by guessing its ID.
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

// uploadOpen copies an upload into the task's folder on the computer, where shell can read it. PDF parsing and analysis
// run there, away from the worker's credentials.
func (s *Service) uploadOpen(ctx context.Context, run string, c *computer.Client, id string) (map[string]any, error) {
	attachment, err := s.taskUpload(ctx, run, id)
	if err != nil {
		return nil, err
	}
	data, err := s.uploadBytes(ctx, attachment)
	if err != nil {
		return nil, err
	}
	path, err := c.PutFile(ctx, state.ComputerFileName(attachment), data)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"upload_id": id, "path": path, "name": attachment.Name, "media_type": attachment.MediaType, "size": attachment.Size, "note": uploadNote,
	}, nil
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

// TaskUpload returns an upload the task was given and its bytes, for registry tools that read attachments.
func (s *Service) TaskUpload(ctx context.Context, run, id string) (state.Attachment, []byte, error) {
	attachment, err := s.taskUpload(ctx, run, id)
	if err != nil {
		return attachment, nil, err
	}
	data, err := s.uploadBytes(ctx, attachment)
	return attachment, data, err
}
