package env

import (
	"encoding/binary"
	"hash"
	"time"
)

func writeTag(h hash.Hash, tag string) {
	writeString(h, tag)
}

func writeUint64(h hash.Hash, n uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], n)
	_, _ = h.Write(buf[:])
}

func writeInt64(h hash.Hash, n int64) {
	writeUint64(h, uint64(n))
}

func writeBool(h hash.Hash, b bool) {
	if b {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
}

func writeString(h hash.Hash, s string) {
	writeUint64(h, uint64(len(s)))
	_, _ = h.Write([]byte(s))
}

func writeTime(h hash.Hash, t time.Time) {
	if t.IsZero() {
		writeBool(h, false)
		return
	}
	writeBool(h, true)
	utc := t.UTC()
	writeInt64(h, utc.Unix())
	writeInt64(h, int64(utc.Nanosecond()))
}
