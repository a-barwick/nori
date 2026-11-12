// Package sse reads complete Server-Sent Events from a Nori response.
package sse

import (
	"bufio"
	"io"
	"strings"
)

type Frame struct {
	ID   string
	Data []byte
}

type Reader struct{ scanner *bufio.Scanner }

func NewReader(body io.Reader) *Reader {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	return &Reader{scanner: scanner}
}

func (r *Reader) Next() (Frame, error) {
	var frame Frame
	var data []string
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if line == "" {
			if len(data) != 0 {
				frame.Data = []byte(strings.Join(data, "\n"))
				return frame, nil
			}
			frame = Frame{}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			frame.ID = value
		case "data":
			data = append(data, value)
		}
	}
	if err := r.scanner.Err(); err != nil {
		return Frame{}, err
	}
	return Frame{}, io.EOF
}
