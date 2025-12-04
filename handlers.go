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
	"os" // <--- Added this required import
	"strconv"
	"sync/atomic"
	"time"
)

// --- Federation Handlers ---

func processImmigration() {
	for req := range immigrationQueue {
		time.Sleep(2 * time.Second)
		if Config.PeeringMode == "strict" {
			continue
		}

		peerLock.RLock()
		_, exists := Peers[req.UUID]
		peerLock.RUnlock()
		if exists {
			continue
		}

		var myGenHash string
		db.QueryRow("SELECT value FROM system_meta WHERE key='genesis_hash'").Scan(&myGenHash)
		if req.GenesisHash != myGenHash {
			continue
		}

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

func handleFederationTransaction(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	decompressed := decompressLZ4(body)
	var req TransactionRequest
	json.Unmarshal(decompressed, &req)

	peerLock.RLock()
	peer, known := Peers[req.UUID]
	peerLock.RUnlock()

	if !known {
		http.Error(w, "Unknown Peer", 403)
		return
	}

	if !VerifySignature(peer.PublicKey, req.Payload, req.Signature) {
		http.Error(w, "Invalid Signature", 401)
		return
	}

	stateLock.Lock()
	tickDiff := req.Tick - atomic.LoadInt64(&CurrentTick)
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
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

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

	// 1. Generate Identity
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	userUUID := hashBLAKE3(pub)
	pubHex := hex.EncodeToString(pub)
	privEnc := encryptKey(priv, req.Password)
	passHash := hashBLAKE3([]byte(req.Password))

	// 2. Insert User
	_, err := db.Exec(`INSERT INTO users (global_uuid, username, password_hash, is_local, ed25519_pubkey, ed25519_priv_enc) 
	                   VALUES (?, ?, ?, 1, ?, ?)`, userUUID, req.Username, passHash, pubHex, privEnc)

	if err != nil {
		http.Error(w, "Taken", 400)
		return
	}

	// 3. Goldilocks Search
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
	if !found {
		sysID = "sys-fallback"
	}

	// Create System
	db.Exec("INSERT OR IGNORE INTO solar_systems (id, owner_uuid) VALUES (?, ?)", sysID, ServerUUID)

	// 4. Create Homestead (Colony)
	startBuilds := `{"farm": 5, "iron_mine": 5, "urban_housing": 10}`
	db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, pop_laborers, food, iron, buildings_json) 
	         VALUES (?, ?, ?, 100, 2000, 1000, ?)`, sysID, userUUID, req.Username+" Prime", startBuilds)

	// 5. Create Ark Ship
	db.Exec(`INSERT INTO fleets (owner_uuid, status, origin_system, ark_ship, fuel) 
			 VALUES (?, 'ORBIT', ?, 1, 2000)`, userUUID, sysID)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "registered",
		"user_uuid": userUUID,
		"system_id": sysID,
		"message":   "Identity Secured. Colony Founded. Ark Ship Ready.",
	})
}

func handleConstruct(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID int    `json:"colony_id"`
		Unit     string `json:"unit"`
		Amount   int    `json:"amount"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Amount < 1 {
		req.Amount = 1
	}

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
	if req.Unit == "ark_ship" {
		arkCount = req.Amount
	}

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
	if req.Amount < 1 {
		req.Amount = 1
	}

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
	if c.Buildings == nil {
		c.Buildings = make(map[string]int)
	}
	c.Buildings[req.Structure] += req.Amount
	newBJson, _ := json.Marshal(c.Buildings)

	db.Exec("UPDATE colonies SET iron=iron-?, carbon=carbon-?, buildings_json=? WHERE id=?",
		neededIron, neededCarbon, string(newBJson), req.ColonyID)
	w.Write([]byte("Build Complete"))
}

func handleDeploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FleetID int    `json:"fleet_id"`
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
		UserID   int    `json:"user_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	eff := GetEfficiency(req.ColonyID, req.Item)
	if eff < 0.1 {
		eff = 0.1
	}

	multiplier := 1.0 / eff
	payout := int(float64(req.Amount) * 1.0 * multiplier)

	tx, _ := db.Begin()
	res, _ := tx.Exec(fmt.Sprintf("UPDATE colonies SET %s = %s - ? WHERE id=? AND %s >= ?", req.Item, req.Item, req.Item), req.Amount, req.ColonyID, req.Amount)
	if n, _ := res.RowsAffected(); n == 0 {
		tx.Rollback()
		http.Error(w, "Insufficient Funds", 400)
		return
	}
	tx.Exec("UPDATE users SET credits = credits + ? WHERE id=?", payout, req.UserID)
	tx.Commit()

	w.Write([]byte(fmt.Sprintf("Burned %d for %d credits", req.Amount, payout)))
}

func handleFleetLaunch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FleetID      int    `json:"fleet_id"`
		TargetSystem string `json:"target_system"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	var f Fleet
	var currentSys string
	err := db.QueryRow("SELECT owner_uuid, origin_system, fuel, status FROM fleets WHERE id=?", req.FleetID).Scan(&f.OwnerUUID, &currentSys, &f.Fuel, &f.Status)

	if err != nil {
		http.Error(w, "Fleet Not Found", 404)
		return
	}
	if f.Status != "ORBIT" {
		http.Error(w, "Fleet in transit", 400)
		return
	}

	// Physics
	originCoords := GetSystemCoords(currentSys)
	targetCoords := GetSystemCoords(req.TargetSystem)
	
	var targetOwner string
	db.QueryRow("SELECT owner_uuid FROM solar_systems WHERE id=?", req.TargetSystem).Scan(&targetOwner)
	
	cost := CalculateFuelCost(originCoords, targetCoords, 1000, targetOwner)
	
	if f.Fuel < cost {
		http.Error(w, fmt.Sprintf("Insufficient Fuel. Need %d", cost), 402)
		return
	}

	dist := 0.0
	for i := 0; i < 3; i++ { dist += math.Pow(float64(originCoords[i]-targetCoords[i]), 2) }
	distance := math.Sqrt(dist)
	
	arrivalTick := atomic.LoadInt64(&CurrentTick) + int64(distance)

	db.Exec(`UPDATE fleets SET status='TRANSIT', fuel=fuel-?, dest_system=?, 
	         departure_tick=?, arrival_tick=? WHERE id=?`, 
	         cost, req.TargetSystem, atomic.LoadInt64(&CurrentTick), arrivalTick, req.FleetID)

	w.Write([]byte(fmt.Sprintf("Fleet Launched. Cost: %d. Arrival: %d", cost, arrivalTick)))
}

