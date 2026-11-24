package conversation

import (
	"context"

	"nori/internal/state"
)

func (t *Tasks) SaveUpload(ctx context.Context, id, name, declared string, data []byte) (state.Upload, error) {
	upload, err := state.SaveUploadBytes(ctx, t.Store, state.UploadKey(id), id, name, declared, data)
	if err != nil {
		return state.Upload{}, err
	}
	return t.Store.CreateUpload(ctx, upload)
}
