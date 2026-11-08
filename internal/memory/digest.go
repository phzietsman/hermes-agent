package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
)

// digest is Python's transcript_digest: sha256 over "role\0content\0" per message, first 16 hex characters.
type digest struct{ h hash.Hash }

func newDigest() digest { return digest{sha256.New()} }

func (d digest) add(role, content string) { fmt.Fprintf(d.h, "%s\x00%s\x00", role, content) }

func (d digest) sum() string { return hex.EncodeToString(d.h.Sum(nil))[:16] }