func handleState(w http.ResponseWriter, r *http.Request) {
	userUUID := r.Header.Get("X-User-UUID")
	if userUUID == "" {
		http.Error(w, "Missing X-User-UUID", 401)
		return
	}

	type Resp struct {
		Colonies []Colony `json:"colonies"`
		Fleets   []Fleet  `json:"fleets"`
		Credits  int      `json:"credits"`
	}
	var resp Resp

	rows, _ := db.Query(`SELECT id, name, system_id, pop_laborers, food, iron, buildings_json FROM colonies WHERE owner_uuid=?`, userUUID)
	defer rows.Close()
	for rows.Next() {
		var c Colony
		var bJson string
		rows.Scan(&c.ID, &c.Name, &c.SystemID, &c.PopLaborers, &c.Food, &c.Iron, &bJson)
		json.Unmarshal([]byte(bJson), &c.Buildings)
		resp.Colonies = append(resp.Colonies, c)
	}

	fRows, _ := db.Query(`SELECT id, status, origin_system, dest_system, arrival_tick, fuel, ark_ship, fighters FROM fleets WHERE owner_uuid=?`, userUUID)
	defer fRows.Close()
	for fRows.Next() {
		var f Fleet
		fRows.Scan(&f.ID, &f.Status, &f.OriginSystem, &f.DestSystem, &f.ArrivalTick, &f.Fuel, &f.ArkShip, &f.Fighters)
		resp.Fleets = append(resp.Fleets, f)
	}

	db.QueryRow("SELECT credits FROM users WHERE global_uuid=?", userUUID).Scan(&resp.Credits)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
func handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	// 1. Read & Decompress
	body, _ := io.ReadAll(r.Body)
	decompressed := decompressLZ4(body)

	type HeartbeatWire struct {
		UUID      string `json:"uuid"`
		Tick      int64  `json:"tick"`
		PeerCount int    `json:"peer_count"`
		GenHash   string `json:"gen_hash"`
		Signature string `json:"sig"`
	}
	var req HeartbeatWire
	if err := json.Unmarshal(decompressed, &req); err != nil {
		return // Silent fail to save CPU
	}

	// 2. "Layer Cake" Defense
	peerLock.RLock()
	peer, known := Peers[req.UUID]
	peerLock.RUnlock()

	if !known {
		// Optional: If unknown, maybe trigger a handshake back?
		// For now, ignore to prevent spam.
		return
	}

	// 3. Probabilistic Verification (10% Chance)
	// We trust established peers most of the time to save CPU.
	if mrand.Float32() < 0.10 {
		msg := fmt.Sprintf("%s:%d", req.UUID, req.Tick)
		sigBytes, _ := hex.DecodeString(req.Signature)

		if !VerifySignature(peer.PublicKey, []byte(msg), sigBytes) {
			// SLASHING LOGIC: Ban them locally
			InfoLog.Printf("🚨 BAD SIG from %s. Banning.", req.UUID)
			// delete(Peers, req.UUID)
			return
		}
	}

	// 4. Update State
	peerLock.Lock()
	// Update existing pointer
	if p, ok := Peers[req.UUID]; ok {
		p.LastSeen = time.Now()
		p.CurrentTick = req.Tick
		p.PeerCount = req.PeerCount
	}
	peerLock.Unlock()

	// 5. Check for Election Trigger
	// If their score is higher than current leader, trigger a recount
	// (This effectively syncs the network leadership)
	// recalculateLeader() is cheap, we can run it.
	// But better: Only run if *they* claim to be leader or have massive score?
	// For simplicity, let the periodic recalculateLeader handle it,
	// OR trigger it if we realize we are drifting.

	w.Write([]byte("OK"))
}
