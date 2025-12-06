package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http" // Removed unused "math/rand"
	"os"
	"regexp"
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

		pubKeyBytes, err := hex.DecodeString(req.PublicKey)
		if err != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
			continue
		}

		newPeer := &Peer{
			UUID:        req.UUID,
			Url:         req.Address,
			PublicKey:   ed25519.PublicKey(pubKeyBytes),
			GenesisHash: req.GenesisHash,
			LastSeen:    time.Now(),
			Relation:    0,
			Reputation:  10.0,
		}

		peerLock.Lock()
		Peers[req.UUID] = newPeer
		peerLock.Unlock()
		
		// Recalculate outside the lock to avoid potential deadlock issues discussed in consensus.go
		go recalculateLeader()
	}
}

func handleHandshake(w http.ResponseWriter, r *http.Request) {
	lr := io.LimitReader(r.Body, 1024*1024)
	body, err := io.ReadAll(lr)
	if err != nil {
		http.Error(w, "Payload Too Large", 413)
		return
	}

	decompressed := decompressLZ4(body)
	var req HandshakeRequest
	json.Unmarshal(decompressed, &req)

	resp := HandshakeResponse{
		Status:   "Queued",
		UUID:     ServerUUID,
		Location: ServerLoc,
	}

	select {
	case immigrationQueue <- req:
		w.WriteHeader(http.StatusAccepted)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	default:
		http.Error(w, "Full", 503)
	}
}

func handleFederationTransaction(w http.ResponseWriter, r *http.Request) {
	lr := io.LimitReader(r.Body, 1024*1024)
	body, err := io.ReadAll(lr)
	if err != nil {
		http.Error(w, "Payload Too Large", 413)
		return
	}

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

	sigHex := hex.EncodeToString(req.Signature)

	SeenTxLock.Lock()
	if SeenCurrent[sigHex] || SeenPrevious[sigHex] {
		SeenTxLock.Unlock()
		w.Write([]byte("ACK_REPLAY"))
		return
	}
	SeenCurrent[sigHex] = true
	SeenTxLock.Unlock()

	stateLock.Lock()
	tickDiff := req.Tick - atomic.LoadInt64(&CurrentTick)
	stateLock.Unlock()

	if tickDiff < -2 {
		http.Error(w, "Transaction Expired", 408)
		return
	}

	_, err = db.Exec("INSERT INTO transaction_log (tick, action_type, payload_blob) VALUES (?, 'FED_TX', ?)", req.Tick, req.Payload)
	if err != nil {
		ErrorLog.Printf("Failed to log federation tx: %v", err)
		http.Error(w, "Internal Error", 500)
		return
	}

	w.Write([]byte("ACK"))
}

func handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	lr := io.LimitReader(r.Body, 1024*1024)
	body, err := io.ReadAll(lr)
	if err != nil {
		return
	}

	decompressed := decompressLZ4(body)

	var req HeartbeatRequest
	if err := json.Unmarshal(decompressed, &req); err != nil {
		http.Error(w, "Bad Payload", 400)
		return
	}

	peerLock.RLock()
	peer, known := Peers[req.UUID]
	peerLock.RUnlock()

	if !known {
		return
	}

	msg := fmt.Sprintf("%s:%d", req.UUID, req.Tick)
	sigBytes, _ := hex.DecodeString(req.Signature)
	if !VerifySignature(peer.PublicKey, []byte(msg), sigBytes) {
		InfoLog.Printf("🚨 BAD SIG from %s. Ignored.", req.UUID)
		return
	}

	peerLock.Lock()
	if p, ok := Peers[req.UUID]; ok {
		p.LastSeen = time.Now()
		p.LastTick = req.Tick
		p.PeerCount = req.PeerCount
		if p.Reputation < 100 {
			p.Reputation += 0.1
		}
	}
	peerLock.Unlock()

	if req.UUID == LeaderUUID {
		syncClock(req.Tick)
	}

	w.Write([]byte("OK"))
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

