package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"
)

// --- Configuration ---

// BasePrices defines the global standard value for items before local scarcity is applied.
var BasePrices = map[string]int{
	"iron":       1,
	"carbon":     2,
	"water":      1,
	"vegetation": 2, // Food
	"gold":       50,
	"platinum":   100,
}

// --- Federation Handlers ---

func processImmigration() {
	for req := range immigrationQueue {
		time.Sleep(2 * time.Second)

		if Config.PeeringMode == "strict" {
			continue
		}

		peerLock.RLock()
		_, exists := peers[req.UUID]
		peerLock.RUnlock()
		if exists {
			continue
		}
		var myGenHash string
		db.QueryRow("SELECT value FROM system_meta WHERE key='genesis_hash'").Scan(&myGenHash)
		if req.GenesisHash != myGenHash {
			continue
		}
		pubBytes, _ := hex.DecodeString(req.PublicKey)
		peer := &Peer{
			UUID: req.UUID, Address: req.Address, LastSeen: time.Now(),
			PublicKey: ed25519.PublicKey(pubBytes),
			Status:    "VERIFIED",
		}
		peerLock.Lock()
		peers[req.UUID] = peer
		peerLock.Unlock()
		InfoLog.Printf("IMMIGRATION: Peer %s added.", req.UUID)
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
	if err := json.Unmarshal(decompressed, &req); err != nil {
		http.Error(w, "Bad JSON", 400)
		return
	}

	peerLock.RLock()
	peer, known := peers[req.UUID]
	peerLock.RUnlock()
	if !known {
		http.Error(w, "Unknown Peer", 403)
		return
	}

	if !VerifySignature(peer.PublicKey, req.Payload, req.Signature) {
		ErrorLog.Printf("SECURITY: Invalid Signature from %s", req.UUID)
		http.Error(w, "Invalid Signature", 401)
		return
	}

	stateLock.Lock()
	tickDiff := req.Tick - CurrentTick
	stateLock.Unlock()

	if tickDiff < -5 {
		http.Error(w, "Transaction Expired (Lag)", 408)
		return
	}
	if tickDiff > 2 {
		http.Error(w, "Transaction Future (Clock Skew)", 400)
		return
	}

	switch req.Type {
	case "FLEET_ARRIVAL":
		var f Fleet
		json.Unmarshal(req.Payload, &f)
		InfoLog.Printf("FLEET ARRIVAL: Fleet %d from %s arrived at %s", f.ID, req.UUID, f.DestSystem)
		db.Exec(`INSERT INTO fleets (owner_uuid, status, fuel, origin_system, dest_system, ark_ship, fighters, frigates, haulers) 
		         VALUES (?, 'ORBIT', ?, ?, ?, ?, ?, ?, ?)`,
			f.OwnerUUID, f.Fuel, f.OriginSystem, f.DestSystem, f.ArkShip, f.Fighters, f.Frigates, f.Haulers)
		w.Write([]byte("Fleet Docked"))

	default:
		http.Error(w, "Unknown Type", 400)
	}
}

func snapshotPeers() {
	ticker := time.NewTicker(60 * time.Second)
	for {
		<-ticker.C
		peerLock.RLock()

		list := make([]Peer, 0, len(peers))
		for _, p := range peers {
			list = append(list, *p)
		}
		peerLock.RUnlock()

		data, _ := json.Marshal(list)
		mapSnapshot.Store(data)
	}
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

// --- Client Handlers ---

// Phase 5.1: The Homestead Start (Goldilocks Search)
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

	// 1. Create User
	res, _ := db.Exec("INSERT INTO users (username, password_hash, is_local) VALUES (?,?, 1)", req.Username, hash)
	uid, _ := res.LastInsertId()

	// 2. Goldilocks Search: Find a viable planet
	// We loop 50 times trying to find a planet with Vegetation Efficiency > 0.9.
	// This ensures new players don't spawn on "dead" rocks.
	var sysUUID string
	var x, y, z int
	
	rand.Seed(time.Now().UnixNano())
	found := false

	for i := 0; i < 50; i++ {
		// Temporary ID for checking efficiency
		tempID := rand.Intn(1000000)
		eff := GetEfficiency(tempID, "vegetation") // "vegetation" = food source
		
		if eff > 0.9 {
			sysUUID = fmt.Sprintf("sys-%d-%d", uid, tempID)
			x, y, z = rand.Intn(100)-50, rand.Intn(100)-50, rand.Intn(100)-50
			found = true
			break
		}
	}

	// Fallback if super unlucky
	if !found {
		sysUUID = fmt.Sprintf("sys-%d-fallback", uid)
		x, y, z = 0, 0, 0
	}

	// 3. Create World
	db.Exec("INSERT INTO solar_systems (id, x, y, z, star_type, owner_uuid) VALUES (?,?,?,?, 'G2V', ?)", sysUUID, x, y, z, ServerUUID)
	// We use ID 1 for calculation simplicity in this MVP, but in prod we'd use LastInsertId
	db.Exec("INSERT INTO planets (system_id, efficiency_seed, type) VALUES (?, ?, 'TERRAN')", sysUUID, "SEED")
	
	// 4. Create Colony
	bJson, _ := json.Marshal(map[string]int{"farm": 5, "well": 5, "urban_housing": 10})
	db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, buildings_json, pop_laborers, water, vegetation, iron) 
	         VALUES (?, ?, ?, ?, 1000, 5000, 5000, 500)`, sysUUID, req.Username, req.Username+"'s Prime", string(bJson))

	// 5. Spawn Ark Ship (New for Phase 5.1)
	// Players start with a "Lifeboat" fleet in orbit.
	db.Exec(`INSERT INTO fleets (owner_uuid, status, fuel, origin_system, dest_system, ark_ship) 
			 VALUES (?, 'ORBIT', 1000, ?, ?, 1)`, req.Username, sysUUID, sysUUID)

	json.NewEncoder(w).Encode(map[string]interface{}{"status": "registered", "user_id": uid, "system_id": sysUUID})
}

// Phase 5.2: The Bank (Scarcity Arbitrage)
func handleBankBurn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID int    `json:"colony_id"`
		Item     string `json:"item"` // iron, carbon, gold
		Amount   int    `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad JSON", 400)
		return
	}

	if req.Amount < 1 {
		http.Error(w, "Amount must be positive", 400)
		return
	}

	basePrice, ok := BasePrices[req.Item]
	if !ok {
		http.Error(w, "Bank does not accept this item", 400)
		return
	}

	stateLock.Lock()
	defer stateLock.Unlock()

	// 1. Check Balance
	var currentAmount int
	var userID int
	// Note: We are using string building for column name. In prod, whitelist this to prevent injection.
	// Since we checked BasePrices map above, req.Item is safe.
	query := fmt.Sprintf("SELECT %s, (SELECT id FROM users WHERE username=owner_uuid) FROM colonies WHERE id=?", req.Item)
	err := db.QueryRow(query, req.ColonyID).Scan(&currentAmount, &userID)
	if err != nil {
		http.Error(w, "Colony not found", 404)
		return
	}

	if currentAmount < req.Amount {
		http.Error(w, "Insufficient Resources", 402)
		return
	}

	// 2. Calculate Payout (Scarcity Logic)
	// Payout = BasePrice * (1.0 / LocalEfficiency)
	// High Efficiency (Abundant) -> Low Payout
	// Low Efficiency (Scarce) -> High Payout
	eff := GetEfficiency(req.ColonyID, req.Item) // using ColonyID as proxy for PlanetID
	multiplier := 1.0
	if eff > 0.1 {
		multiplier = 1.0 / eff
	} else {
		multiplier = 10.0 // Cap max multiplier
	}
	
	payoutPerUnit := float64(basePrice) * multiplier
	totalCredits := int(payoutPerUnit * float64(req.Amount))

	// 3. Execute Transaction
	tx, _ := db.Begin()
	updateCol := fmt.Sprintf("UPDATE colonies SET %s = %s - ? WHERE id=?", req.Item, req.Item)
	tx.Exec(updateCol, req.Amount, req.ColonyID)
	tx.Exec("UPDATE users SET credits = credits + ? WHERE id=?", totalCredits, userID)
	tx.Commit()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"burned": req.Amount, 
		"credits": totalCredits, 
		"rate": payoutPerUnit,
	})
}

