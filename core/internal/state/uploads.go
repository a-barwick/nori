package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// MaxUploadBytes is the size limit for one file. Agent Runtime accepted 16 MiB and refused 33 MiB in a probe.
	MaxUploadBytes = 10 << 20
	MaxAttachments = 10
)

var ErrUploadType = errors.New("Only PNG, JPEG or WebP images, PDFs and CSV files can be attached")

var ErrAttachmentMissing = errors.New("an attached file wasn't uploaded")

// Attachment is what a message or task records about an uploaded file. Name came from the user's device, so it's
// treated as untrusted data wherever a model sees it.
type Attachment struct {
	ID        string `json:"id" firestore:"id"`
	Name      string `json:"name" firestore:"name"`
	MediaType string `json:"media_type" firestore:"media_type"`
	Size      int64  `json:"size" firestore:"size"`
}

// Upload is the saved record of a file in quarantined storage. The record is written after the bytes, so a record
// always means a complete file was stored.
type Upload struct {
	Attachment
	SHA256    string    `json:"sha256" firestore:"sha256"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
}

// SaveUploadBytes saves the complete file before its caller commits metadata.
func SaveUploadBytes(ctx context.Context, files Artifacts, key, id, name, declared string, data []byte) (Upload, error) {
	name = CleanUploadName(name)
	mediaType, err := SniffUpload(name, declared, data)
	if err != nil {
		return Upload{}, err
	}
	sum := sha256.Sum256(data)
	upload := Upload{
		Attachment: Attachment{ID: id, Name: name, MediaType: mediaType, Size: int64(len(data))},
		SHA256:     hex.EncodeToString(sum[:]), CreatedAt: time.Now().UTC(),
	}
	err = files.PutFile(ctx, key, mediaType, data)
	if !errors.Is(err, ErrFileExists) {
		return upload, err
	}
	saved, err := files.GetFile(ctx, key)
	if err != nil {
		return upload, err
	}
	if !bytes.Equal(saved, data) {
		return upload, ErrDuplicate
	}
	return upload, nil
}

func UploadKey(id string) string { return "uploads/" + id }

var fileKey = regexp.MustCompile(`^(uploads|results)/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidFileKey requires one of the file store's known folders and a canonical UUID.
func ValidFileKey(key string) bool { return fileKey.MatchString(key) }

func ValidUploadID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

// SniffUpload decides a file's type from its bytes. A declared type that disagrees with them is refused, and so is
// anything else, because a task reads the file by this type and the browser shows it by this type.
func SniffUpload(name, declared string, data []byte) (string, error) {
	declared = strings.ToLower(strings.TrimSpace(strings.SplitN(declared, ";", 2)[0]))
	if len(data) == 0 {
		return "", fmt.Errorf("%w: the file is empty", ErrUploadType)
	}
	found := ""
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		found = "image/png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		found = "image/jpeg"
	case len(data) > 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		found = "image/webp"
	case bytes.HasPrefix(data, []byte("%PDF-")):
		found = "application/pdf"
	case strings.EqualFold(path.Ext(name), ".csv") && utf8.Valid(data) && !bytes.ContainsRune(data, 0):
		found = "text/csv"
	}
	if found == "" {
		return "", ErrUploadType
	}
	// Browsers report CSV in several ways, and the name and bytes already decided it.
	if declared != found && found != "text/csv" && declared != "application/octet-stream" && declared != "" {
		return "", fmt.Errorf("%w: the file's contents don't match its type", ErrUploadType)
	}
	return found, nil
}

// CleanUploadName keeps a name safe to display: the last path segment, with control characters removed, at most 120
// bytes.
func CleanUploadName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	for len(name) > 120 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

var unsafeFileName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ComputerFileName is the name of an upload's copy on the computer. The ID's prefix keeps two files with the same name
// apart, and it starts the name, so no name can be hidden or look like an option to a command.
func ComputerFileName(a Attachment) string {
	return a.ID[:8] + "-" + strings.TrimLeft(unsafeFileName.ReplaceAllString(a.Name, "_"), "._-")
}

func AttachmentPart(a Attachment) Part {
	return Part{
		Type: PartAttachment, ID: a.ID, Name: a.Name, MediaType: a.MediaType, Size: a.Size,
		Fallback: fmt.Sprintf("Attached file: %s (%s, %d bytes)", a.Name, a.MediaType, a.Size),
	}
}

func MergeAttachments(list, added []Attachment) []Attachment {
	for _, attachment := range added {
		seen := false
		for _, existing := range list {
			seen = seen || existing.ID == attachment.ID
		}
		if !seen {
			list = append(append([]Attachment{}, list...), attachment)
		}
	}
	return list
}
