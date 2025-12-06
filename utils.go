package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
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

// NOTE: bufferPool is defined in globals.go
// NOTE: ipLimiters and ipLock are defined in globals.go

func setupLogging() {
	logDir := "./logs"
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		os.Mkdir(logDir, 0755)
	}

	// standard logs
	fInfo, _ := os.OpenFile(filepath.Join(logDir, "server.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	fErr, _ := os.OpenFile(filepath.Join(logDir, "error.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)

	InfoLog = log.New(io.MultiWriter(os.Stdout, fInfo), "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLog = log.New(io.MultiWriter(os.Stderr, fErr), "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)

	// --- DEBUG LOGGER SETUP ---
	fDebug, _ := os.OpenFile(filepath.Join(logDir, "debug.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)

	// Default to discard (silent)
	var debugOut io.Writer = io.Discard

	// Enable if Env Var is set
	if os.Getenv("OWNWORLD_DEBUG") == "true" {
		debugOut = io.MultiWriter(os.Stdout, fDebug)
		InfoLog.Println("🔧 DEBUG MODE ENABLED")
	}

	DebugLog = log.New(debugOut, "DEBUG: ", log.Ldate|log.Ltime|log.Lshortfile)
}

func compressLZ4(src []byte) []byte {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)
	w := lz4.NewWriter(buf)
	w.Write(src)
	w.Close()
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out
}

func decompressLZ4(src []byte) []byte {
	r := lz4.NewReader(bytes.NewReader(src))
	out, _ := io.ReadAll(r)
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
	if len(pubKey) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(pubKey, msg, sig)
}

func encryptKey(key ed25519.PrivateKey, password string) string {
	passHash := blake3.Sum256([]byte(password))
	block, _ := aes.NewCipher(passHash[:])
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	io.ReadFull(rand.Reader, nonce)
	ciphertext := gcm.Seal(nonce, nonce, key, nil)
	return hex.EncodeToString(ciphertext)
}

// --- Middleware ---

func getLimiter(ip string) *rate.Limiter {
	ipLock.Lock()
	defer ipLock.Unlock()
	limiter, exists := ipLimiters[ip]
	if !exists {
		limiter = rate.NewLimiter(1, 5)
		ipLimiters[ip] = limiter
	}
	return limiter
}

func middlewareSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip != "::1" && ip != "127.0.0.1" && ip != "" {
			if !getLimiter(ip).Allow() {
				http.Error(w, "Rate Limit Exceeded", 429)
				return
			}
		}

		if r.Method == "OPTIONS" {
            // FIX CORS: Must include headers here for preflight check
            w.Header().Set("Access-Control-Allow-Origin", "*")
            w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
            w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-User-ID, X-User-UUID, X-Server-UUID, X-Session-Token")
			w.WriteHeader(http.StatusOK)
			return
		}

		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/x-protobuf") {
			next.ServeHTTP(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func middlewareCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
        // FIX CORS: Added X-Session-Token and X-User-UUID explicitly
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-User-ID, X-User-UUID, X-Server-UUID, X-Session-Token")
		
        if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
