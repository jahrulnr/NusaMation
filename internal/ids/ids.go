package ids

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"strings"
	"time"
)

var enc = base32.StdEncoding.WithPadding(base32.NoPadding)

// New returns a lexicographically sortable, URL-safe ID with a short type prefix.
func New(prefix string) string {
	var b [13]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli()))
	_, _ = rand.Read(b[8:])
	id := strings.ToLower(enc.EncodeToString(b[:]))
	if prefix != "" {
		return prefix + "_" + id
	}
	return id
}

func Token(n int) string {
	if n <= 0 {
		n = 32
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.TrimRight(enc.EncodeToString(b), "=")
}