// New Federation Handler: Reputation Query
// Allows peers to query our local trust score for a specific target UUID.
func handleReputationQuery(w http.ResponseWriter, r *http.Request) {
	// 1. Parse the Target UUID
	target := r.URL.Query().Get("uuid")
	if target == "" {
		http.Error(w, "Missing 'uuid' param", 400)
		return
	}

	// 2. Read Local Opinion
	peerLock.RLock()
	peer, known := Peers[target]
	peerLock.RUnlock()

	score := 0.0
	if known {
		score = peer.Reputation
	}

	// 3. Return JSON
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]float64{"score": score})
}

// --- Client Handlers ---

func generateSessionToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

var usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	json.NewDecoder(r.Body).Decode(&req)

	if len(req.Username) < 3 || len(req.Username) > 20 || !usernameRegex.MatchString(req.Username) {
		http.Error(w, "Invalid Username (Alphanumeric only, 3-20 chars)", 400)
		return
	}

	var storedHash, globalUUID, sysID string
	var sysX, sysY, sysZ int

	err := db.QueryRow("SELECT password_hash, global_uuid FROM users WHERE username=?", req.Username).Scan(&storedHash, &globalUUID)

	if err == nil {
		passHash := hashBLAKE3([]byte(req.Password))
		if storedHash == passHash {
			token := generateSessionToken()
			db.Exec("UPDATE users SET session_token=? WHERE global_uuid=?", token, globalUUID)

			db.QueryRow("SELECT id, x, y, z FROM solar_systems WHERE owner_uuid=?", globalUUID).Scan(&sysID, &sysX, &sysY, &sysZ)

			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":        "logged_in",
				"user_uuid":     globalUUID,
				"session_token": token,
				"system_id":     sysID,
				"location":      []int{sysX, sysY, sysZ},
				"message":       "Welcome back, Commander.",
			})
			return
		} else {
			http.Error(w, "Invalid Credentials", 401)
			return
		}
	}

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	userUUID := hashBLAKE3(pub)
	pubHex := hex.EncodeToString(pub)
	privEnc := encryptKey(priv, req.Password)
	passHash := hashBLAKE3([]byte(req.Password))
	token := generateSessionToken()

	_, err = db.Exec(`INSERT INTO users (global_uuid, username, password_hash, is_local, ed25519_pubkey, ed25519_priv_enc, session_token) 
	                   VALUES (?, ?, ?, 1, ?, ?, ?)`, userUUID, req.Username, passHash, pubHex, privEnc, token)

	if err != nil {
		http.Error(w, "Taken", 400)
		return
	}

	found := false
	var sysXNew, sysYNew, sysZNew int

	// Random Start
	for i := 0; i < 50; i++ {
		uuidBytes, _ := hex.DecodeString(userUUID)
		offX := int(uuidBytes[0]%40) - 20
		offY := int(uuidBytes[1]%40) - 20
		offZ := int(uuidBytes[2]%40) - 20

		sysXNew = ServerLoc[0] + offX
		sysYNew = ServerLoc[1] + offY
		sysZNew = ServerLoc[2] + offZ

		// Use Universal Truth to find valid start
		potential := GetSectorData(sysXNew, sysYNew, sysZNew)

		if potential.HasSystem && potential.SystemType == "G2V" {
			sysID = fmt.Sprintf("sys-%d-%d-%d", sysXNew, sysYNew, sysZNew)
			found = true
			break
		}
	}
	if !found {
		// Fallback to absolute coords near server loc
		sysID = fmt.Sprintf("sys-%d-%d-%d", ServerLoc[0], ServerLoc[1], ServerLoc[2])
		sysXNew, sysYNew, sysZNew = ServerLoc[0], ServerLoc[1], ServerLoc[2]
	}

	db.Exec("INSERT OR IGNORE INTO solar_systems (id, x, y, z, star_type, owner_uuid) VALUES (?, ?, ?, ?, 'G2V', ?)",
		sysID, sysXNew, sysYNew, sysZNew, ServerUUID)

	startBuilds := `{"farm": 5, "iron_mine": 5, "urban_housing": 10}`
	db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, pop_laborers, food, iron, buildings_json) 
	         VALUES (?, ?, ?, 100, 2000, 1000, ?)`, sysID, userUUID, req.Username+" Prime", startBuilds)

	// Updated Fleet Insert: Colonizer Hull with Warp Drive
	modules := `["warp_drive", "warp_drive", "colony_kit"]`
	db.Exec(`INSERT INTO fleets (owner_uuid, status, origin_system, hull_class, modules_json, fuel) 
			 VALUES (?, 'ORBIT', ?, 'Colonizer', ?, 2000)`, userUUID, sysID, modules)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "registered",
		"user_uuid":     userUUID,
		"session_token": token,
		"system_id":     sysID,
		"location":      []int{sysXNew, sysYNew, sysZNew},
		"message":       "Identity Secured. Colony Founded. Ark Ship Ready.",
	})
}

// Scan Sector (Information Economy)
func handleScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetX int `json:"x"`
		TargetY int `json:"y"`
		TargetZ int `json:"z"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	userUUID := r.Header.Get("X-User-UUID")
	token := r.Header.Get("X-Session-Token")
	if userUUID == "" || token == "" {
		http.Error(w, "Unauthorized", 401)
		return
	}

	// 2. REVEAL THE TRUTH
	data := GetSectorData(req.TargetX, req.TargetY, req.TargetZ)

	if !data.HasSystem {
		w.Write([]byte(`{"result": "void", "message": "No significant gravity well detected."}`))
		return
	}

	// 3. RETURN INTEL
	json.NewEncoder(w).Encode(data)
}

