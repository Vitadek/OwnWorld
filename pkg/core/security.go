package core

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"sync"

	"github.com/lukechampine/blake3"
	"github.com/pierrec/lz4/v4"
)

var bufferPool = sync.Pool{New: func() interface{} { return new(bytes.Buffer) }}

// --- Compression ---

func Compress(src []byte) []byte {
	buf := bufferPool.Get().(*bytes.Buffer)
	defer bufferPool.Put(buf)
	buf.Reset()

	w := lz4.NewWriter(buf)
	w.Write(src)
	w.Close()

	// Return strictly sized slice
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out
}

func Decompress(src []byte) []byte {
	// In production: Use a Reader Pool too
	r := lz4.NewReader(bytes.NewReader(src))
	var out bytes.Buffer
	out.ReadFrom(r)
	return out.Bytes()
}

// --- Hashing ---

func Hash(data []byte) string {
	h := blake3.Sum256(data)
	return hex.EncodeToString(h[:])
}

// --- Identity ---

func VerifySignature(pubKey ed25519.PublicKey, msg, sig []byte) bool {
	if len(pubKey) != ed25519.PublicKeySize { return false }
	return ed25519.Verify(pubKey, msg, sig)
}
