package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	mrand "math/rand"
	"net/http"
	"time"
	"sync/atomic"
	
	// pb "ownworld/pkg/federation" // Uncomment when proto generated
	// "google.golang.org/protobuf/proto"
)

// --- Client Handlers ---

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	json.NewDecoder(r.Body).Decode(&req)
	
	// 1. Generate Identity
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	userUUID := hashBLAKE3(pub) 
	pubHex := hex.EncodeToString(pub)
	// Encrypt private key in real app!
	privHex := hex.EncodeToString(priv) 
	passHash := hashBLAKE3([]byte(req.Password))

	_, err := db.Exec(`INSERT INTO users (global_uuid, username, password_hash, is_local, ed25519_pubkey) 
	                   VALUES (?, ?, ?, 1, ?)`, userUUID, req.Username, passHash, pubHex)
	if err != nil { http.Error(w, "Taken", 400); return }

	// 2. Goldilocks Search
	mrand.Seed(time.Now().UnixNano())
	var sysID string
	found := false
	
	for i := 0; i < 50; i++ {
		id := mrand.Intn(1000000)
		if GetEfficiency(id, "food") > 0.9 {
			sysID = fmt.Sprintf("sys-%d", id)
			found = true
			break
		}
	}
	if !found { sysID = "sys-fallback" }
	
	// Create System
	db.Exec("INSERT OR IGNORE INTO solar_systems (id, owner_uuid) VALUES (?, ?)", sysID, ServerUUID)

	// 3. Create Homestead (Colony)
	// Gives them a base to produce food/iron
	startBuilds := `{"farm": 5, "iron_mine": 5, "urban_housing": 10}`
	db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, pop_laborers, food, iron, buildings_json) 
	         VALUES (?, ?, ?, 100, 2000, 1000, ?)`, sysID, userUUID, req.Username+" Prime", startBuilds)

	// 4. Create Ark Ship (Expansion Tool) - NOT FREE, but for Alpha/Testing maybe give one?
	// User request: "Colony is supposed to start with the ark"
	// Okay, we give one Ark Ship in Orbit to allow immediate expansion.
	db.Exec(`INSERT INTO fleets (owner_uuid, status, origin_system, ark_ship, fuel) 
			 VALUES (?, 'ORBIT', ?, 1, 2000)`, userUUID, sysID)

	json.NewEncoder(w).Encode(map[string]string{"uuid": userUUID, "system": sysID})
}

func handleFleetLaunch(w http.ResponseWriter, r *http.Request) {
	var req struct{ FleetID int; TargetSystem string }
	json.NewDecoder(r.Body).Decode(&req)

	var f Fleet
	var currentSys string
	err := db.QueryRow("SELECT owner_uuid, origin_system, fuel, status FROM fleets WHERE id=?", req.FleetID).Scan(&f.OwnerUUID, &currentSys, &f.Fuel, &f.Status)
	
	if err != nil || f.Status != "ORBIT" {
		http.Error(w, "Invalid Fleet", 400); return
	}

	// Distance Logic (Placeholder)
	dist := 100.0
	fuelCost := int(dist * 10.0)
	
	if f.Fuel < fuelCost {
		http.Error(w, "Insufficient Fuel", 402); return
	}

	arrivalTick := atomic.LoadInt64(&CurrentTick) + int64(dist)
	
	db.Exec(`UPDATE fleets SET status='TRANSIT', fuel=fuel-?, dest_system=?, 
	         arrival_tick=? WHERE id=?`, fuelCost, req.TargetSystem, arrivalTick, req.FleetID)

	w.Write([]byte("Fleet Launched"))
}

func handleBankBurn(w http.ResponseWriter, r *http.Request) {
	var req struct { UserID int; ColonyID int; Resource string; Amount int }
	json.NewDecoder(r.Body).Decode(&req)

	// Scarcity Pricing
	eff := GetEfficiency(req.ColonyID, req.Resource)
	if eff < 0.1 { eff = 0.1 }
	basePrice := 1.0
	payout := int(float64(req.Amount) * basePrice * (1.0/eff))

	tx, _ := db.Begin()
	// Remove Resource
	res, _ := tx.Exec(fmt.Sprintf("UPDATE colonies SET %s = %s - ? WHERE id=? AND %s >= ?", req.Resource, req.Resource, req.Resource), req.Amount, req.ColonyID, req.Amount)
	if n, _ := res.RowsAffected(); n == 0 {
		tx.Rollback(); http.Error(w, "Funds", 400); return
	}
	// Add Credits
	tx.Exec("UPDATE users SET credits = credits + ? WHERE id=?", payout, req.UserID)
	tx.Commit()
	
	json.NewEncoder(w).Encode(map[string]int{"credits": payout})
}

// --- Federation ---

func handleFederationMessage(w http.ResponseWriter, r *http.Request) {
	// Layer 1: Rate Limit (Already in Middleware)
	
	// Layer 2: Dual Mode
	if r.Header.Get("Content-Type") == "application/x-protobuf" {
		// ROBOT PATH
		body, _ := io.ReadAll(r.Body)
		// decompressed := decompressLZ4(body)
		// proto.Unmarshal...
		InfoLog.Printf("Binary Message Received: %d bytes", len(body))
		w.Write([]byte("ACK_BIN"))
		return
	}
	
	// HUMAN PATH
	w.Write([]byte("ACK_JSON"))
}
