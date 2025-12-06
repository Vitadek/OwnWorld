package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"
)

// --- Helper: Auth ---
func authenticate(r *http.Request) (string, error) {
	userUUID := r.Header.Get("X-User-UUID")
	token := r.Header.Get("X-Session-Token")

	if userUUID == "" || token == "" {
		return "", fmt.Errorf("Missing Auth Headers")
	}

	var count int
	err := db.QueryRow("SELECT count(*) FROM users WHERE global_uuid=? AND session_token=?", userUUID, token).Scan(&count)
	if err != nil || count == 0 {
		return "", fmt.Errorf("Access Denied")
	}
	return userUUID, nil
}

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

	// Process Payload: Grievance Report
	var grievance GrievanceReport
	if err := json.Unmarshal(req.Payload, &grievance); err == nil && grievance.OffenderUUID != "" {
		processGrievance(&grievance, req.UUID)
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

func handleReputationQuery(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("uuid")
	if target == "" {
		http.Error(w, "Missing 'uuid' param", 400)
		return
	}

	peerLock.RLock()
	peer, known := Peers[target]
	peerLock.RUnlock()

	score := 0.0
	if known {
		score = peer.Reputation
	}

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

// NOTE: Use the handleRegister from the previous debug step (which works), 
// but ensure to include the other handlers below.
func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	json.NewDecoder(r.Body).Decode(&req)

	DebugLog.Printf("📝 Handling Register Request for: %s", req.Username)

	if len(req.Username) < 3 || len(req.Username) > 20 || !usernameRegex.MatchString(req.Username) {
		DebugLog.Printf("❌ Invalid Username format: %s", req.Username)
		http.Error(w, "Invalid Username (Alphanumeric only, 3-20 chars)", 400)
		return
	}

	var storedHash, globalUUID, sysID string
	var sysX, sysY, sysZ int

	// 1. Check if user exists
	err := db.QueryRow("SELECT password_hash, global_uuid FROM users WHERE username=?", req.Username).Scan(&storedHash, &globalUUID)

	if err == nil {
		DebugLog.Printf("👤 User %s found in DB. Attempting Login...", req.Username)
		passHash := hashBLAKE3([]byte(req.Password))
		if storedHash == passHash {
			token := generateSessionToken()
			db.Exec("UPDATE users SET session_token=? WHERE global_uuid=?", token, globalUUID)

			// Fetch Location
			errLoc := db.QueryRow("SELECT id, x, y, z FROM solar_systems WHERE owner_uuid=?", globalUUID).Scan(&sysID, &sysX, &sysY, &sysZ)
			if errLoc != nil {
				DebugLog.Printf("⚠️ Login Successful but NO SYSTEM found for %s! (Zombie State)", req.Username)
			} else {
				DebugLog.Printf("✅ Login Successful. System: %s", sysID)
			}

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
			DebugLog.Printf("⛔ Login Failed: Wrong Password for %s", req.Username)
			http.Error(w, "Invalid Credentials", 401)
			return
		}
	}

	// 2. New Registration
	DebugLog.Printf("🆕 User %s not found. Creating new Identity...", req.Username)
	
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	userUUID := hashBLAKE3(pub)
	pubHex := hex.EncodeToString(pub)
	privEnc := encryptKey(priv, req.Password)
	passHash := hashBLAKE3([]byte(req.Password))
	token := generateSessionToken()

	_, err = db.Exec(`INSERT INTO users (global_uuid, username, password_hash, is_local, ed25519_pubkey, ed25519_priv_enc, session_token) 
	                   VALUES (?, ?, ?, 1, ?, ?, ?)`, userUUID, req.Username, passHash, pubHex, privEnc, token)

	if err != nil {
		DebugLog.Printf("❌ DB Insert Failed for User %s: %v", req.Username, err)
		http.Error(w, "Taken", 400)
		return
	}
	DebugLog.Printf("✅ User inserted. UUID: %s", userUUID)

	// 3. Find Starting Location
	found := false
	var sysXNew, sysYNew, sysZNew int
	
	// Safety Check for Race Condition
	if ServerLoc == nil {
		DebugLog.Printf("🚨 CRITICAL: ServerLoc is nil! Defaulting to 0,0,0")
		ServerLoc = []int{0, 0, 0}
	} else {
		DebugLog.Printf("📍 ServerLoc is valid: %v", ServerLoc)
	}

	DebugLog.Printf("🔍 Searching for valid sector near %v...", ServerLoc)
	for i := 0; i < 50; i++ {
		uuidBytes, _ := hex.DecodeString(userUUID)
		offX := int(uuidBytes[0]%40) - 20
		offY := int(uuidBytes[1]%40) - 20
		offZ := int(uuidBytes[2]%40) - 20

		sysXNew = ServerLoc[0] + offX
		sysYNew = ServerLoc[1] + offY
		sysZNew = ServerLoc[2] + offZ

		potential := GetSectorData(sysXNew, sysYNew, sysZNew)

		if potential.HasSystem && potential.SystemType == "G2V" {
			sysID = fmt.Sprintf("sys-%d-%d-%d", sysXNew, sysYNew, sysZNew)
			found = true
			DebugLog.Printf("✨ Found Habitable G2V System: %s", sysID)
			break
		}
	}
	if !found {
		sysID = fmt.Sprintf("sys-%d-%d-%d", ServerLoc[0], ServerLoc[1], ServerLoc[2])
		sysXNew, sysYNew, sysZNew = ServerLoc[0], ServerLoc[1], ServerLoc[2]
		DebugLog.Printf("⚠️ No random spot found. Forcing spawn at Cluster Center: %s", sysID)
	}

	// 4. Create Infrastructure
	_, errSys := db.Exec("INSERT OR IGNORE INTO solar_systems (id, x, y, z, star_type, owner_uuid) VALUES (?, ?, ?, ?, 'G2V', ?)",
		sysID, sysXNew, sysYNew, sysZNew, ServerUUID) // Note: Owner is Server, not Player (Players own Colonies)
	if errSys != nil { DebugLog.Printf("❌ Failed to create Solar System: %v", errSys) }

	startBuilds := `{"farm": 5, "iron_mine": 5, "urban_housing": 10}`
	_, errCol := db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, pop_laborers, food, iron, buildings_json) 
	         VALUES (?, ?, ?, 100, 2000, 1000, ?)`, sysID, userUUID, req.Username+" Prime", startBuilds)
	
	if errCol != nil {
		DebugLog.Printf("❌ Failed to create Colony: %v", errCol)
	} else {
		DebugLog.Printf("🏰 Colony Created Successfully")
	}

	modules := `["warp_drive", "warp_drive", "colony_kit"]`
	_, errFleet := db.Exec(`INSERT INTO fleets (owner_uuid, status, origin_system, hull_class, modules_json, fuel) 
			 VALUES (?, 'ORBIT', ?, 'Colonizer', ?, 2000)`, userUUID, sysID, modules)
	
	if errFleet != nil {
		DebugLog.Printf("❌ Failed to create Starter Fleet: %v", errFleet)
	} else {
		DebugLog.Printf("🚀 Starter Fleet Created")
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "registered",
		"user_uuid":     userUUID,
		"session_token": token,
		"system_id":     sysID,
		"location":      []int{sysXNew, sysYNew, sysZNew},
		"message":       "Identity Secured. Colony Founded. Ark Ship Ready.",
	})
}

func handleScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetX int `json:"x"`
		TargetY int `json:"y"`
		TargetZ int `json:"z"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	_, err := authenticate(r)
	if err != nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

	data := GetSectorData(req.TargetX, req.TargetY, req.TargetZ)

	if !data.HasSystem {
		w.Write([]byte(`{"result": "void", "message": "No significant gravity well detected."}`))
		return
	}

	json.NewEncoder(w).Encode(data)
}