func handleFleetLaunch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FleetID      int    `json:"fleet_id"`
		TargetSystem string `json:"target_system"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	stateLock.Lock()
	defer stateLock.Unlock()

	var f Fleet
	var currentSys string
	// Updated Query to fetch modular data
	var modJson string
	err := db.QueryRow("SELECT owner_uuid, origin_system, fuel, status, hull_class, modules_json FROM fleets WHERE id=?", req.FleetID).Scan(&f.OwnerUUID, &currentSys, &f.Fuel, &f.Status, &f.HullClass, &modJson)

	if err != nil {
		http.Error(w, "Fleet Not Found", 404)
		return
	}
	json.Unmarshal([]byte(modJson), &f.Modules)

	if f.Status != "ORBIT" {
		http.Error(w, "Fleet in transit", 400)
		return
	}

	originCoords := GetSystemCoords(currentSys)
	targetCoords := []int{0, 0, 0}
	if len(req.TargetSystem) > 4 && req.TargetSystem[:4] == "sys-" {
		targetCoords = GetSystemCoords(req.TargetSystem)
		if targetCoords[0] == 0 && targetCoords[1] == 0 && targetCoords[2] == 0 {
			fmt.Sscanf(req.TargetSystem, "sys-%d-%d-%d", &targetCoords[0], &targetCoords[1], &targetCoords[2])
		}
	} else {
		targetCoords = originCoords
	}

	mass := 1000
	// FIX: Loop for mass calc was missing variable usage causing compile error?
	// It was `for _, m := range f.Modules` but unused `m`.
	// Corrected to use _, _ or just loop range.
	for range f.Modules {
		mass += 100
	}

	var targetOwner string
	db.QueryRow("SELECT owner_uuid FROM solar_systems WHERE id=?", req.TargetSystem).Scan(&targetOwner)

	cost := CalculateFuelCost(originCoords, targetCoords, mass, targetOwner)

	if cost < 0 {
		http.Error(w, "Cost Overflow", 400)
		return
	}

	if f.Fuel < cost {
		http.Error(w, fmt.Sprintf("Insufficient Fuel. Need %d", cost), 402)
		return
	}

	dist := 0.0
	for i := 0; i < 3; i++ {
		dist += math.Pow(float64(originCoords[i]-targetCoords[i]), 2)
	}
	distance := math.Sqrt(dist)

	warpCount := 0
	for _, m := range f.Modules {
		if m == "warp_drive" {
			warpCount++
		}
	}

	travelTime := int64(distance)
	reduction := float64(travelTime) * (0.2 * float64(warpCount))
	travelTime -= int64(reduction)
	if travelTime < 1 {
		travelTime = 1
	}

	arrivalTick := atomic.LoadInt64(&CurrentTick) + travelTime

	db.Exec(`UPDATE fleets SET status='TRANSIT', fuel=fuel-?, dest_system=?, 
	         departure_tick=?, arrival_tick=? WHERE id=?`,
		cost, req.TargetSystem, atomic.LoadInt64(&CurrentTick), arrivalTick, req.FleetID)

	w.Write([]byte(fmt.Sprintf("Fleet Launched. Cost: %d. Arrival Tick: %d", cost, arrivalTick)))
}

var validResources = map[string]bool{
	"food": true, "water": true, "iron": true, "carbon": true,
	"gold": true, "platinum": true, "uranium": true, "diamond": true,
	"vegetation": true, "oxygen": true, "fuel": true,
	// New ones
	"steel": true, "wine": true,
}

func handleBankBurn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID int    `json:"colony_id"`
		Item     string `json:"item"`
		Amount   int    `json:"amount"`
		UserID   int    `json:"user_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	if req.Amount <= 0 {
		http.Error(w, "Invalid Amount", 400)
		return
	}

	if !validResources[req.Item] {
		http.Error(w, "Invalid Resource", 400)
		return
	}

	eff := GetEfficiency(req.ColonyID, req.Item)
	if eff < 0.1 {
		eff = 0.1
	}

	multiplier := 1.0 / eff
	basePrice := 1.0
	payout := int(float64(req.Amount) * basePrice * multiplier)

	if payout < 0 {
		http.Error(w, "Payout Calculation Overflow", 500)
		return
	}

	stateLock.Lock()
	tx, _ := db.Begin()

	res, _ := tx.Exec(fmt.Sprintf("UPDATE colonies SET %s = %s - ? WHERE id=? AND %s >= ?", req.Item, req.Item, req.Item), req.Amount, req.ColonyID, req.Amount)

	if n, _ := res.RowsAffected(); n == 0 {
		tx.Rollback()
		stateLock.Unlock()
		http.Error(w, "Insufficient Funds", 400)
		return
	}
	tx.Exec("UPDATE users SET credits = credits + ? WHERE id=?", payout, req.UserID)
	tx.Commit()
	stateLock.Unlock()

	w.Write([]byte(fmt.Sprintf("Burned %d for %d credits", req.Amount, payout)))
}

