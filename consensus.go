package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// --- Heartbeat Loop ---

func startHeartbeatLoop() {
	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {
		broadcastHeartbeat()
		pruneDeadPeers()
		// New: Periodically enforce infamy bans
		if atomic.LoadInt64(&CurrentTick) % 100 == 0 {
			enforceInfamy()
		}
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

	payload := HeartbeatRequest{
		UUID:      ServerUUID,
		Tick:      myTick,
		PeerCount: len(peersList),
		GenHash:   GenesisHash,
	}

	msg := fmt.Sprintf("%s:%d", payload.UUID, payload.Tick)
	sig := SignMessage(PrivateKey, []byte(msg))
	payload.Signature = hex.EncodeToString(sig)

	data, _ := json.Marshal(payload)
	compressed := compressLZ4(data)

	var wg sync.WaitGroup
	for _, p := range peersList {
		if p.Relation == 2 { continue } // Don't broadcast to enemies
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
		if now.Sub(p.LastSeen) > 5*time.Minute {
			InfoLog.Printf("🍂 Pruning dead peer: %s", id)
			delete(Peers, id)
			go recalculateLeader()
		}
	}
}

// --- EigenTrust Implementation ---

// queryPeerOpinion is a stub. In prod, this would hit /api/reputation?target=UUID on the peer.
// For now, we assume peers share their ledger or we approximate from local data.
// Here we return 1.0 (trustworthy) or -1.0 (untrustworthy) based on our local view of that peer.
func queryPeerOpinion(peer *Peer, targetUUID string) float64 {
	// Simplified: We assume trusted peers dislike our enemies.
	// This is a naive implementation; a real one requires a dedicated protocol exchange.
	return 0.0 // Neutral assumption without exchange data
}

// THE EIGENTRUST CALCULATION (The Brain)
func calculateTrust(targetUUID string) float64 {
    peerLock.RLock()
    defer peerLock.RUnlock()

	target, exists := Peers[targetUUID]
	if !exists { return 0.0 }

    // Start with my direct experience (Local Trust)
    directTrust := target.Reputation 

    // Calculate Indirect Trust (What my friends think)
    indirectTrust := 0.0
    totalWeight := 0.0

    for _, peer := range Peers {
        // I only care about opinions from people I TRUST (Rep > 0)
        if peer.Reputation > 0 && peer.UUID != targetUUID {
            // Does this peer have an opinion?
            // In full EigenTrust, we iterate this matrix multiplication.
            // Here we do a single-pass neighbor check.
            theirOpinion := queryPeerOpinion(peer, targetUUID) 
            
            // Weighted Average: My trust in Peer * Peer's trust in Target
            indirectTrust += float64(peer.Reputation) * theirOpinion
            totalWeight += float64(peer.Reputation)
        }
    }

    if totalWeight == 0 { return float64(directTrust) }
    
    // EIGENTRUST FORMULA (Simplified)
    // Trust = (0.5 * Direct) + (0.5 * Indirect)
    return (0.5 * float64(directTrust)) + (0.5 * (indirectTrust / totalWeight))
}

// THE ENFORCEMENT (The Ban Hammer)
func enforceInfamy() {
	peerLock.Lock()
	defer peerLock.Unlock()

    for id, p := range Peers {
		// Skip calculation if already locked
		if p.Relation == 2 { continue }

        trustScore := p.Reputation // Using simplified local rep until full matrix is ready
        
        // The "Soft Barrier" for Raiders
        // If trust drops below -50, we stop routing their packets (Federated Ban)
        if trustScore < -50.0 {
            p.Relation = 2 // Hostile/Ignored
			InfoLog.Printf("🛡️  Peer %s ostracized by EigenTrust consensus.", id)
        }
    }
	recalculateLeader()
}

// Handles Grievance Reports from Federation
func processGrievance(g *GrievanceReport, reporterID string) {
    peerLock.Lock()
    defer peerLock.Unlock()

    reporter, known := Peers[reporterID]
    if !known { return }

    // 1. RELATIVE TRUST CHECK
    // If I don't trust the reporter (Hostile), I ignore their accusation.
    if reporter.Relation == 2 { 
        InfoLog.Printf("Ignoring grievance from hostile peer %s", reporterID)
        return 
    }

    // 2. APPLY INFAMY LOCALLY
    // We only downgrade the offender in *our* ledger.
    if offender, exists := Peers[g.OffenderUUID]; exists {
        
        // Impact scales with damage and the reporter's reputation
        // Trusted reporters cause more reputational damage.
        impact := (float64(g.Damage) / 100.0) * (reporter.Reputation / 10.0)
        if impact < 1.0 { impact = 1.0 } 
        
        offender.Reputation -= impact
        
        // Immediate check
        if offender.Reputation < -50 {
            offender.Relation = 2
            InfoLog.Printf("⚔️ Peer %s declared HOSTILE due to grievance (Rep: %.2f).", offender.UUID, offender.Reputation)
        } else {
             InfoLog.Printf("📉 Peer %s reputation dropped to %.2f (Reported by %s)", offender.UUID, offender.Reputation, reporterID)
        }
    }
}

func recalculateLeader() {
	// peerLock already handled by caller mostly, but enforce safety if called externally
	// Note: Careful of deadlocks if calling from inside a lock.
	// For this snippet, assuming safe context or RLock.
	// Ideally refactor to pass candidate list.
}
