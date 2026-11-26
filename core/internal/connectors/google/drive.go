package google

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/ledongthuc/pdf"
	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/state"
)

const driveBase = "https://www.googleapis.com/drive/v3"
const driveUploadBase = "https://www.googleapis.com/upload/drive/v3"
const mimeDoc = "application/vnd.google-apps.document"
const mimeSheet = "application/vnd.google-apps.spreadsheet"
const mimePDF = "application/pdf"
const maxDriveBytes = 10 << 20
const maxDriveText = 64 << 10

type driveAdapter struct{}

type driveFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	Version      string `json:"version"`
	ModifiedTime string `json:"modifiedTime"`
	WebViewLink  string `json:"webViewLink"`
	Trashed      bool   `json:"trashed"`
	Capabilities struct {
		CanEdit  bool `json:"canEdit"`
		CanTrash bool `json:"canTrash"`
	} `json:"capabilities"`
}

const driveFields = "id,name,mimeType,version,modifiedTime,webViewLink,trashed,capabilities(canEdit,canTrash)"

func supportedFile(mime string) bool { return mime == mimeDoc || mime == mimeSheet || mime == mimePDF }

func (g Integration) DriveAccount(ctx context.Context, client *http.Client) (string, error) {
	var info struct {
		User struct {
			Email string `json:"emailAddress"`
		} `json:"user"`
	}
	err := jsonRequest(ctx, client, "GET", driveBase+"/about?fields=user(emailAddress)", nil, nil, &info)
	if err != nil {
		return "", err
	}
	if info.User.Email == "" {
		return "", errors.New("Google Drive did not identify its account")
	}
	return info.User.Email, nil
}

