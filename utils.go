package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"io"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/pierrec/lz4/v4"
	"golang.org/x/time/rate"
	"lukechampine.com/blake3"
)

var bufferPool = sync.Pool{New: func() interface{} { return new(bytes.Buffer) }}

func hashBLAKE3(data []byte) string {
	sum := blake3.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func compressLZ4(src []byte) []byte {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)
	w := lz4.NewWriter(buf)
	w.Write(src)
	w.Close()
	out := make([]byte, buf.Len()); copy(out, buf.Bytes()); return out
}

func decompressLZ4(src []byte) []byte {
	r := lz4.NewReader(bytes.NewReader(src))
	out, _ := io.ReadAll(r)
	return out
}

func SignMessage(msg []byte) []byte {
	return ed25519.Sign(PrivateKey, msg)
}

func VerifySignature(pub ed25519.PublicKey, msg, sig []byte) bool {
	return ed25519.Verify(pub, msg, sig)
}

// Middleware
var ipLimiters = make(map[string]*rate.Limiter)
var ipLock sync.Mutex

func getLimiter(ip string) *rate.Limiter {
	ipLock.Lock(); defer ipLock.Unlock()
	if _, exists := ipLimiters[ip]; !exists {
		ipLimiters[ip] = rate.NewLimiter(1, 5)
	}
	return ipLimiters[ip]
}

func middlewareSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !getLimiter(ip).Allow() {
			http.Error(w, "429 Too Many Requests", 429); return
		}
		next.ServeHTTP(w, r)
	})
}