func handleFleetLaunch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FleetID      int    `json:"fleet_id"`
		TargetSystem string `json:"target_system"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	userID, err := authenticate(r)
	if err != nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

	stateLock.Lock()
	defer stateLock.Unlock()

	var f Fleet
	var currentSys string
	var modJson string
	err = db.QueryRow("SELECT owner_uuid, origin_system, fuel, status, hull_class, modules_json FROM fleets WHERE id=?", req.FleetID).Scan(&f.OwnerUUID, &currentSys, &f.Fuel, &f.Status, &f.HullClass, &modJson)

	if err != nil {
		http.Error(w, "Fleet Not Found", 404)
		return
	}
	
	if f.OwnerUUID != userID {
		http.Error(w, "Not your fleet", 403)
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
	"steel": true, "wine": true,
}

func handleBankBurn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID int    `json:"colony_id"`
		Item     string `json:"item"`
		Amount   int    `json:"amount"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	userID, err := authenticate(r)
	if err != nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

	if req.Amount <= 0 {
		http.Error(w, "Invalid Amount", 400)
		return
	}

	if !validResources[req.Item] {
		http.Error(w, "Invalid Resource", 400)
		return
	}

	stateLock.Lock()
	defer stateLock.Unlock()

	var owner string
	err = db.QueryRow("SELECT owner_uuid FROM colonies WHERE id=?", req.ColonyID).Scan(&owner)
	if err != nil || owner != userID {
		http.Error(w, "Access Denied", 403)
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

	tx, _ := db.Begin()

	res, _ := tx.Exec(fmt.Sprintf("UPDATE colonies SET %s = %s - ? WHERE id=? AND %s >= ?", req.Item, req.Item, req.Item), req.Amount, req.ColonyID, req.Amount)

	if n, _ := res.RowsAffected(); n == 0 {
		tx.Rollback()
		http.Error(w, "Insufficient Funds", 400)
		return
	}
	tx.Exec("UPDATE users SET credits = credits + ? WHERE global_uuid=?", payout, userID)
	tx.Commit()

	w.Write([]byte(fmt.Sprintf("Burned %d for %d credits", req.Amount, payout)))
}

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
	var req struct {
		ColonyID  int          `json:"colony_id"`
		HullClass string       `json:"hull_class"`
		Modules   []string     `json:"modules"`
		Payload   FleetPayload `json:"payload"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	userID, err := authenticate(r)
	if err != nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

	if !validateModules(req.HullClass, req.Modules) {
		http.Error(w, "Invalid Configuration", 400)
		return
	}

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
	err = db.QueryRow("SELECT buildings_json, system_id, owner_uuid, iron, gold, food, pop_laborers FROM colonies WHERE id=?", req.ColonyID).Scan(&bJson, &c.SystemID, &c.OwnerUUID, &c.Iron, &c.Gold, &c.Food, &c.PopLaborers)

	if err != nil {
		http.Error(w, "Colony Not Found", 404)
		return
	}
	
	if c.OwnerUUID != userID {
		http.Error(w, "Access Denied", 403)
		return
	}

	json.Unmarshal([]byte(bJson), &c.Buildings)

	if c.Buildings["shipyard"] < 1 {
		http.Error(w, "Shipyard Required", 400)
		return
	}

	neededCrew := 50

	payloadFood := req.Payload.Resources["food"]
	payloadIron := req.Payload.Resources["iron"]
	payloadLabs := req.Payload.PopLaborers

	if payloadFood < 0 || payloadIron < 0 || payloadLabs < 0 {
		http.Error(w, "Negative Payload", 400)
		return
	}
	
	for _, v := range req.Payload.Resources {
		if v < 0 {
			http.Error(w, "Negative Payload Resource", 400)
			return
		}
	}

	if c.Iron < (totalIron+payloadIron) ||
		c.Gold < totalGold ||
		c.Food < payloadFood ||
		c.PopLaborers < (neededCrew+payloadLabs) {
		http.Error(w, "Insufficient Resources for Hull + Payload", 402)
		return
	}

	db.Exec("UPDATE colonies SET iron=iron-?, gold=gold-?, food=food-?, pop_laborers=pop_laborers-? WHERE id=?",
		totalIron+payloadIron, totalGold, payloadFood, neededCrew+payloadLabs, req.ColonyID)

	modJson, _ := json.Marshal(req.Modules)
	payloadJson, _ := json.Marshal(req.Payload)

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

	userID, err := authenticate(r)
	if err != nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

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
	err = db.QueryRow("SELECT buildings_json, iron, carbon, owner_uuid FROM colonies WHERE id=?", req.ColonyID).Scan(&bJson, &c.Iron, &c.Carbon, &c.OwnerUUID)
	if err != nil {
		http.Error(w, "Colony Not Found", 404)
		return
	}

	if c.OwnerUUID != userID {
		http.Error(w, "Access Denied", 403)
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

	userID, err := authenticate(r)
	if err != nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

	stateLock.Lock()
	defer stateLock.Unlock()

	var sysID, owner string
	var modJson, payloadJson string
	err = db.QueryRow("SELECT origin_system, owner_uuid, modules_json, payload_json FROM fleets WHERE id=? AND status='ORBIT'", req.FleetID).Scan(&sysID, &owner, &modJson, &payloadJson)

	if err != nil {
		http.Error(w, "Fleet Not Available", 400)
		return
	}

	if owner != userID {
		http.Error(w, "Access Denied", 403)
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

	var payload FleetPayload
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

	bJson, _ := json.Marshal(map[string]int{"urban_housing": 10})

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

// NOTE: This function was modified to include debugging and safe initialization
func handleState(w http.ResponseWriter, r *http.Request) {
	userID, err := authenticate(r)
	if err != nil {
		if DebugLog != nil {
			DebugLog.Printf("⛔ handleState Auth Failed: %v", err)
		}
		http.Error(w, "Unauthorized", 401)
		return
	}

	if DebugLog != nil {
		DebugLog.Printf("🔍 Fetching State for User: %s", userID)
	}

	type Resp struct {
		Colonies []Colony `json:"colonies"`
		Fleets   []Fleet  `json:"fleets"`
		Credits  int      `json:"credits"`
	}
	var resp Resp

	// Initialize slices to empty (not nil) to ensure JSON [] instead of null
	resp.Colonies = []Colony{}
	resp.Fleets = []Fleet{}

	rows, err := db.Query(`SELECT id, name, system_id, parent_colony_id, pop_laborers, pop_specialists, pop_elites, food, water, iron, carbon, gold, steel, wine, buildings_json, stability_current FROM colonies WHERE owner_uuid=?`, userID)
	if err != nil {
		if DebugLog != nil {
			DebugLog.Printf("❌ DB Query Error (Colonies): %v", err)
		}
	} else {
		defer rows.Close()
		for rows.Next() {
			var c Colony
			var bJson string
			err := rows.Scan(&c.ID, &c.Name, &c.SystemID, &c.ParentID, &c.PopLaborers, &c.PopSpecialists, &c.PopElites, &c.Food, &c.Water, &c.Iron, &c.Carbon, &c.Gold, &c.Steel, &c.Wine, &bJson, &c.StabilityCurrent)
			if err != nil {
				if DebugLog != nil {
					DebugLog.Printf("❌ DB Scan Error (Colony ID %d): %v", c.ID, err)
				}
				continue
			}
			json.Unmarshal([]byte(bJson), &c.Buildings)
			resp.Colonies = append(resp.Colonies, c)
		}
	}
	
	if DebugLog != nil {
		DebugLog.Printf("🏰 Found %d colonies", len(resp.Colonies))
	}

	fRows, err := db.Query(`SELECT id, status, origin_system, dest_system, arrival_tick, fuel, hull_class, modules_json, payload_json FROM fleets WHERE owner_uuid=?`, userID)
	if err != nil {
		if DebugLog != nil {
			DebugLog.Printf("❌ DB Query Error (Fleets): %v", err)
		}
	} else {
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
	}
	
	if DebugLog != nil {
		DebugLog.Printf("🚀 Found %d fleets", len(resp.Fleets))
	}

	db.QueryRow("SELECT credits FROM users WHERE global_uuid=?", userID).Scan(&resp.Credits)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleCargoTransfer(w http.ResponseWriter, r *http.Request) {
    var req struct {
        FleetID   int            `json:"fleet_id"`
        ColonyID  int            `json:"colony_id"`
        Transfers map[string]int `json:"transfers"` 
    }
    json.NewDecoder(r.Body).Decode(&req)

	userID, err := authenticate(r)
	if err != nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

    stateLock.Lock()
    defer stateLock.Unlock()

    var f Fleet
    var fPayloadJson string
    errF := db.QueryRow("SELECT owner_uuid, origin_system, status, payload_json FROM fleets WHERE id=?", req.FleetID).Scan(&f.OwnerUUID, &f.OriginSystem, &f.Status, &fPayloadJson)
    
    var c Colony
    errC := db.QueryRow("SELECT owner_uuid, system_id, food, iron, steel, wine FROM colonies WHERE id=?", req.ColonyID).Scan(&c.OwnerUUID, &c.SystemID, &c.Food, &c.Iron, &c.Steel, &c.Wine)

    if errF != nil || errC != nil {
        http.Error(w, "Invalid Fleet or Colony ID", 404)
        return
    }

	if f.OwnerUUID != userID || c.OwnerUUID != userID {
		http.Error(w, "Access Denied", 403)
		return
	}

    if f.OwnerUUID != c.OwnerUUID || f.Status != "ORBIT" || f.OriginSystem != c.SystemID {
        http.Error(w, "Transfer Rejected: Invalid Location or Ownership", 403)
        return
    }

    if fPayloadJson != "" {
        json.Unmarshal([]byte(fPayloadJson), &f.Payload)
    }
    if f.Payload.Resources == nil {
        f.Payload.Resources = make(map[string]int)
    }

    for item, amount := range req.Transfers {
        if amount == 0 { continue }
        
        colonyVal := 0
        switch item {
        case "food": colonyVal = c.Food
        case "iron": colonyVal = c.Iron
        case "steel": colonyVal = c.Steel
        case "wine": colonyVal = c.Wine
        default: continue
        }

        fleetVal := f.Payload.Resources[item]

        if amount > 0 {
            if colonyVal < amount {
                http.Error(w, fmt.Sprintf("Insufficient %s in Colony", item), 400)
                return 
            }
        } else {
            qtyToUnload := -amount
            if fleetVal < qtyToUnload {
                http.Error(w, fmt.Sprintf("Insufficient %s in Fleet", item), 400)
                return
            }
        }
    }

    tx, _ := db.Begin()
    
    for item, amount := range req.Transfers {
        f.Payload.Resources[item] += amount
        
		if f.Payload.Resources[item] < 0 {
			tx.Rollback()
			http.Error(w, "Cargo Overflow", 500)
			return
		}

        query := fmt.Sprintf("UPDATE colonies SET %s = %s - ? WHERE id=?", item, item)
        if _, err := tx.Exec(query, amount, req.ColonyID); err != nil {
            tx.Rollback()
            http.Error(w, "Database Error during transfer", 500)
            return
        }
    }

    newPayloadJson, _ := json.Marshal(f.Payload)
    tx.Exec("UPDATE fleets SET payload_json = ? WHERE id=?", string(newPayloadJson), req.FleetID)
    
    tx.Commit()
    w.Write([]byte("Cargo Transfer Complete"))
}

func handleSetPolicy(w http.ResponseWriter, r *http.Request) {
    var req struct {
        ColonyID int             `json:"colony_id"`
        Policies map[string]bool `json:"policies"`
    }
    json.NewDecoder(r.Body).Decode(&req)

	userID, err := authenticate(r)
	if err != nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

    stateLock.Lock()
    defer stateLock.Unlock()

    var owner string
    err = db.QueryRow("SELECT owner_uuid FROM colonies WHERE id=?", req.ColonyID).Scan(&owner)
    if err != nil { 
        http.Error(w, "Colony Not Found", 404)
        return
    }

	if owner != userID {
		http.Error(w, "Access Denied", 403)
		return
	}

    policyJson, _ := json.Marshal(req.Policies)
    _, err = db.Exec("UPDATE colonies SET policies_json=? WHERE id=?", string(policyJson), req.ColonyID)
    
    if err != nil {
        http.Error(w, "Failed to set policies", 500)
        return
    }
    w.Write([]byte("Policies Updated"))
}
