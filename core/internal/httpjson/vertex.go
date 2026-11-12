package httpjson

import (
	"bufio"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/felixge/httpsnoop"
)

const VertexResponseHeader = "X-Nori-Vertex-Response"

const vertexResponseType = "application/vnd.nori.response"

type vertexResponse struct {
	Status  int         `json:"status"`
	Headers http.Header `json:"headers"`
}

func CopyGatewayHeaders(dst, src http.Header) {
	for _, key := range []string{"Content-Type", "Content-Disposition", "Content-Security-Policy", "Content-Length", "Allow", "Retry-After", "Mcp-Protocol-Version", "Mcp-Session-Id"} {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}

// ServeVertexResponse preserves status and headers across Agent Runtime,
// which rewrites HTTP statuses and removes application response headers.
// Successful event streams keep their original media type for streaming.
func ServeVertexResponse(w http.ResponseWriter, serve func(http.ResponseWriter)) {
	started := false
	start := func(status int) {
		if started {
			return
		}
		started = true
		media, _, _ := mime.ParseMediaType(w.Header().Get("Content-Type"))
		if status == http.StatusOK && media == "text/event-stream" {
			w.WriteHeader(status)
			return
		}
		metadata := vertexResponse{Status: status, Headers: make(http.Header)}
		CopyGatewayHeaders(metadata.Headers, w.Header())
		for key := range metadata.Headers {
			w.Header().Del(key)
		}
		w.Header().Set("Content-Type", vertexResponseType)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(metadata)
	}
	serve(httpsnoop.Wrap(w, httpsnoop.Hooks{
		WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
			return func(status int) {
				if status < 200 {
					next(status)
					return
				}
				start(status)
			}
		},
		Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
			return func(data []byte) (int, error) {
				start(http.StatusOK)
				return next(data)
			}
		},
		ReadFrom: func(next httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
			return func(body io.Reader) (int64, error) {
				start(http.StatusOK)
				return next(body)
			}
		},
		Flush: func(next httpsnoop.FlushFunc) httpsnoop.FlushFunc {
			return func() {
				start(http.StatusOK)
				next()
			}
		},
	}))
	start(http.StatusOK)
}

// ReadVertexResponse consumes only the metadata line. Downloads can stream
// directly from the remaining body and the original body retains ownership of Close.
func ReadVertexResponse(response *http.Response) bool {
	if response.StatusCode != http.StatusOK {
		return false
	}
	media, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if media == "text/event-stream" {
		return true
	}
	if media != vertexResponseType {
		return false
	}
	body := bufio.NewReaderSize(response.Body, 16<<10)
	line, err := body.ReadSlice('\n')
	var metadata vertexResponse
	if err != nil || json.Unmarshal(line, &metadata) != nil || metadata.Status < 200 || metadata.Status >= 600 {
		return false
	}
	response.StatusCode = metadata.Status
	response.Header = metadata.Headers
	response.Body = struct {
		io.Reader
		io.Closer
	}{body, response.Body}
	return true
}
