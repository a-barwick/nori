package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"nori/internal/state"
)

type uploadFile struct {
	name   string
	body   []byte
	digest string
}

func readFiles(paths []string) ([]uploadFile, error) {
	if len(paths) > state.MaxAttachments {
		return nil, fmt.Errorf("a message accepts up to %d attachments", state.MaxAttachments)
	}
	files := make([]uploadFile, 0, len(paths))
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(file, state.MaxUploadBytes+1))
		file.Close()
		if err != nil {
			return nil, err
		}
		if len(data) == 0 || len(data) > state.MaxUploadBytes {
			return nil, fmt.Errorf("%s must be between 1 byte and %d bytes", filepath.Base(path), state.MaxUploadBytes)
		}
		digest := sha256.Sum256(data)
		files = append(files, uploadFile{filepath.Base(path), data, hex.EncodeToString(digest[:])})
	}
	return files, nil
}

func (a *cli) upload(ctx context.Context, conversation, id string, file uploadFile, output any) error {
	query := url.Values{"id": {id}, "filename": {file.name}}
	path := conversationPath(conversation) + "/uploads?" + query.Encode()
	return a.client.callBytes(ctx, "POST", path, "application/octet-stream", file.body, output)
}

func (a *cli) uploadCommand(ctx context.Context, args []string) error {
	flags := a.flags("upload")
	explicit := flags.String("request-id", "", "stable upload UUID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return errors.New("upload requires a conversation ID and file")
	}
	conversation, path := flags.Arg(0), flags.Arg(1)
	files, err := readFiles([]string{path})
	if err != nil {
		return err
	}
	file := files[0]
	id, done, err := journalID(*explicit, a.scope(), []string{"upload", conversation, file.name, file.digest})
	if err != nil {
		return err
	}
	var attachment state.Attachment
	if err := a.upload(ctx, conversation, id, file, &attachment); err != nil {
		return err
	}
	if a.machine {
		err = a.emit(attachment)
	} else {
		_, err = fmt.Fprintf(a.out, "%s\t%s\n", attachment.ID, attachment.Name)
	}
	if err == nil {
		done()
	}
	return err
}

func (a *cli) result(ctx context.Context, args []string) error {
	flags := a.flags("result")
	file := flags.String("out", "", "download a result file to this path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("result requires a result ID")
	}
	path := "/v1/results/" + url.PathEscape(flags.Arg(0))
	if *file != "" {
		if err := a.download(ctx, path+"/file", *file); err != nil {
			return err
		}
		if a.machine {
			return a.emit(map[string]string{"path": *file, "id": flags.Arg(0)})
		}
		_, err := fmt.Fprintln(a.out, *file)
		return err
	}
	var result json.RawMessage
	if err := a.client.call(ctx, "GET", path, nil, &result); err != nil {
		return err
	}
	if a.machine {
		return a.emit(result)
	}
	var view state.Result
	if err := json.Unmarshal(result, &view); err != nil {
		return err
	}
	_, err := fmt.Fprintf(a.out, "%s\n%s\n", view.Title, view.Text)
	return err
}

func (a *cli) download(ctx context.Context, path, target string) error {
	response, err := a.client.do(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	file, err := os.CreateTemp(filepath.Dir(target), ".nori-download-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, copyErr := io.Copy(file, response.Body)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), target)
}
