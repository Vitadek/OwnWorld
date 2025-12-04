package main

import (
	"sort"
	"time"
)

func recalculateLeader() {
	peerLock.RLock()
	defer peerLock.RUnlock()

	type Candidate struct {
		UUID      string
		Score     int64
	}
	
	// Use 'Peers', not 'peers'
	myScore := (int64(CurrentTick) << 16) | int64(len(Peers))
	candidates := []Candidate{{UUID: ServerUUID, Score: myScore}}
	
	for _, p := range Peers {
		pScore := (int64(p.LastTick) << 16) | 0 
		candidates = append(candidates, Candidate{UUID: p.UUID, Score: pScore})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].UUID > candidates[j].UUID
	})

	BestNode := candidates[0]
	LeaderUUID = BestNode.UUID
	IsLeader = (LeaderUUID == ServerUUID)

	// TDMA Staggering
	allUUIDs := make([]string, 0, len(Peers)+1)
	allUUIDs = append(allUUIDs, ServerUUID)
	for uuid := range Peers {
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

	// Calculate Phase Offset
	totalNodes := len(allUUIDs)
	if totalNodes > 0 {
		slotDuration := 5000 / totalNodes
		PhaseOffset = time.Duration(slotDuration*myRank) * time.Millisecond
	} else {
		PhaseOffset = 0
	}
}

func snapshotPeers() {
	ticker := time.NewTicker(60 * time.Second)
	for {
		<-ticker.C
		// Atomic Snapshot Logic
		// (Implemented in mapCacheUpdater in main logic, or here)
	}
}
