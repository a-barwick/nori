package api

import "nori/internal/state"

type uploadAcceptView struct {
	MediaTypes []string `json:"media_types"`
	MaxBytes   int      `json:"max_bytes"`
	Extensions []string `json:"extensions,omitempty"`
}

// uploadAccept declares what the uploader takes, so a client never suggests an unsupported fallback.
func uploadAccept() uploadAcceptView {
	return uploadAcceptView{MediaTypes: state.UploadMediaTypes, MaxBytes: state.MaxUploadBytes,
		Extensions: []string{".png", ".jpg", ".jpeg", ".webp", ".pdf", ".csv", ".ics", ".txt", ".md"}}
}
