package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"time"
)

func recalculateLeader() {
	peerLock.RLock()
	defer peerLock.RUnlock()

	type Candidate struct {
		UUID      string
		Tick      int
		PeerCount int
	}
	candidates := []Candidate{{UUID: ServerUUID, Tick: CurrentTick, PeerCount: len(peers)}}
	for _, p := range peers {
		candidates = append(candidates, Candidate{UUID: p.UUID, Tick: p.LastTick, PeerCount: 0})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Tick != candidates[j].Tick {
			return candidates[i].Tick > candidates[j].Tick
		}
		return candidates[i].UUID > candidates[j].UUID
	})

	BestNode := candidates[0]
	LeaderUUID = BestNode.UUID
	IsLeader = (LeaderUUID == ServerUUID)

	allUUIDs := make([]string, 0, len(peers)+1)
	allUUIDs = append(allUUIDs, ServerUUID)
	for uuid := range peers {
		allUUIDs = append(allUUIDs, uuid)
	}
	sort.Strings(allUUIDs)

	myRank := 0
	for i, id := range allUUIDs {
		if id == ServerUUID {
			myRank = i
			break
		}
	}

	totalNodes := len(allUUIDs)
	slotDuration := 5000 / totalNodes
	PhaseOffset = time.Duration(slotDuration*myRank) * time.Millisecond
}

func handleSyncLedger(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	decompressed := decompressLZ4(body)
	var req LedgerPayload
	json.Unmarshal(decompressed, &req)

	peerLock.Lock()
	defer peerLock.Unlock()

	// Phase 6.2: Fork Detection
	if p, ok := peers[req.UUID]; ok {
		if req.Tick == p.LastTick+1 {
			if req.Entry.PrevHash != p.LastHash {
				ErrorLog.Printf("SECURITY: FORK DETECTED from %s. Expected Prev %s, Got %s. BANNING.", req.UUID, p.LastHash, req.Entry.PrevHash)
				delete(peers, req.UUID)
				http.Error(w, "Fork Detected: Banned", 403)
				return
			}
		}

		p.LastTick = req.Tick
		p.LastHash = req.Entry.FinalHash
		p.LastSeen = time.Now()
	}

	stateLock.Lock()
	defer stateLock.Unlock()

	if req.Tick > CurrentTick+5 {
		InfoLog.Printf("SYNC SNAP: Jumping from %d to %d (Leader: %s)", CurrentTick, req.Tick, req.UUID)
		CurrentTick = req.Tick
		PreviousHash = req.Entry.FinalHash
	}
	w.WriteHeader(http.StatusOK)
}
