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
	MaxUploadBytes = 10 << 20
	MaxAttachments = 10
)

var ErrUploadType = errors.New("Only PNG, JPEG or WebP images, PDFs, CSV, ICS calendar, text and Markdown files can be attached")

var UploadMediaTypes = []string{"image/png", "image/jpeg", "image/webp", "application/pdf", "text/csv", "text/calendar", "text/plain", "text/markdown"}

var ErrAttachmentMissing = errors.New("an attached file wasn't uploaded")

type Attachment struct {
	ID        string `json:"id" firestore:"id"`
	Name      string `json:"name" firestore:"name"`
	MediaType string `json:"media_type" firestore:"media_type"`
	Size      int64  `json:"size" firestore:"size"`
}

type UploadLifecycle string

const (
	UploadPending  UploadLifecycle = "pending"
	UploadReady    UploadLifecycle = "ready"
	UploadRetained UploadLifecycle = "retained"
	UploadExpiring UploadLifecycle = "expiring"
	UploadExpired  UploadLifecycle = "expired"
)

type Upload struct {
	Attachment
	Lifecycle UploadLifecycle `json:"lifecycle,omitempty" firestore:"lifecycle,omitempty"`
	SHA256    string          `json:"sha256" firestore:"sha256"`
	CreatedAt time.Time       `json:"created_at" firestore:"created_at"`
}

func PrepareUpload(id, name, declared string, data []byte, now time.Time) (Upload, error) {
	name = CleanUploadName(name)
	mediaType, err := SniffUpload(name, declared, data)
	if err != nil {
		return Upload{}, err
	}
	sum := sha256.Sum256(data)
	return Upload{
		Attachment: Attachment{ID: id, Name: name, MediaType: mediaType, Size: int64(len(data))},
		SHA256:     hex.EncodeToString(sum[:]), CreatedAt: now.UTC(), Lifecycle: UploadPending,
	}, nil
}
func PutUploadBytes(ctx context.Context, files Artifacts, upload Upload, data []byte) error {
	key := UploadKey(upload.ID)
	err := files.PutFile(ctx, key, upload.MediaType, data)
	if !errors.Is(err, ErrFileExists) {
		return err
	}
	saved, err := files.GetFile(ctx, key)
	if err != nil {
		return err
	}
	if !bytes.Equal(saved, data) {
		return ErrDuplicate
	}
	return nil
}

func UploadKey(id string) string { return "uploads/" + id }

var fileKey = regexp.MustCompile(`^(uploads|results)/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func ValidFileKey(key string) bool { return fileKey.MatchString(key) }

func ValidUploadID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

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
	case strings.EqualFold(path.Ext(name), ".ics") && utf8.Valid(data) && bytes.HasPrefix(bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))), []byte("BEGIN:VCALENDAR")):
		found = "text/calendar"
	case (strings.EqualFold(path.Ext(name), ".md") || strings.EqualFold(path.Ext(name), ".markdown")) && utf8.Valid(data) && !bytes.ContainsRune(data, 0):
		found = "text/markdown"
	case strings.EqualFold(path.Ext(name), ".txt") && utf8.Valid(data) && !bytes.ContainsRune(data, 0):
		found = "text/plain"
	}
	if found == "" {
		return "", ErrUploadType
	}
	if declared != found && !strings.HasPrefix(found, "text/") && declared != "application/octet-stream" && declared != "" {
		return "", fmt.Errorf("%w: the file's contents don't match its type", ErrUploadType)
	}
	return found, nil
}

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
