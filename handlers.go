package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"
)

// Phase 3.2: Immigration Queue Worker
func processImmigration() {
	for req := range immigrationQueue {
		// Rate limit immigration to prevent CPU stalling
		time.Sleep(2 * time.Second)

		if Config.PeeringMode == "strict" { continue }

		peerLock.RLock()
		_, exists := peers[req.UUID]
		peerLock.RUnlock()
		if exists { continue }

		// Genesis Match Check
		var myGenHash string
		db.QueryRow("SELECT value FROM system_meta WHERE key='genesis_hash'").Scan(&myGenHash)
		if req.GenesisHash != myGenHash {
			continue
		}

		// Add Peer Logic (simplified for brevity, assuming Peer struct exists)
		// In a real implementation, you would decode the public key and add to 'peers' map
		InfoLog.Printf("IMMIGRATION: Peer %s processing...", req.UUID)
		recalculateLeader()
	}
}

func handleHandshake(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	decompressed := decompressLZ4(body)
	var req HandshakeRequest
	json.Unmarshal(decompressed, &req)
	select {
	case immigrationQueue <- req:
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte("Queued"))
	default:
		http.Error(w, "Full", 503)
	}
}

func handleFederationTransaction(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	decompressed := decompressLZ4(body)
	var req TransactionRequest
	json.Unmarshal(decompressed, &req)

	peerLock.RLock()
	peer, known := peers[req.UUID]
	peerLock.RUnlock()
	
	if !known { http.Error(w, "Unknown Peer", 403); return }

	// Phase 3.2: Strict Signature Verification
	if !VerifySignature(peer.PublicKey, req.Payload, req.Signature) {
		ErrorLog.Printf("SECURITY: Invalid Signature from %s", req.UUID)
		http.Error(w, "Invalid Signature", 401)
		return
	}

	// Phase 4.3: Transaction Window (Lag Switch Protection)
	stateLock.Lock()
	tickDiff := req.Tick - CurrentTick
	stateLock.Unlock()

	if tickDiff < -2 {
		http.Error(w, "Transaction Expired (Lag > 2 ticks)", 408)
		return
	}
	
	w.Write([]byte("ACK"))
}

// Phase 6.2: Atomic Map Snapshot
func handleMap(w http.ResponseWriter, r *http.Request) {
	data := mapSnapshot.Load()
	if data == nil {
		w.Write([]byte("[]"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data.([]byte))
}

// Phase 5.1: Homestead Start
func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	json.NewDecoder(r.Body).Decode(&req)
	hash := hashBLAKE3([]byte(req.Password))

	var count int
	db.QueryRow("SELECT count(*) FROM users WHERE username=?", req.Username).Scan(&count)
	if count > 0 {
		http.Error(w, "Taken", 400)
		return
	}

	res, _ := db.Exec("INSERT INTO users (username, password_hash, is_local) VALUES (?,?, 1)", req.Username, hash)
	uid, _ := res.LastInsertId()

	// Goldilocks Search
	var sysUUID string
	found := false
	
	for i := 0; i < 50; i++ {
		tempID := rand.Intn(1000000)
		foodEff := GetEfficiency(tempID, "food")
		ironEff := GetEfficiency(tempID, "iron")
		
		if foodEff > 0.9 && ironEff > 0.8 {
			sysUUID = fmt.Sprintf("sys-%d-%d", tempID, rand.Intn(999))
			found = true
			break
		}
	}
	if !found {
		sysUUID = fmt.Sprintf("sys-%s-fallback", "user")
	}

	// Create System & Colony (Stubbed for brevity)
	// In full version, insert into solar_systems and colonies tables
	
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "registered", "user_id": uid, "system_id": sysUUID})
}

// Phase 5.2: Economy & Arbitrage
func handleBankBurn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID int    `json:"colony_id"`
		Item     string `json:"item"`
		Amount   int    `json:"amount"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	
	// Base Prices
	BasePrices := map[string]int{"iron": 1, "carbon": 2, "water": 1, "gold": 50}
	basePrice, ok := BasePrices[req.Item]
	if !ok { basePrice = 1 }

	eff := GetEfficiency(req.ColonyID, req.Item)
	if eff < 0.1 { eff = 0.1 } 
	
	multiplier := 1.0 / eff
	payout := int(float64(basePrice) * multiplier * float64(req.Amount))
	
	// Update DB (Stubbed)
	w.Write([]byte(fmt.Sprintf("Burned %d %s for %d credits", req.Amount, req.Item, payout)))
}

// Phase 5.4: Fuel & Topology
func handleFleetLaunch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FleetID    int    `json:"fleet_id"`
		DestSystem string `json:"dest_system"`
		Distance   int    `json:"distance"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	// isPeer check (Stubbed)
	isPeer := false
	
	cost := req.Distance * 10
	if isPeer { cost = int(float64(cost) * 2.5) }
	
	// Update DB (Stubbed)
	w.Write([]byte("Fleet Launched"))
}

func handleBuild(w http.ResponseWriter, r *http.Request) {
    // Basic stub to satisfy main.go reference
    w.Write([]byte("Build"))
}

func handleState(w http.ResponseWriter, r *http.Request) {
    // Basic stub to satisfy main.go reference
    w.Write([]byte("{}"))
}

func handleSyncLedger(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	decompressed := decompressLZ4(body)
	var req LedgerPayload
	json.Unmarshal(decompressed, &req)

	peerLock.Lock()
	defer peerLock.Unlock()

	if p, ok := peers[req.UUID]; ok {
		p.LastTick = req.Tick
		p.LastHash = req.Entry.FinalHash
		p.LastSeen = time.Now()
	}
	w.WriteHeader(http.StatusOK)
}
