package main

import (
	"sort"
	"time"
)

// CalculateOffset determines the TDMA sleep slice to prevent CPU spikes
func CalculateOffset() time.Duration {
    peerLock.RLock()
    defer peerLock.RUnlock()

    // 1. Collect all known nodes
    allUUIDs := make([]string, 0, len(peers)+1)
    allUUIDs = append(allUUIDs, ServerUUID)
    for uuid := range peers {
        allUUIDs = append(allUUIDs, uuid)
    }
    
    // 2. Sort deterministically
    sort.Strings(allUUIDs)

    // 3. Find My Rank
    myRank := 0
    for i, id := range allUUIDs {
        if id == ServerUUID {
            myRank = i
            break
        }
    }

    // 4. Calculate Slice (5000ms / NodeCount)
    totalNodes := len(allUUIDs)
    if totalNodes == 0 { totalNodes = 1 } // Safety
    slice := 5000 / totalNodes
    
    return time.Duration(slice * myRank) * time.Millisecond
}

// Phase 4.1: Leader Election & Score Logic
func recalculateLeader() {
	peerLock.RLock()
	defer peerLock.RUnlock()

	type Candidate struct {
		UUID      string
		Score     int64
	}
	
	myScore := (int64(CurrentTick) << 16) | int64(len(peers))
	candidates := []Candidate{{UUID: ServerUUID, Score: myScore}}
	
	for _, p := range peers {
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
}

func snapshotPeers() {
	ticker := time.NewTicker(60 * time.Second)
	for {
		<-ticker.C
		peerLock.RLock()
		// Basic snapshot logic would go here
		peerLock.RUnlock()
	}
}
