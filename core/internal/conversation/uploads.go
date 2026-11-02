package conversation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"nori/internal/state"
)

// SaveUpload stores a file once. The bytes go first, so a saved record always means a complete file. A repeat with the
// same bytes returns the same record, and different bytes under the same ID are refused.
func (t *Tasks) SaveUpload(ctx context.Context, id, name, declared string, data []byte) (state.Upload, error) {
	name = state.CleanUploadName(name)
	mediaType, err := state.SniffUpload(name, declared, data)
	if err != nil {
		return state.Upload{}, err
	}
	sum := sha256.Sum256(data)
	upload := state.Upload{
		Attachment: state.Attachment{ID: id, Name: name, MediaType: mediaType, Size: int64(len(data))},
		SHA256:     hex.EncodeToString(sum[:]), CreatedAt: time.Now().UTC(),
	}
	err = t.Store.PutFile(ctx, state.UploadKey(id), mediaType, data)
	if errors.Is(err, state.ErrFileExists) {
		saved, readErr := t.Store.GetFile(ctx, state.UploadKey(id))
		if readErr != nil {
			return state.Upload{}, readErr
		}
		if !bytes.Equal(saved, data) {
			return state.Upload{}, state.ErrDuplicate
		}
	} else if err != nil {
		return state.Upload{}, err
	}
	return t.Store.CreateUpload(ctx, upload)
}
