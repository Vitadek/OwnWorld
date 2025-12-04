package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	mrand "math/rand" // Aliased to avoid conflict with crypto/rand
	"net/http"
	"os"
	"strconv"
	"time"

    pb "ownworld/pkg/federation" 
    "google.golang.org/protobuf/proto"
)

// --- Federation Handlers ---

func processImmigration() {
	for req := range immigrationQueue {
		time.Sleep(2 * time.Second)
		if Config.PeeringMode == "strict" { continue }

		peerLock.RLock()
		_, exists := peers[req.UUID]
		peerLock.RUnlock()
		if exists { continue }

		var myGenHash string
		db.QueryRow("SELECT value FROM system_meta WHERE key='genesis_hash'").Scan(&myGenHash)
		if req.GenesisHash != myGenHash { continue }

		InfoLog.Printf("IMMIGRATION: Peer %s joined.", req.UUID)
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

// The New Dual-Mode Handler
func handleFederationMessage(w http.ResponseWriter, r *http.Request) {
    // 1. Check Content-Type for Protobuf
    if r.Header.Get("Content-Type") == "application/x-protobuf" {
        // --- ROBOT PATH (Protobuf) ---
        body, _ := io.ReadAll(r.Body)
        
        // Decompress LZ4 (assuming middleware handled this or we do it here)
        // For V3.1 strictness, let's assume raw body for now or decompress if needed
        // rawProto := decompressLZ4(body) 
        
        var packet pb.Packet
        if err := proto.Unmarshal(body, &packet); err != nil {
            http.Error(w, "Bad Proto", 400)
            return
        }

        // Verify Signature logic would go here...

        switch inner := packet.Content.(type) {
        case *pb.Packet_Heartbeat:
            InfoLog.Printf("Proto Heartbeat from %s: Tick %d", packet.SenderUuid, inner.Heartbeat.Tick)
        case *pb.Packet_FleetMove:
            InfoLog.Printf("Incoming Fleet from %s", packet.SenderUuid)
        }
        w.Write([]byte("ACK_PROTO"))
        return
    }

    // --- HUMAN PATH (JSON Fallback) ---
    handleFederationTransaction(w, r)
}

// The Original JSON Handler (Fallback)
func handleFederationTransaction(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	decompressed := decompressLZ4(body)
	var req TransactionRequest
	json.Unmarshal(decompressed, &req)

	peerLock.RLock()
	peer, known := peers[req.UUID]
	peerLock.RUnlock()
	
	if !known { http.Error(w, "Unknown Peer", 403); return }

	if !VerifySignature(peer.PublicKey, req.Payload, req.Signature) {
		http.Error(w, "Invalid Signature", 401)
		return
	}

	stateLock.Lock()
	tickDiff := req.Tick - CurrentTick
	stateLock.Unlock()

	if tickDiff < -2 {
		http.Error(w, "Transaction Expired", 408)
		return
	}
	
	w.Write([]byte("ACK"))
}

func handleMap(w http.ResponseWriter, r *http.Request) {
	data := mapSnapshot.Load()
	if data == nil {
		w.Write([]byte("[]"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data.([]byte))
}

func handleSyncLedger(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Fed-Key") != os.Getenv("FEDERATION_KEY") {
		http.Error(w, "Unauthorized", 401)
		return
	}

	sinceDay, _ := strconv.Atoi(r.URL.Query().Get("since_day"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 { limit = 50 }
	if limit > 100 { limit = 100 }

	rows, err := db.Query(`SELECT day_id, state_blob, final_hash 
	                       FROM daily_snapshots 
	                       WHERE day_id > ? 
	                       ORDER BY day_id ASC 
	                       LIMIT ?`, sinceDay, limit)
	if err != nil {
		http.Error(w, "DB Error", 500)
		return
	}
	defer rows.Close()

	type SnapshotItem struct {
		DayID     int    `json:"day_id"`
		Blob      []byte `json:"blob"`
		FinalHash string `json:"hash"`
	}

	var history []SnapshotItem
	for rows.Next() {
		var h SnapshotItem
		if err := rows.Scan(&h.DayID, &h.Blob, &h.FinalHash); err != nil {
			continue
		}
		history = append(history, h)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(history)
}

// --- Client Handlers ---

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	json.NewDecoder(r.Body).Decode(&req)
	
	hash := hashBLAKE3([]byte(req.Password))

	var count int
	db.QueryRow("SELECT count(*) FROM users WHERE username=?", req.Username).Scan(&count)
	if count > 0 {
		http.Error(w, "Username Taken", 400)
		return
	}

    // 1. Generate Global Identity (The Fix)
    pubKey, _, _ := ed25519.GenerateKey(rand.Reader)
    userUUID := hashBLAKE3(pubKey)
    pubKeyStr := hex.EncodeToString(pubKey)

	res, _ := db.Exec(`INSERT INTO users (global_uuid, username, password_hash, is_local, ed25519_pubkey) 
                       VALUES (?, ?, ?, 1, ?)`, userUUID, req.Username, hash, pubKeyStr)
	uid, _ := res.LastInsertId()

	// 2. Goldilocks Search
	var sysID string
	found := false
	mrand.Seed(time.Now().UnixNano())
	
	// Server "Location" anchor (0,0,0 for now)
	serverX, serverY, serverZ := 0, 0, 0

	for i := 0; i < 50; i++ {
		x := serverX + mrand.Intn(100) - 50
		y := serverY + mrand.Intn(100) - 50
		z := serverZ + mrand.Intn(100) - 50
		
		tempID := fmt.Sprintf("sys-%d-%d-%d", x, y, z)
		
		// GetEfficiency is in simulation.go
		if GetEfficiency(x*1000+y, "food") > 0.9 && GetEfficiency(x*1000+y, "iron") > 0.8 {
			sysID = tempID
			found = true
			break
		}
	}
	if !found {
		sysID = fmt.Sprintf("sys-%s-fallback", req.Username)
	}

	db.Exec("INSERT OR IGNORE INTO solar_systems (id, x, y, z, star_type, owner_uuid) VALUES (?,?,?,?, 'G2V', ?)", 
		sysID, mrand.Intn(100), mrand.Intn(100), mrand.Intn(100), ServerUUID)

	// 3. Spawn Homestead + Ark
	startBuilds := `{"farm": 5, "well": 5, "urban_housing": 10}`
	db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, pop_laborers, food, iron, buildings_json) 
	         VALUES (?, ?, ?, 100, 5000, 1000, ?)`, sysID, userUUID, req.Username+" Prime", startBuilds)

	db.Exec(`INSERT INTO fleets (owner_uuid, status, origin_system, ark_ship, fuel) 
			 VALUES (?, 'ORBIT', ?, 1, 5000)`, userUUID, sysID)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "registered", 
		"user_id": uid, 
        "user_uuid": userUUID,
		"system_id": sysID,
        "message": "Identity Secured. Ark Ship Ready.",
	})
}

func handleConstruct(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID int    `json:"colony_id"`
		Unit     string `json:"unit"`
		Amount   int    `json:"amount"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Amount < 1 { req.Amount = 1 }

	costs, ok := UnitCosts[req.Unit]
	if !ok {
		http.Error(w, "Unknown Unit", 400)
		return
	}

	stateLock.Lock()
	defer stateLock.Unlock()

	var c Colony
	var bJson string
	err := db.QueryRow("SELECT buildings_json, system_id, owner_uuid, iron, food, fuel, pop_laborers FROM colonies WHERE id=?", req.ColonyID).Scan(&bJson, &c.SystemID, &c.OwnerUUID, &c.Iron, &c.Food, &c.Fuel, &c.PopLaborers)
	
	if err == nil {
		c.Buildings = make(map[string]int)
		json.Unmarshal([]byte(bJson), &c.Buildings)
	} else {
		http.Error(w, "Colony Not Found", 404)
		return
	}
	
	if c.Buildings["shipyard"] < 1 {
		http.Error(w, "Shipyard Required", 400)
		return
	}

	neededIron := costs["iron"] * req.Amount
	neededFood := costs["food"] * req.Amount
	neededFuel := costs["fuel"] * req.Amount
	neededCrew := costs["pop_laborers"] * req.Amount

	if c.Iron < neededIron || c.Food < neededFood || c.Fuel < neededFuel || c.PopLaborers < neededCrew {
		http.Error(w, "Insufficient Resources/Crew", 402)
		return
	}

	db.Exec("UPDATE colonies SET iron=iron-?, food=food-?, fuel=fuel-?, pop_laborers=pop_laborers-? WHERE id=?",
		neededIron, neededFood, neededFuel, neededCrew, req.ColonyID)

	arkCount := 0
	if req.Unit == "ark_ship" { arkCount = req.Amount }
	
	db.Exec(`INSERT INTO fleets (owner_uuid, status, fuel, origin_system, dest_system, ark_ship) 
			 VALUES (?, 'ORBIT', ?, ?, ?, ?)`, 
			 c.OwnerUUID, 1000, c.SystemID, c.SystemID, arkCount)

	w.Write([]byte("Construction Complete"))
}

func handleBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID  int    `json:"colony_id"`
		Structure string `json:"structure"`
		Amount    int    `json:"amount"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Amount < 1 { req.Amount = 1 }
	
	cost, ok := BuildingCosts[req.Structure]
	if !ok {
		http.Error(w, "Unknown Structure", 400)
		return
	}

	stateLock.Lock()
	defer stateLock.Unlock()

	var c Colony
	var bJson string
	err := db.QueryRow("SELECT buildings_json, iron, carbon FROM colonies WHERE id=?", req.ColonyID).Scan(&bJson, &c.Iron, &c.Carbon)
	if err != nil {
		http.Error(w, "Colony Not Found", 404)
		return
	}

	neededIron := cost["iron"] * req.Amount
	neededCarbon := cost["carbon"] * req.Amount
	
	if c.Iron < neededIron || c.Carbon < neededCarbon {
		http.Error(w, "Insufficient Resources", 402)
		return
	}

	json.Unmarshal([]byte(bJson), &c.Buildings)
	if c.Buildings == nil { c.Buildings = make(map[string]int) }
	c.Buildings[req.Structure] += req.Amount
	newBJson, _ := json.Marshal(c.Buildings)

	db.Exec("UPDATE colonies SET iron=iron-?, carbon=carbon-?, buildings_json=? WHERE id=?", 
		neededIron, neededCarbon, string(newBJson), req.ColonyID)
	w.Write([]byte("Build Complete"))
}

func handleDeploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FleetID int `json:"fleet_id"`
		Name    string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	
	var sysID, owner string
	var arkCount int
	err := db.QueryRow("SELECT origin_system, owner_uuid, ark_ship FROM fleets WHERE id=? AND status='ORBIT'", req.FleetID).Scan(&sysID, &owner, &arkCount)
	if err != nil || arkCount < 1 {
		http.Error(w, "No Ark Available", 400)
		return
	}

	var colonyCount int
	db.QueryRow("SELECT count(*) FROM colonies WHERE system_id=?", sysID).Scan(&colonyCount)
	if colonyCount > 0 {
		http.Error(w, "System Already Colonized", 409)
		return
	}

	bJson, _ := json.Marshal(map[string]int{"farm": 5, "well": 5, "urban_housing": 10})
	_, err = db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, buildings_json, pop_laborers, water, food, iron) 
	         VALUES (?, ?, ?, ?, 1000, 5000, 5000, 500)`, sysID, owner, req.Name, string(bJson))
	
	if err == nil {
		db.Exec("DELETE FROM fleets WHERE id=?", req.FleetID)
		w.Write([]byte("Colony Established"))
	} else {
		http.Error(w, "Deployment Failed", 500)
	}
}

func handleBankBurn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID int    `json:"colony_id"`
		Item     string `json:"item"`
		Amount   int    `json:"amount"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	
	BasePrices := map[string]int{"iron": 1, "carbon": 2, "water": 1, "gold": 50}
	basePrice, ok := BasePrices[req.Item]
	if !ok { basePrice = 1 }

	eff := GetEfficiency(req.ColonyID, req.Item)
	if eff < 0.1 { eff = 0.1 } 
	
	multiplier := 1.0 / eff
	payout := int(float64(basePrice) * multiplier * float64(req.Amount))
	
	w.Write([]byte(fmt.Sprintf("Burned %d %s for %d credits", req.Amount, req.Item, payout)))
}

func handleFleetLaunch(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Launch Stub"))
}

func handleState(w http.ResponseWriter, r *http.Request) {
	// Simple dump for now, secure in production
	w.Write([]byte("{}"))
}
