package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// --- Heartbeat Loop ---

func startHeartbeatLoop() {
	// Send Heartbeat every 10 seconds (approx 2 ticks)
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

	myTick := atomic.LoadInt64(&CurrentTick)

	// 1. Create Payload
	payload := HeartbeatRequest{
		UUID:      ServerUUID,
		Tick:      myTick,
		PeerCount: len(peersList),
		GenHash:   GenesisHash,
	}

	// 2. Sign It (Proof of Identity)
	// Sign "UUID:Tick"
	msg := fmt.Sprintf("%s:%d", payload.UUID, payload.Tick)
	sig := SignMessage(PrivateKey, []byte(msg))
	payload.Signature = hex.EncodeToString(sig)

	// 3. Serialize & Compress
	data, _ := json.Marshal(payload)
	compressed := compressLZ4(data)

	// 4. Fan-Out (Send to all peers)
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
	client := &http.Client{Timeout: 2 * time.Second}
	// Use the Dual-Mode content type
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
			go recalculateLeader()
		}
	}
}

// --- Consensus & Sync ---

func recalculateLeader() {
	peerLock.RLock()
	defer peerLock.RUnlock()

	type Candidate struct {
		UUID  string
		Score int64
	}
	
	myScore := (atomic.LoadInt64(&CurrentTick) << 16) | int64(len(Peers))
	candidates := []Candidate{{UUID: ServerUUID, Score: myScore}}
	
	for _, p := range Peers {
		if p.Reputation < 0 { continue } // Skip banned
		pScore := (p.LastTick << 16) | int64(p.PeerCount)
		candidates = append(candidates, Candidate{UUID: p.UUID, Score: pScore})
	}

	// Sort High Score -> Low Score
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].UUID > candidates[j].UUID
	})

	BestNode := candidates[0]
	LeaderUUID = BestNode.UUID
	IsLeader = (LeaderUUID == ServerUUID)

	// TDMA Calculation
	allUUIDs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		allUUIDs = append(allUUIDs, c.UUID)
	}
	sort.Strings(allUUIDs) // Deterministic slot order

	myRank := 0
	for i, id := range allUUIDs {
		if id == ServerUUID {
			myRank = i
			break
		}
	}

	totalNodes := len(allUUIDs)
	if totalNodes > 0 {
		slotDuration := 5000 / totalNodes
		PhaseOffset = time.Duration(slotDuration*myRank) * time.Millisecond
	} else {
		PhaseOffset = 0
	}
}

func CalculateOffset() time.Duration {
	return PhaseOffset
}

// Called by handleHeartbeat when we hear from the Leader
func syncClock(leaderTick int64) {
	myTick := atomic.LoadInt64(&CurrentTick)
	delta := leaderTick - myTick

	// 1. HARD SNAP (We are way behind)
	if delta > 10 {
		InfoLog.Printf("⚠️ Major Desync (Delta %d). Snapping to Tick %d", delta, leaderTick)
		atomic.StoreInt64(&CurrentTick, leaderTick)
		// In a real implementation, this is where you'd call /federation/sync
		// to download the missing state snapshots.
		return
	}

	// 2. SLEW (Micro-adjust speed)
	// If we are behind (delta > 0), run faster (shorter duration)
	// If we are ahead (delta < 0), run slower (longer duration)
	
	baseDuration := int64(5000)
	adjustment := int64(0)

	if delta > 0 {
		adjustment = -50 // Run faster (4950ms)
	} else if delta < 0 {
		adjustment = 50  // Run slower (5050ms)
	}

	newDuration := baseDuration + adjustment
	
	// Clamp
	if newDuration < MinTickDuration { newDuration = MinTickDuration }
	if newDuration > MaxTickDuration { newDuration = MaxTickDuration }

	atomic.StoreInt64(&TickDuration, newDuration)
}
