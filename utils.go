package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pierrec/lz4/v4"
	"golang.org/x/time/rate"
	"lukechampine.com/blake3"
)

// NOTE: bufferPool is defined in globals.go, so we do not redeclare it here.

func setupLogging() {
	logDir := "./logs"
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		os.Mkdir(logDir, 0755)
	}
	fInfo, _ := os.OpenFile(filepath.Join(logDir, "server.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	fErr, _ := os.OpenFile(filepath.Join(logDir, "error.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	InfoLog = log.New(fInfo, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLog = log.New(fErr, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
}

// Renamed back to legacy names to satisfy handlers.go and simulation.go calls
func compressLZ4(src []byte) []byte {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)
	zw := lz4.NewWriter(buf)
	zw.Write(src)
	zw.Close()
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out
}

func decompressLZ4(src []byte) []byte {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)
	zr := lz4.NewReader(bytes.NewReader(src))
	io.Copy(buf, zr)
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out
}

func hashBLAKE3(data []byte) string {
	sum := blake3.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func SignMessage(privKey ed25519.PrivateKey, msg []byte) []byte {
	return ed25519.Sign(privKey, msg)
}

func VerifySignature(pubKey ed25519.PublicKey, msg, sig []byte) bool {
	return ed25519.Verify(pubKey, msg, sig)
}

// --- Middleware & Security ---

func getLimiter(ip string) *rate.Limiter {
	ipLock.Lock()
	defer ipLock.Unlock()
	limiter, exists := ipLimiters[ip]
	if !exists {
		// Layer 1: IP Rate Limiting (Strict 1 req/s, Burst 5)
		limiter = rate.NewLimiter(1, 5)
		ipLimiters[ip] = limiter
	}
	return limiter
}

func middlewareSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Layer 1: IP Rate Limit
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		// Fallback for localhost testing
		if ip == "::1" || ip == "127.0.0.1" {
			// Looser limit for localhost
		} else if !getLimiter(ip).Allow() {
			http.Error(w, "Rate Limit Exceeded", 429)
			return
		}

		if r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}

		contentType := r.Header.Get("Content-Type")

		// Mode A: Federation Logic
		if contentType == "application/x-ownworld-fed" {
			// Layer 2: UUID Allowlist (Skip for Handshake)
			if !strings.Contains(r.URL.Path, "handshake") {
				senderUUID := r.Header.Get("X-Server-UUID")
				peerLock.RLock()
				_, known := peers[senderUUID]
				peerLock.RUnlock()
				if !known {
					http.Error(w, "Unknown Peer (Not Federated)", 403)
					return
				}
				
				// Layer 3: Probabilistic Verification could go here
				if strings.Contains(r.URL.Path, "sync") && rand.Float32() < 0.1 {
					// Verify logic placeholder
				}
			}
			next.ServeHTTP(w, r)
			return
		}

		// Mode B: Client API
		if strings.Contains(contentType, "application/json") || r.Method == "GET" || contentType == "" {
			if strings.HasPrefix(r.URL.Path, "/api/") && !Config.CommandControl {
				http.Error(w, "Node is in Infrastructure Mode (API Disabled)", 503)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		http.Error(w, "Unsupported Content-Type", 415)
	})
}

func middlewareCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-User-ID, X-Server-UUID")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
