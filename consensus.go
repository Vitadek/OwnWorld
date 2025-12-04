package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	// Assuming you have the proto package generated
	// pb "ownworld/pkg/federation" 
	// "google.golang.org/protobuf/proto"
)

// Replaces snapshotPeers
func startHeartbeatLoop() {
	// Tick every 10 seconds (2 Game Ticks)
	ticker := time.NewTicker(10 * time.Second)
	
	for range ticker.C {
		broadcastHeartbeat()
		pruneDeadPeers()
	}
}

func broadcastHeartbeat() {
	peerLock.RLock()
	peersList := make([]Peer, 0, len(Peers))
	for _, p := range Peers {
		peersList = append(peersList, *p)
	}
	peerLock.RUnlock()

	// 1. Construct Heartbeat Payload
	// (Using JSON-over-LZ4 for now to match your handlers)
	hb := HandshakeRequest{ // Reusing this struct or create a Heartbeat struct
		UUID:        ServerUUID,
		GenesisHash: GenesisHash,
		// We need to send current tick/peer count for election
		// Add these fields to HandshakeRequest or create a dedicated struct
	}
	
	// Let's create a dedicated lightweight struct for the wire
	type HeartbeatWire struct {
		UUID      string `json:"uuid"`
		Tick      int64  `json:"tick"`
		PeerCount int    `json:"peer_count"`
		GenHash   string `json:"gen_hash"`
		Signature string `json:"sig"` // Hex encoded
	}

	myTick := atomic.LoadInt64(&CurrentTick)
	
	payload := HeartbeatWire{
		UUID:      ServerUUID,
		Tick:      myTick,
		PeerCount: len(peersList),
		GenHash:   GenesisHash,
	}

	// 2. Sign It (Integrity)
	// Sign the non-signature parts (UUID + Tick)
	msg := fmt.Sprintf("%s:%d", payload.UUID, payload.Tick)
	sig := SignMessage(PrivateKey, []byte(msg))
	payload.Signature = hex.EncodeToString(sig)

	// 3. Serialize & Compress
	data, _ := json.Marshal(payload)
	compressed := compressLZ4(data)

	// 4. Broadcast (Fan-out)
	var wg sync.WaitGroup
	for _, p := range peersList {
		wg.Add(1)
		go func(target Peer) {
			defer wg.Done()
			sendHeartbeat(target.Url, compressed)
		}(p)
	}
	wg.Wait()
}

func sendHeartbeat(url string, data []byte) {
	client := &http.Client{Timeout: 2 * time.Second} // Fast timeout
	resp, err := client.Post(url+"/federation/heartbeat", "application/x-ownworld-fed", bytes.NewBuffer(data))
	if err == nil {
		resp.Body.Close()
	}
}

func pruneDeadPeers() {
	peerLock.Lock()
	defer peerLock.Unlock()

	now := time.Now()
	for id, p := range Peers {
		// If haven't heard in 5 minutes, remove
		if now.Sub(p.LastSeen) > 5*time.Minute {
			InfoLog.Printf("🍂 Pruning dead peer: %s", id)
			delete(Peers, id)
			// Trigger election since peer count changed
			go recalculateLeader() 
		}
	}
}