func listDriveFiles(ctx context.Context, client *http.Client, search string) ([]driveFile, error) {
	q := `trashed = false and (mimeType = '` + mimeDoc + `' or mimeType = '` + mimeSheet + `' or mimeType = '` + mimePDF + `')`
	if search != "" {
		escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(search)
		q += ` and name contains '` + escaped + `'`
	}
	files := []driveFile{}
	token := ""
	for pages := 0; pages < 10; pages++ {
		params := url.Values{"q": {q}, "pageSize": {"100"}, "fields": {"nextPageToken,files(" + driveFields + ")"}, "orderBy": {"modifiedTime desc"}, "supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"}}
		if token != "" {
			params.Set("pageToken", token)
		}
		var list struct {
			Files         []driveFile `json:"files"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := jsonRequest(ctx, client, "GET", driveBase+"/files?"+params.Encode(), nil, nil, &list); err != nil {
			return nil, err
		}
		for _, file := range list.Files {
			if supportedFile(file.MimeType) && !file.Trashed {
				files = append(files, file)
			}
		}
		token = list.NextPageToken
		if token == "" || len(files) >= 1000 {
			break
		}
	}
	return files, nil
}

func (g Integration) DriveResources(ctx context.Context, client *http.Client) ([]connections.Resource, error) {
	files, err := listDriveFiles(ctx, client, "")
	if err != nil {
		return nil, err
	}
	out := make([]connections.Resource, 0, len(files))
	for _, f := range files {
		out = append(out, connections.Resource{Ref: connections.ResourceRef{Kind: "drive_file", ProviderID: f.ID, ProviderRevision: f.Version}, Title: f.Name, Kind: "drive_file", Writable: f.Capabilities.CanEdit})
	}
	return out, nil
}

func getDriveFile(ctx context.Context, client *http.Client, id string) (driveFile, string, error) {
	var file driveFile
	if id == "" {
		return file, "", fmt.Errorf("%w: file ID is required", invoke.ErrInvalidArguments)
	}
	params := url.Values{"fields": {driveFields}, "supportsAllDrives": {"true"}}
	data, headers, err := request(ctx, client, "GET", driveBase+"/files/"+pathID(id)+"?"+params.Encode(), nil, nil, 2<<20)
	if err != nil {
		return file, "", err
	}
	if err = json.Unmarshal(data, &file); err != nil {
		return file, "", err
	}
	if !supportedFile(file.MimeType) || file.Trashed {
		return file, "", fmt.Errorf("%w: select a Google Doc, Google Sheet or PDF", invoke.ErrInvalidArguments)
	}
	return file, headers.Get("ETag"), nil
}

func stringArg(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return value
}
func digestBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func pdfUpload(ctx context.Context, call invoke.Call, id string) (string, []byte, error) {
	if call.Upload == nil || id == "" {
		return "", nil, fmt.Errorf("%w: a PDF attachment is required", invoke.ErrInvalidArguments)
	}
	name, media, data, err := call.Upload(ctx, id)
	if err != nil {
		return "", nil, err
	}
	if media != mimePDF || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return "", nil, fmt.Errorf("%w: select a PDF attachment", invoke.ErrInvalidArguments)
	}
	if len(data) > maxDriveBytes {
		return "", nil, fmt.Errorf("%w: PDF exceeds the file size limit", invoke.ErrInvalidArguments)
	}
	return name, data, nil
}

func (driveAdapter) Prepare(ctx context.Context, call invoke.Call) (invoke.Prepared, error) {
	_, op, _ := strings.Cut(call.Exec.Binding.Target, "/")
	client, err := clientFor(call)
	if err != nil {
		return invoke.Prepared{}, err
	}
	id := invoke.String(call.Args, "file_id")
	switch op {
	case "files.list":
		return invoke.Prepared{Summary: "List selected Google Drive files", Effect: extensions.EffectRead}, nil
	case "files.read":
		return invoke.Prepared{Summary: "Read a selected Google Drive file", Effect: extensions.EffectRead, Resources: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "drive_file", ProviderID: id}}}, nil
	case "files.create", "files.update", "files.trash":
		spec := map[string]string{"file_id": id}
		var file driveFile
		etag := ""
		if op != "files.create" {
			file, etag, err = getDriveFile(ctx, client, id)
			if err != nil {
				return invoke.Prepared{}, err
			}
			if op == "files.trash" && !file.Capabilities.CanTrash {
				return invoke.Prepared{}, errors.New("this account cannot move that file to Trash")
			}
			if op == "files.update" && !file.Capabilities.CanEdit {
				return invoke.Prepared{}, errors.New("this account cannot edit that file")
			}
			if file.Version == "" {
				return invoke.Prepared{}, errors.New("Google did not provide a file revision")
			}
			spec["mime_type"] = file.MimeType
			spec["original_version"] = file.Version
			spec["etag"] = etag
		} else {
			switch invoke.String(call.Args, "format") {
			case "google_doc":
				spec["mime_type"] = mimeDoc
			case "google_sheet":
				spec["mime_type"] = mimeSheet
			case "pdf":
				spec["mime_type"] = mimePDF
			default:
				return invoke.Prepared{}, fmt.Errorf("%w: invalid file format", invoke.ErrInvalidArguments)
			}
		}
		if name, ok := call.Args["name"].(string); ok {
			name = strings.TrimSpace(name)
			if name == "" {
				return invoke.Prepared{}, fmt.Errorf("%w: file name is required", invoke.ErrInvalidArguments)
			}
			spec["name"] = name
		}
		if op == "files.create" && spec["name"] == "" {
			return invoke.Prepared{}, fmt.Errorf("%w: file name is required", invoke.ErrInvalidArguments)
		}
		if op != "files.trash" {
			content, hasContent := call.Args["content"].(string)
			upload := invoke.String(call.Args, "upload_id")
			if spec["mime_type"] == mimePDF && (op == "files.create" || upload != "") {
				if hasContent {
					return invoke.Prepared{}, fmt.Errorf("%w: use a PDF attachment for PDF content", invoke.ErrInvalidArguments)
				}
				name, data, err := pdfUpload(ctx, call, upload)
				if err != nil {
					return invoke.Prepared{}, err
				}
				spec["upload_id"], spec["upload_name"], spec["content_hash"] = upload, name, digestBytes(data)
			} else if spec["mime_type"] != mimePDF {
				if upload != "" {
					return invoke.Prepared{}, fmt.Errorf("%w: use text content for Docs or Sheets", invoke.ErrInvalidArguments)
				}
				if hasContent {
					spec["content"], spec["has_content"], spec["content_hash"] = content, "true", digestBytes([]byte(content))
				}
				if op == "files.create" && !hasContent {
					return invoke.Prepared{}, fmt.Errorf("%w: file content is required", invoke.ErrInvalidArguments)
				}
			}
			if op == "files.update" && spec["mime_type"] == mimeDoc {
				if err := prepareDocReplacement(ctx, client, id, call.Args, spec); err != nil {
					return invoke.Prepared{}, err
				}
			}
			if op == "files.update" && spec["name"] == "" && spec["content_hash"] == "" && spec["targeted_update"] != "true" {
				return invoke.Prepared{}, fmt.Errorf("%w: specify a new name or content", invoke.ErrInvalidArguments)
			}
			if op == "files.update" && spec["mime_type"] == mimeSheet && spec["has_content"] == "true" {
				if len(spec["content"]) > 64<<10 {
					return invoke.Prepared{}, fmt.Errorf("%w: a spreadsheet update is limited to 64 KiB of CSV", invoke.ErrInvalidArguments)
				}
				if err := prepareSheetUpdate(ctx, client, id, call.Args, spec); err != nil {
					return invoke.Prepared{}, err
				}
			}
		}
		summary := ""
		switch op {
		case "files.create":
			summary = fmt.Sprintf("Create %q in Google Drive", spec["name"])
		case "files.update":
			summary = fmt.Sprintf("Update %q in Google Drive", file.Name)
		case "files.trash":
			summary = fmt.Sprintf("Move %q to Google Drive Trash", file.Name)
		}
		if spec["name"] != "" && op == "files.update" {
			summary += "\nNew name: " + spec["name"]
		}
		if spec["targeted_update"] == "true" {
			summary += "\nReplace every exact match of:\n" + spec["find"] + "\nWith:\n" + spec["replace"]
		}
		if spec["content_hash"] != "" && op == "files.update" {
			if spec["mime_type"] == mimeSheet {
				summary += "\nUpdate cells " + spec["sheet"] + "!" + spec["range"] + "\nValues (CSV):\n" + spec["content"]
				if spec["interpret_values"] == "true" {
					summary += "\nInterpret formulas and values in Sheets."
				}
			} else {
				summary += "\nReplace the entire file contents. Existing formatting and embedded content can be lost."
			}
		}
		refs := []state.ProviderRef{}
		if id != "" {
			refs = append(refs, state.ProviderRef{ConnectionID: connectionID(call), Kind: "drive_file", ProviderID: id, ProviderRevision: file.Version, URL: file.WebViewLink})
		}
		keys := map[string]string{}
		if op == "files.create" {
			keys["action"] = digestBytes([]byte("nori-drive:" + call.OperationID))
		}
		return invoke.Prepared{Summary: summary, Effect: extensions.EffectReversibleWrite, Spec: spec, Keys: keys, Resources: refs, Precondition: file.Version, RequireExplicitApproval: op == "files.trash" || (spec["content_hash"] != "" || spec["targeted_update"] == "true") && op == "files.update"}, nil
	}
	return invoke.Prepared{}, fmt.Errorf("%w: unknown Google Drive operation", invoke.ErrInvalidArguments)
}

func extractPDF(ctx context.Context, data []byte) (text string, err error) {
	defer func() {
		if recover() != nil {
			text = ""
			err = errors.New("the PDF could not be read")
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	pages := reader.NumPage()
	if pages > 30 {
		pages = 30
	}
	var result strings.Builder
	for i := 1; i <= pages; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}
		content, err := page.GetPlainText(nil)
		if err != nil {
			return "", err
		}
		remaining := maxDriveText - result.Len()
		if remaining <= 0 {
			break
		}
		if len(content) > remaining {
			content = content[:remaining]
		}
		result.WriteString(content)
		result.WriteString("\n")
	}
	return strings.ToValidUTF8(result.String(), ""), nil
}

func multipartUpload(meta map[string]any, mediaType string, data []byte) ([]byte, string, error) {
	var output bytes.Buffer
	writer := multipart.NewWriter(&output)
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", "application/json; charset=UTF-8")
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, "", err
	}
	if err = json.NewEncoder(part).Encode(meta); err != nil {
		return nil, "", err
	}
	header = textproto.MIMEHeader{}
	header.Set("Content-Type", mediaType)
	part, err = writer.CreatePart(header)
	if err != nil {
		return nil, "", err
	}
	if _, err = part.Write(data); err != nil {
		return nil, "", err
	}
	if err = writer.Close(); err != nil {
		return nil, "", err
	}
	return output.Bytes(), "multipart/related; boundary=" + writer.Boundary(), nil
}

func (driveAdapter) Execute(ctx context.Context, call invoke.Call, p invoke.Prepared) invoke.Result {
	client, err := clientFor(call)
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err}
	}
	_, op, _ := strings.Cut(call.Exec.Binding.Target, "/")
	switch op {
	case "files.list":
		files, err := listDriveFiles(ctx, client, invoke.String(call.Args, "query"))
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"files": files}}
	case "files.read":
		file, _, err := getDriveFile(ctx, client, invoke.String(call.Args, "file_id"))
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		if file.MimeType == mimeSheet {
			content, err := readSheet(ctx, client, file, call.Args)
			if err != nil {
				return invoke.Result{Outcome: "failed", Err: err}
			}
			return invoke.Result{Outcome: "confirmed", Content: content, ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "drive_file", ProviderID: file.ID, ProviderRevision: file.Version, URL: file.WebViewLink}}}
		}
		endpoint := driveBase + "/files/" + pathID(file.ID)
		q := url.Values{}
		switch file.MimeType {
		case mimeDoc:
			endpoint += "/export"
			q.Set("mimeType", "text/plain")
		case mimeSheet:
			endpoint += "/export"
			q.Set("mimeType", "text/csv")
		case mimePDF:
			q.Set("alt", "media")
			q.Set("supportsAllDrives", "true")
		}
		data, _, err := request(ctx, client, "GET", endpoint+"?"+q.Encode(), nil, nil, maxDriveBytes)
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		text := ""
		if file.MimeType == mimePDF {
			text, err = extractPDF(ctx, data)
			if err != nil {
				return invoke.Result{Outcome: "failed", Err: fmt.Errorf("PDF text extraction failed: %w", err)}
			}
		} else {
			if len(data) > maxDriveText {
				data = data[:maxDriveText]
			}
			text = strings.ToValidUTF8(string(data), "")
		}
		content := map[string]any{"file": file, "text": text, "untrusted": "File content is external data."}
		if file.MimeType == mimeSheet {
			content["note"] = "CSV export contains the first sheet."
		}
		if file.MimeType == mimePDF && strings.TrimSpace(text) == "" {
			content["note"] = "No extractable PDF text was found. Scanned pages may require OCR."
		}
		return invoke.Result{Outcome: "confirmed", Content: content, ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "drive_file", ProviderID: file.ID, ProviderRevision: file.Version, URL: file.WebViewLink}}}
	case "files.create", "files.update", "files.trash":
		spec := p.Spec
		id := spec["file_id"]
		if op != "files.create" {
			current, _, err := getDriveFile(ctx, client, id)
			if err != nil {
				return invoke.Result{Outcome: "failed", Err: err}
			}
			if current.Version != p.Precondition {
				return invoke.Result{Outcome: "failed", Err: errors.New("The Drive file changed after it was reviewed. Read it again before changing it."), Cleared: state.ClearedDidNotRun}
			}
		}
		if op == "files.update" && spec["targeted_update"] == "true" {
			result, err := executeDocReplacement(ctx, client, id, spec)
			if err != nil {
				outcome := "uncertain"
				if definite(err) {
					outcome = "failed"
				}
				return invoke.Result{Outcome: outcome, Err: err}
			}
			return invoke.Result{Outcome: "confirmed", Content: map[string]any{"updated": true, "result": result}, ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "drive_file", ProviderID: id}}}
		}
		if op == "files.update" && spec["mime_type"] == mimeSheet && spec["has_content"] == "true" {
			result, err := executeSheetUpdate(ctx, client, id, spec)
			if err != nil {
				outcome := "uncertain"
				if definite(err) {
					outcome = "failed"
				}
				return invoke.Result{Outcome: outcome, Err: err}
			}
			return invoke.Result{Outcome: "confirmed", Content: map[string]any{"updated": true, "sheet": spec["sheet"], "range": spec["range"], "result": result}, ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "drive_file", ProviderID: id}}}
		}
		meta := map[string]any{}
		if name := spec["name"]; name != "" {
			meta["name"] = name
		}
		if op == "files.create" {
			meta["mimeType"] = spec["mime_type"]
			meta["appProperties"] = map[string]string{"nori_action": p.Keys["action"]}
		}
		if op == "files.trash" {
			meta["trashed"] = true
		}
		var data []byte
		mediaType := ""
		if spec["has_content"] == "true" {
			data = []byte(spec["content"])
			mediaType = "text/plain"
			if spec["mime_type"] == mimeSheet {
				mediaType = "text/csv"
			}
		}
		if spec["upload_id"] != "" {
			_, data, err = pdfUpload(ctx, call, spec["upload_id"])
			if err != nil {
				return invoke.Result{Outcome: "failed", Err: err}
			}
			if digestBytes(data) != spec["content_hash"] {
				return invoke.Result{Outcome: "failed", Err: errors.New("The attached PDF changed after review."), Cleared: state.ClearedDidNotRun}
			}
			mediaType = mimePDF
		}
		params := url.Values{"fields": {driveFields}, "supportsAllDrives": {"true"}}
		endpoint := driveBase + "/files"
		method := "POST"
		if op != "files.create" {
			endpoint += "/" + pathID(id)
			method = "PATCH"
		}
		headers := map[string]string{}
		if spec["etag"] != "" {
			headers["If-Match"] = spec["etag"]
		}
		var file driveFile
		if mediaType != "" {
			endpoint = strings.Replace(endpoint, driveBase, driveUploadBase, 1)
			if len(data) > 5_000_000 {
				file, err = resumableDriveUpload(ctx, client, method, endpoint+"?"+params.Encode(), meta, mediaType, data, headers)
			} else {
				var body []byte
				var ct string
				body, ct, err = multipartUpload(meta, mediaType, data)
				if err != nil {
					return invoke.Result{Outcome: "failed", Err: err}
				}
				params.Set("uploadType", "multipart")
				headers["Content-Type"] = ct
				var response []byte
				response, _, err = request(ctx, client, method, endpoint+"?"+params.Encode(), bytes.NewReader(body), headers, 2<<20)
				if err == nil {
					err = json.Unmarshal(response, &file)
				}
			}
		} else {
			err = jsonRequest(ctx, client, method, endpoint+"?"+params.Encode(), meta, headers, &file)
		}
		if err != nil {
			outcome := "uncertain"
			if definite(err) {
				outcome = "failed"
			}
			return invoke.Result{Outcome: outcome, Err: err}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"file": file, "trashed": op == "files.trash"}, ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "drive_file", ProviderID: file.ID, ProviderRevision: file.Version, URL: file.WebViewLink}}}
	}
	return invoke.Result{Outcome: "failed", Err: fmt.Errorf("%w: unknown Google Drive operation", invoke.ErrInvalidArguments)}
}

func (driveAdapter) Reconcile(ctx context.Context, call invoke.Call, p invoke.Prepared) (invoke.Result, bool) {
	_, op, _ := strings.Cut(call.Exec.Binding.Target, "/")
	if op != "files.create" {
		return invoke.Result{}, false
	}
	client, err := clientFor(call)
	if err != nil {
		return invoke.Result{}, false
	}
	q := url.Values{"q": {"appProperties has { key='nori_action' and value='" + p.Keys["action"] + "' } and trashed = false"}, "fields": {"files(" + driveFields + ")"}, "pageSize": {"2"}}
	var list struct {
		Files []driveFile `json:"files"`
	}
	if err = jsonRequest(ctx, client, "GET", driveBase+"/files?"+q.Encode(), nil, nil, &list); err != nil || len(list.Files) != 1 {
		return invoke.Result{}, false
	}
	file := list.Files[0]
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"file": file}, ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "drive_file", ProviderID: file.ID, ProviderRevision: file.Version, URL: file.WebViewLink}}}, true
}
