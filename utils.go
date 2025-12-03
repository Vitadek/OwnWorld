package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pierrec/lz4/v4"
	"golang.org/x/time/rate"
	"lukechampine.com/blake3"
)

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

// Phase 2.2: Signature Helpers
func SignMessage(privKey ed25519.PrivateKey, msg []byte) []byte {
	return ed25519.Sign(privKey, msg)
}

func VerifySignature(pubKey ed25519.PublicKey, msg, sig []byte) bool {
	return ed25519.Verify(pubKey, msg, sig)
}

func getLimiter(ip string) *rate.Limiter {
	ipLock.Lock()
	defer ipLock.Unlock()
	limiter, exists := ipLimiters[ip]
	if !exists {
		// UPDATED: Increased limit to 10 req/s with burst of 20 to prevent 429s during polling
		limiter = rate.NewLimiter(10, 20)
		ipLimiters[ip] = limiter
	}
	return limiter
}

// middlewareCORS adds headers to allow browser clients
func middlewareCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-User-ID")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func middlewareSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !getLimiter(ip).Allow() {
			http.Error(w, "Rate Limit", 429)
			return
		}

		// Allow OPTIONS (CORS Preflight) to skip security checks
		if r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}

		contentType := r.Header.Get("Content-Type")

		// Mode A: Federation
		if contentType == "application/x-ownworld-fed" {
			if !strings.Contains(r.URL.Path, "handshake") {
				senderUUID := r.Header.Get("X-Server-UUID")
				peerLock.RLock()
				_, known := peers[senderUUID]
				peerLock.RUnlock()
				if !known {
					http.Error(w, "Unknown Peer", 403)
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}

		// Mode B: Client API
		// Robust check: Allow if Content-Type contains "application/json" (handles charset)
		// OR if method is GET (often no body/type)
		// OR if type is empty (some clients be lazy)
		// OR if type is "text/plain;charset=UTF-8" (sometimes sent by fetch on errors/text responses)
		if r.Method == "GET" || strings.Contains(contentType, "application/json") || contentType == "" || strings.Contains(contentType, "text/plain") {
			if strings.HasPrefix(r.URL.Path, "/api/") && !Config.CommandControl {
				http.Error(w, "Node is in Infrastructure Mode (No User API)", 503)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		http.Error(w, "Bad Type: "+contentType, 415)
	})
}