// Phase 5.4 Helper: Fuel Logic
func calculateFuelCost(distance int, isPeer bool) int {
	// Base cost per unit distance
	cost := distance * 10 
	
	if isPeer {
		// Federation Treaty: 2.5x cost
		return int(float64(cost) * 2.5)
	}
	// Hostile Space: 10x cost
	return cost * 10
}

func handleFleetLaunch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FleetID    int    `json:"fleet_id"`
		DestSystem string `json:"dest_system"`
		Distance   int    `json:"distance"` // Simplification: Client calculates distance for now
	}
	json.NewDecoder(r.Body).Decode(&req)

	stateLock.Lock()
	defer stateLock.Unlock()

	var f Fleet
	err := db.QueryRow("SELECT id, fuel, status FROM fleets WHERE id=?", req.FleetID).Scan(&f.ID, &f.Fuel, &f.Status)
	if err != nil || f.Status != "ORBIT" {
		http.Error(w, "Invalid Fleet", 400)
		return
	}

	// Fuel Calc
	// Check if destination is a known peer
	// For MVP, we assume if it's not local, it's unknown/hostile unless mapped
	isPeer := false // Lookup logic here
	cost := calculateFuelCost(req.Distance, isPeer)

	if f.Fuel < cost {
		http.Error(w, fmt.Sprintf("Insufficient Fuel. Need %d", cost), 402)
		return
	}

	arrivalTick := CurrentTick + req.Distance
	db.Exec("UPDATE fleets SET status='TRANSIT', dest_system=?, arrival_tick=?, fuel=fuel-? WHERE id=?", 
		req.DestSystem, arrivalTick, cost, f.ID)
	
	w.Write([]byte("Fleet Launched"))
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
	err := db.QueryRow("SELECT buildings_json, iron, carbon, water FROM colonies WHERE id=?", req.ColonyID).Scan(&bJson, &c.Iron, &c.Carbon, &c.Water)
	if err != nil {
		http.Error(w, "Colony Not Found", 404)
		return
	}
	neededIron := cost["iron"] * req.Amount
	neededCarbon := cost["carbon"] * req.Amount
	if c.Iron < neededIron || c.Carbon < neededCarbon {
		http.Error(w, "Insufficient Funds", 402)
		return
	}
	json.Unmarshal([]byte(bJson), &c.Buildings)
	if c.Buildings == nil {
		c.Buildings = make(map[string]int)
	}
	c.Buildings[req.Structure] += req.Amount
	newBJson, _ := json.Marshal(c.Buildings)
	db.Exec("UPDATE colonies SET iron=iron-?, carbon=carbon-?, buildings_json=? WHERE id=?", neededIron, neededCarbon, string(newBJson), req.ColonyID)
	w.Write([]byte("Build Complete"))
}
