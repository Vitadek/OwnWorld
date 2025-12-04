package main

import (
	"sort"
	"time"
)

// Phase 4.1: Leader Election & Score Logic
func recalculateLeader() {
	peerLock.RLock()
	defer peerLock.RUnlock()

	type Candidate struct {
		UUID      string
		Score     int64
	}
	
	myScore := (int64(CurrentTick) << 16) | int64(len(Peers))
	candidates := []Candidate{{UUID: ServerUUID, Score: myScore}}
	
	for _, p := range Peers {
		// Ignore banned peers
		if p.Reputation < 0 { continue }
		
		pScore := (int64(p.LastTick) << 16) | int64(p.PeerCount)
		candidates = append(candidates, Candidate{UUID: p.UUID, Score: pScore})
	}

	// Deterministic Sort (High Score First, then UUID Lexicographical)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].UUID > candidates[j].UUID
	})

	BestNode := candidates[0]
	LeaderUUID = BestNode.UUID
	IsLeader = (LeaderUUID == ServerUUID)

	// Phase 4.2: TDMA Staggering
	// Create sorted list of all valid nodes for slot assignment
	allUUIDs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		allUUIDs = append(allUUIDs, c.UUID)
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
	if totalNodes > 0 {
		slotDuration := 5000 / totalNodes
		PhaseOffset = time.Duration(slotDuration*myRank) * time.Millisecond
	} else {
		PhaseOffset = 0
	}
	
	if IsLeader {
		InfoLog.Printf("👑 I am the Leader. Rank %d/%d", myRank, totalNodes)
	}
}

func CalculateOffset() time.Duration {
	return PhaseOffset
}

func snapshotPeers() {
	ticker := time.NewTicker(60 * time.Second)
	for {
		<-ticker.C
		// In the future: Serialize Peers map to disk/JSON for restart persistence
		// For now, just log stats
		peerLock.RLock()
		InfoLog.Printf("Consensus Report: %d Peers. Leader: %s", len(Peers), LeaderUUID)
		peerLock.RUnlock()
	}
}
