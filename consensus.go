package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings" 
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
		if atomic.LoadInt64(&CurrentTick)%100 == 0 {
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

    // Gather Market Orders to Gossip (Last 5 created locally or new ones)
    // Simple logic: Fetch active orders
    var orders []MarketOrder
    rows, _ := db.Query("SELECT order_id, seller_uuid, item, quantity, price, is_buy, origin_system, expires_tick FROM market_orders WHERE expires_tick > ? ORDER BY rowid DESC LIMIT 5", myTick)
    if rows != nil {
        defer rows.Close()
        for rows.Next() {
            var o MarketOrder
            rows.Scan(&o.ID, &o.SellerUUID, &o.Item, &o.Quantity, &o.Price, &o.IsBuy, &o.OriginSystem, &o.ExpiresTick)
            orders = append(orders, o)
        }
    }

	payload := HeartbeatRequest{
		UUID:      ServerUUID,
		Tick:      myTick,
		PeerCount: len(peersList),
		GenHash:   GenesisHash,
        MarketOrders: orders, // Attach Market Gossip
	}

	msg := fmt.Sprintf("%s:%d", payload.UUID, payload.Tick)
	sig := SignMessage(PrivateKey, []byte(msg))
	payload.Signature = hex.EncodeToString(sig)

	data, _ := json.Marshal(payload)
	compressed := compressLZ4(data)

	var wg sync.WaitGroup
	for _, p := range peersList {
		if p.Relation == 2 {
			continue
		} // Don't broadcast to enemies
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

// REPLACED: Real Implementation (Network Call)
func queryPeerOpinion(peer *Peer, targetUUID string) float64 {
	// 1. Construct URL
	// Assumes peer.Url is "http://ip:port" or similar
	targetURL := fmt.Sprintf("%s/federation/reputation?uuid=%s", peer.Url, targetUUID)
	if !strings.HasPrefix(peer.Url, "http") {
		targetURL = fmt.Sprintf("http://%s/federation/reputation?uuid=%s", peer.Url, targetUUID)
	}

	// 2. Make Request (Short Timeout)
	// We use a short timeout (1s) because we query many peers; we can't hang on one.
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(targetURL)
	if err != nil {
		// If peer is unreachable, we assume Neutral (0.0) to avoid biasing the score.
		return 0.0
	}
	defer resp.Body.Close()

	// 3. Parse Response
	var data struct {
		Score float64 `json:"score"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0.0
	}

	return data.Score
}

// THE EIGENTRUST CALCULATION (The Brain)
func calculateTrust(targetUUID string) float64 {
	// Note: peerLock should be held by caller (enforceInfamy)
	target, exists := Peers[targetUUID]
	if !exists {
		return 0.0
	}

	// Start with my direct experience (Local Trust)
	directTrust := target.Reputation

	// Calculate Indirect Trust (What my friends think)
	indirectTrust := 0.0
	totalWeight := 0.0

	for _, peer := range Peers {
		// I only care about opinions from people I TRUST (Rep > 0)
		if peer.Reputation > 0 && peer.UUID != targetUUID {
			theirOpinion := queryPeerOpinion(peer, targetUUID)

			// Weighted Average
			indirectTrust += float64(peer.Reputation) * theirOpinion
			totalWeight += float64(peer.Reputation)
		}
	}

	if totalWeight == 0 {
		return float64(directTrust)
	}

	// EIGENTRUST FORMULA (Simplified)
	return (0.5 * float64(directTrust)) + (0.5 * (indirectTrust / totalWeight))
}

// THE ENFORCEMENT (The Ban Hammer)
func enforceInfamy() {
	peerLock.Lock()
	defer peerLock.Unlock()

	for id, p := range Peers {
		if p.Relation == 2 {
			continue
		}

		trustScore := p.Reputation // Using simplified local rep until full matrix is ready

		if trustScore < -50.0 {
			p.Relation = 2 // Hostile/Ignored
			InfoLog.Printf("🛡️  Peer %s ostracized by EigenTrust consensus.", id)
		}
	}
	// We do NOT call recalculateLeader here to avoid deadlock if recalculateLeader needs RLock
	// Instead, recalculateLeader is called independently or when peers change.
}

// Handles Grievance Reports from Federation
func processGrievance(g *GrievanceReport, reporterID string) {
	peerLock.Lock()
	defer peerLock.Unlock()

	reporter, known := Peers[reporterID]
	if !known {
		return
	}

	// 1. RELATIVE TRUST CHECK
	if reporter.Relation == 2 {
		InfoLog.Printf("Ignoring grievance from hostile peer %s", reporterID)
		return
	}

	// 2. APPLY INFAMY LOCALLY
	if offender, exists := Peers[g.OffenderUUID]; exists {
		impact := (float64(g.Damage) / 100.0) * (reporter.Reputation / 10.0)
		if impact < 1.0 {
			impact = 1.0
		}

		offender.Reputation -= impact

		if offender.Reputation < -50 {
			offender.Relation = 2
			InfoLog.Printf("⚔️ Peer %s declared HOSTILE due to grievance (Rep: %.2f).", offender.UUID, offender.Reputation)
		} else {
			InfoLog.Printf("📉 Peer %s reputation dropped to %.2f (Reported by %s)", offender.UUID, offender.Reputation, reporterID)
		}
	}
}

// Restored: Logic to determine leader based on score
func recalculateLeader() {
	type Candidate struct {
		UUID  string
		Score int64
	}

	// My score
	myScore := (atomic.LoadInt64(&CurrentTick) << 16) | int64(100*1000)
	candidates := []Candidate{{UUID: ServerUUID, Score: myScore}}

	// We need to read Peers. If caller held lock, we can't RLock.
	// Let's assume callers DO NOT hold lock when calling this.
	peerLock.RLock()
	for _, p := range Peers {
		if p.Reputation < -50 {
			continue
		} // Skip Hostile/Banned nodes

		// Simple trust score usage
		trustScore := int64(p.Reputation)
		if p.Relation == 2 {
			trustScore = 0
		}

		pScore := (p.LastTick << 16) | (trustScore * 1000)
		candidates = append(candidates, Candidate{UUID: p.UUID, Score: pScore})
	}
	peerLock.RUnlock()

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].UUID > candidates[j].UUID
	})

	if len(candidates) > 0 {
		BestNode := candidates[0]
		LeaderUUID = BestNode.UUID
		IsLeader = (LeaderUUID == ServerUUID)

		// TDMA Calculation
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
	}
}

// Restored: CalculateOffset used by simulation loop
func CalculateOffset() time.Duration {
	return PhaseOffset
}

// Restored: SyncClock used by handlers
func syncClock(leaderTick int64) {
	myTick := atomic.LoadInt64(&CurrentTick)
	delta := leaderTick - myTick

	if delta > 10 {
		InfoLog.Printf("⚠️ Major Desync (Delta %d). Snapping to Tick %d", delta, leaderTick)
		atomic.StoreInt64(&CurrentTick, leaderTick)
		return
	}

	baseDuration := int64(5000)
	adjustment := int64(0)

	if delta > 0 {
		adjustment = -50
	} else if delta < 0 {
		adjustment = 50
	}

	newDuration := baseDuration + adjustment

	if newDuration < MinTickDuration {
		newDuration = MinTickDuration
	}
	if newDuration > MaxTickDuration {
		newDuration = MaxTickDuration
	}

	atomic.StoreInt64(&TickDuration, newDuration)
}