// Logic for Validating Modules
func validateModules(hullClass string, modules []string) bool {
	hull, valid := HullRegistry[hullClass]
	if !valid {
		return false
	}

	engines, weapons, specials := 0, 0, 0

	for _, mod := range modules {
		switch mod {
		case "booster", "propeller", "warp_drive":
			engines++
		case "laser", "railgun":
			weapons++
		case "bomb_bay", "colony_kit":
			specials++
		}
	}

	if engines > hull.EngineSlots {
		return false
	}
	if weapons > hull.WeaponSlots {
		return false
	}
	if specials > hull.SpecialSlots {
		return false
	}

	if specials > 0 && len(modules) > 0 && modules[0] == "bomb_bay" && hullClass != "Bomber" {
		return false
	}

	return true
}

func handleConstruct(w http.ResponseWriter, r *http.Request) {
	// Updated: Now accepts Payload for "Seed" colonization
	var req struct {
		ColonyID  int          `json:"colony_id"`
		HullClass string       `json:"hull_class"`
		Modules   []string     `json:"modules"`
		Payload   FleetPayload `json:"payload"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	// 1. Validate Slots
	if !validateModules(req.HullClass, req.Modules) {
		http.Error(w, "Invalid Configuration", 400)
		return
	}

	// 2. Calculate Hull Cost
	totalIron := 1000
	totalGold := 0
	for _, mod := range req.Modules {
		totalIron += ModuleCosts[mod]
		if mod == "warp_drive" {
			totalGold += 100
		}
	}

	stateLock.Lock()
	defer stateLock.Unlock()

	var c Colony
	var bJson string
	err := db.QueryRow("SELECT buildings_json, system_id, owner_uuid, iron, gold, food, pop_laborers FROM colonies WHERE id=?", req.ColonyID).Scan(&bJson, &c.SystemID, &c.OwnerUUID, &c.Iron, &c.Gold, &c.Food, &c.PopLaborers)

	if err != nil {
		http.Error(w, "Colony Not Found", 404)
		return
	}
	json.Unmarshal([]byte(bJson), &c.Buildings)

	if c.Buildings["shipyard"] < 1 {
		http.Error(w, "Shipyard Required", 400)
		return
	}

	// 3. Calculate Payload Cost (Deduct from Colony)
	neededCrew := 50 // Base crew

	// Add Payload Requirements
	payloadFood := req.Payload.Resources["food"]
	payloadIron := req.Payload.Resources["iron"]
	payloadLabs := req.Payload.PopLaborers

	if payloadFood < 0 || payloadIron < 0 || payloadLabs < 0 {
		http.Error(w, "Negative Payload", 400)
		return
	}

	// Total Check
	if c.Iron < (totalIron+payloadIron) ||
		c.Gold < totalGold ||
		c.Food < payloadFood ||
		c.PopLaborers < (neededCrew+payloadLabs) {
		http.Error(w, "Insufficient Resources for Hull + Payload", 402)
		return
	}

	// 4. Execution
	db.Exec("UPDATE colonies SET iron=iron-?, gold=gold-?, food=food-?, pop_laborers=pop_laborers-? WHERE id=?",
		totalIron+payloadIron, totalGold, payloadFood, neededCrew+payloadLabs, req.ColonyID)

	modJson, _ := json.Marshal(req.Modules)
	payloadJson, _ := json.Marshal(req.Payload) // Serialize Seed

	db.Exec(`INSERT INTO fleets (owner_uuid, status, fuel, origin_system, dest_system, hull_class, modules_json, payload_json) 
			 VALUES (?, 'ORBIT', ?, ?, ?, ?, ?, ?)`,
		c.OwnerUUID, 1000, c.SystemID, c.SystemID, req.HullClass, string(modJson), string(payloadJson))

	w.Write([]byte("Ship Constructed with Payload"))
}

func handleBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID  int    `json:"colony_id"`
		Structure string `json:"structure"`
		Amount    int    `json:"amount"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	if req.Amount <= 0 {
		http.Error(w, "Invalid Amount", 400)
		return
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

	if neededIron < 0 || neededCarbon < 0 {
		http.Error(w, "Cost Overflow Detected", 400)
		return
	}

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

	stateLock.Lock()
	defer stateLock.Unlock()

	var sysID, owner string
	var modJson, payloadJson string
	err := db.QueryRow("SELECT origin_system, owner_uuid, modules_json, payload_json FROM fleets WHERE id=? AND status='ORBIT'", req.FleetID).Scan(&sysID, &owner, &modJson, &payloadJson)

	if err != nil {
		http.Error(w, "Fleet Not Available", 400)
		return
	}

	var modules []string
	json.Unmarshal([]byte(modJson), &modules)

	hasColonyKit := false
	for _, m := range modules {
		if m == "colony_kit" {
			hasColonyKit = true
			break
		}
	}

	if !hasColonyKit {
		http.Error(w, "Fleet lacks Colony Kit module", 400)
		return
	}

	var colonyCount int
	db.QueryRow("SELECT count(*) FROM colonies WHERE system_id=?", sysID).Scan(&colonyCount)
	if colonyCount > 0 {
		http.Error(w, "System Already Colonized", 409)
		return
	}

	// UNPACK PAYLOAD
	var payload FleetPayload
	// Default values if empty payload
	startFood := 100
	startPop := 100
	startIron := 100
	bonusCulture := 0.0

	if payloadJson != "" {
		if err := json.Unmarshal([]byte(payloadJson), &payload); err == nil {
			startFood = payload.Resources["food"]
			startIron = payload.Resources["iron"]
			startPop = payload.PopLaborers
			bonusCulture = payload.CultureBonus
		}
	}

	bJson, _ := json.Marshal(map[string]int{"urban_housing": 10}) // Basic shelter

	// Determine Parent
	var parentID int
	db.QueryRow("SELECT id FROM colonies WHERE owner_uuid=? LIMIT 1", owner).Scan(&parentID)

	_, err = db.Exec(`INSERT INTO colonies (
		system_id, owner_uuid, name, buildings_json, 
		pop_laborers, food, iron, parent_colony_id, stability_target
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sysID, owner, req.Name, string(bJson),
		startPop, startFood, startIron, parentID, 50.0+bonusCulture)

	if err == nil {
		db.Exec("DELETE FROM fleets WHERE id=?", req.FleetID)
		w.Write([]byte("Colony Established"))
	} else {
		http.Error(w, "Deployment Failed", 500)
	}
}

func handleState(w http.ResponseWriter, r *http.Request) {
	userUUID := r.Header.Get("X-User-UUID")
	token := r.Header.Get("X-Session-Token")

	if userUUID == "" || token == "" {
		http.Error(w, "Missing Auth Headers", 401)
		return
	}

	var count int
	err := db.QueryRow("SELECT count(*) FROM users WHERE global_uuid=? AND session_token=?", userUUID, token).Scan(&count)
	if err != nil || count == 0 {
		http.Error(w, "Access Denied (Invalid Session)", 403)
		return
	}

	type Resp struct {
		Colonies []Colony `json:"colonies"`
		Fleets   []Fleet  `json:"fleets"`
		Credits  int      `json:"credits"`
	}
	var resp Resp

	// EMPIRE VIEW: Return ALL colonies
	rows, _ := db.Query(`SELECT id, name, system_id, parent_colony_id, pop_laborers, pop_specialists, pop_elites, food, water, iron, carbon, gold, steel, wine, buildings_json, stability_current FROM colonies WHERE owner_uuid=?`, userUUID)
	defer rows.Close()
	for rows.Next() {
		var c Colony
		var bJson string
		rows.Scan(&c.ID, &c.Name, &c.SystemID, &c.ParentID, &c.PopLaborers, &c.PopSpecialists, &c.PopElites, &c.Food, &c.Water, &c.Iron, &c.Carbon, &c.Gold, &c.Steel, &c.Wine, &bJson, &c.StabilityCurrent)
		json.Unmarshal([]byte(bJson), &c.Buildings)
		resp.Colonies = append(resp.Colonies, c)
	}

	// Updated State Query for Modules + Payload
	fRows, _ := db.Query(`SELECT id, status, origin_system, dest_system, arrival_tick, fuel, hull_class, modules_json, payload_json FROM fleets WHERE owner_uuid=?`, userUUID)
	defer fRows.Close()
	for fRows.Next() {
		var f Fleet
		var modJson, plJson string
		fRows.Scan(&f.ID, &f.Status, &f.OriginSystem, &f.DestSystem, &f.ArrivalTick, &f.Fuel, &f.HullClass, &modJson, &plJson)
		json.Unmarshal([]byte(modJson), &f.Modules)
		if plJson != "" {
			json.Unmarshal([]byte(plJson), &f.Payload)
		}
		resp.Fleets = append(resp.Fleets, f)
	}

	db.QueryRow("SELECT credits FROM users WHERE global_uuid=?", userUUID).Scan(&resp.Credits)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
