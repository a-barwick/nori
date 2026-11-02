package computerd

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"

	"github.com/google/uuid"
	"nori/internal/httpjson"
)

const (
	// maxFile matches the size limit on uploads and saved results.
	maxFile = 10 << 20
	// maxFileRequest leaves room for base64 and the rest of the JSON.
	maxFileRequest = maxFile/3*4 + 4096
)

var fileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,159}$`)

// putFile writes a file into the task's uploads folder, whole or not at all. A repeat replaces it with the same bytes,
// so a retried call is safe. The copy is read-only, so a command can't change the original by accident.
func (s *server) putFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"task_id"`
		RunID  string `json:"run_id"`
		Name   string `json:"name"`
		Data   []byte `json:"data"`
	}
	if err := httpjson.Decode(w, r, &req, maxFileRequest); err != nil {
		http.Error(w, "invalid file request", 400)
		return
	}
	if !runPattern.MatchString(req.RunID) || !fileName.MatchString(req.Name) || len(req.Data) > maxFile {
		http.Error(w, "run_id and a safe file name required", 400)
		return
	}
	if req.TaskID == "" {
		req.TaskID = req.RunID
	}
	if !runPattern.MatchString(req.TaskID) {
		http.Error(w, "invalid task_id", 400)
		return
	}
	_, finish, err := s.begin(r.Context(), req.RunID)
	if err != nil {
		http.Error(w, "run canceled", 409)
		return
	}
	defer finish()
	root, closeRoot, err := s.openTaskRoot(req.TaskID)
	if err != nil {
		http.Error(w, "task directory unavailable", 500)
		return
	}
	defer closeRoot()
	if err = root.Mkdir("uploads", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		http.Error(w, "uploads folder unavailable", 500)
		return
	}
	temporary := filepath.Join("uploads", "."+uuid.NewString()+".tmp")
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		http.Error(w, "file could not be written", 500)
		return
	}
	defer root.Remove(temporary)
	_, err = file.Write(req.Data)
	err = errors.Join(err, file.Close())
	if err == nil {
		err = root.Rename(temporary, filepath.Join("uploads", req.Name))
	}
	if err != nil {
		http.Error(w, "file could not be written", 500)
		return
	}
	respond(w, map[string]any{"path": path.Join("uploads", req.Name), "bytes": len(req.Data)})
}

// getFile reads a file from the task's folder. os.Root refuses a path or link that leaves the folder.
func (s *server) getFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"task_id"`
		RunID  string `json:"run_id"`
		Path   string `json:"path"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.TaskID == "" {
		req.TaskID = req.RunID
	}
	if !runPattern.MatchString(req.RunID) || !runPattern.MatchString(req.TaskID) {
		http.Error(w, "run_id and a relative path required", 400)
		return
	}
	// A command's output names full paths, so a full path inside the task's folder means the same file.
	if relative, err := filepath.Rel(filepath.Join(s.tasks, req.TaskID), req.Path); path.IsAbs(req.Path) && err == nil {
		req.Path = relative
	}
	if req.Path == "" || !fs.ValidPath(path.Clean(req.Path)) {
		http.Error(w, "a path inside the task's folder required", 400)
		return
	}
	_, finish, err := s.begin(r.Context(), req.RunID)
	if err != nil {
		http.Error(w, "run canceled", 409)
		return
	}
	defer finish()
	root, closeRoot, err := s.openTaskRoot(req.TaskID)
	if err != nil {
		http.Error(w, "task directory unavailable", 500)
		return
	}
	defer closeRoot()
	info, err := root.Stat(path.Clean(req.Path))
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "no file at that path", 404)
		return
	}
	if info.Size() > maxFile {
		http.Error(w, "file is larger than 10 MB", 413)
		return
	}
	file, err := root.Open(path.Clean(req.Path))
	if err != nil {
		http.Error(w, "file could not be read", 500)
		return
	}
	defer file.Close()
	// The limit holds even if a command is still writing to the file.
	data, err := io.ReadAll(io.LimitReader(file, maxFile+1))
	if err != nil {
		http.Error(w, "file could not be read", 500)
		return
	}
	if len(data) > maxFile {
		http.Error(w, "file is larger than 10 MB", 413)
		return
	}
	respond(w, map[string]any{"name": path.Base(req.Path), "data": data})
}

func (s *server) openTaskRoot(taskID string) (*os.Root, func(), error) {
	root, err := os.OpenRoot(s.tasks)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	if err = root.Mkdir(taskID, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, nil, err
	}
	task, err := root.OpenRoot(taskID)
	if err != nil {
		return nil, nil, err
	}
	return task, func() { _ = task.Close() }, nil
}
