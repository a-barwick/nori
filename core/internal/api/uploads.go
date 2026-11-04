package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"nori/internal/state"
)

// upload saves one file. The body is read whole before anything is stored, so a dropped connection leaves nothing, and
// a retry with the same ID is safe. The file goes to quarantined storage and is only ever read as data. The file's name
// travels as `filename` because Agent Runtime's gateway takes a `name` parameter as a resource of its own and refuses it.
func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !state.ValidUploadID(id) {
		writeJSON(w, 400, map[string]string{"error": "An upload needs a UUID id."})
		return
	}
	// The server's default read limit is too short for a large file on a slow connection.
	http.NewResponseController(w).SetReadDeadline(time.Now().Add(3 * time.Minute))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, state.MaxUploadBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSON(w, 413, map[string]string{"error": "A file can be at most 10 MB."})
		return
	}
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "The upload was interrupted, so nothing was saved. Try again.", "retryable": true})
		return
	}
	upload, err := s.Tasks.SaveUpload(r.Context(), id, r.URL.Query().Get("filename"), r.Header.Get("Content-Type"), data)
	if errors.Is(err, state.ErrUploadType) {
		writeJSON(w, 415, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, upload.Attachment)
}

// readUpload returns a saved file. An attachment header and a sandbox policy keep the browser from running anything in
// it, even if a viewer opens the URL directly.
func (s *server) readUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !state.ValidUploadID(id) {
		writeJSON(w, 404, map[string]string{"error": "That file doesn't exist."})
		return
	}
	upload, err := s.Tasks.Store.GetUpload(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	data, err := s.Tasks.Store.GetFile(r.Context(), state.UploadKey(id))
	if errors.Is(err, state.ErrNotFound) {
		writeJSON(w, 410, map[string]string{"error": "This file is no longer available."})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	serveFile(w, upload.Name, upload.MediaType, data)
}

func serveFile(w http.ResponseWriter, name, mediaType string, data []byte) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Write(data)
}

func (s *server) readResult(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !state.ValidUploadID(id) {
		writeJSON(w, 404, map[string]string{"error": "That result doesn't exist."})
		return
	}
	result, err := s.Tasks.Store.GetResult(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (s *server) readResultFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !state.ValidUploadID(id) {
		writeJSON(w, 404, map[string]string{"error": "That result doesn't exist."})
		return
	}
	result, err := s.Tasks.Store.GetResult(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	var data []byte
	switch result.Kind {
	case state.ResultFile:
		data, err = s.Tasks.Store.GetFile(r.Context(), state.ResultKey(id))
	case state.ResultScreenshot:
		data, err = s.Tasks.Store.GetPNG(r.Context(), result.RunID, result.ArtifactID)
		result.Name, result.MediaType = result.Title+".png", "image/png"
	default:
		writeJSON(w, 404, map[string]string{"error": "This result has no file."})
		return
	}
	if errors.Is(err, state.ErrNotFound) {
		writeJSON(w, 410, map[string]string{"error": "This file is no longer available."})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	serveFile(w, result.Name, result.MediaType, data)
}

// delivered records that a browser showed these results.
func (s *server) delivered(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.IDs) == 0 || len(body.IDs) > state.RecentResultLimit {
		writeJSON(w, 400, map[string]string{"error": "Send the IDs of the results shown."})
		return
	}
	for _, id := range body.IDs {
		if !state.ValidUploadID(id) {
			writeJSON(w, 400, map[string]string{"error": "Send the IDs of the results shown."})
			return
		}
	}
	if err := s.Tasks.Store.MarkDelivered(r.Context(), body.IDs); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"recorded": true})
}

var messageID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// seen records the newest message a visible browser showed, so a notification for it isn't sent.
func (s *server) seen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MessageID string `json:"message_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !messageID.MatchString(body.MessageID) {
		writeJSON(w, 400, map[string]string{"error": "Send the ID of the newest message shown."})
		return
	}
	if err := s.Tasks.Store.MarkSeen(r.Context(), body.MessageID); err != nil {
		writeError(w, err)
		return
	}
	position, err := s.Tasks.Store.ReadPosition(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"recorded": true, "read_position": position})
}
