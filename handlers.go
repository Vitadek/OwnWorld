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

	var sysUUID string
	var x, y, z int
	
	rand.Seed(time.Now().UnixNano())
	found := false

	// Homestead Goldilocks Search
	for i := 0; i < 50; i++ {
		tempID := rand.Intn(1000000)
		eff := GetEfficiency(tempID, "vegetation") 
		if eff > 0.9 {
			sysUUID = fmt.Sprintf("sys-%d-%d", uid, tempID)
			x, y, z = rand.Intn(100)-50, rand.Intn(100)-50, rand.Intn(100)-50
			found = true
			break
		}
	}
	if !found {
		sysUUID = fmt.Sprintf("sys-%d-fallback", uid)
		x, y, z = 0, 0, 0
	}

	db.Exec("INSERT INTO solar_systems (id, x, y, z, star_type, owner_uuid) VALUES (?,?,?,?, 'G2V', ?)", sysUUID, x, y, z, ServerUUID)
	db.Exec("INSERT INTO planets (system_id, efficiency_seed, type) VALUES (?, ?, 'TERRAN')", sysUUID, "SEED")
	
	bJson, _ := json.Marshal(map[string]int{"farm": 5, "well": 5, "urban_housing": 10})
	db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, buildings_json, pop_laborers, water, vegetation, iron) 
	         VALUES (?, ?, ?, ?, 1000, 5000, 5000, 500)`, sysUUID, req.Username, req.Username+"'s Prime", string(bJson))

	db.Exec(`INSERT INTO fleets (owner_uuid, status, fuel, origin_system, dest_system, ark_ship) 
			 VALUES (?, 'ORBIT', 1000, ?, ?, 1)`, req.Username, sysUUID, sysUUID)

	json.NewEncoder(w).Encode(map[string]interface{}{"status": "registered", "user_id": uid, "system_id": sysUUID})
}

// Added per query: Missing /api/state handler
func handleState(w http.ResponseWriter, r *http.Request) {
	uidStr := r.Header.Get("X-User-ID")
	var uid int
	fmt.Sscanf(uidStr, "%d", &uid)

	if uid == 0 {
		http.Error(w, "Unauthorized", 401)
		return
	}

	type StateResp struct {
		ServerUUID string
		Tick       int // Aligned with server tick
		MyColonies []Colony
		MyFleets   []Fleet
		Costs      map[string]map[string]int
		ShipCosts  map[string]map[string]int // Placeholder if needed
	}

	resp := StateResp{
		ServerUUID: ServerUUID,
		Tick:       CurrentTick,
		Costs:      BuildingCosts,
	}

	// Fetch Colonies
	rows, err := db.Query(`SELECT id, system_id, name, buildings_json, pop_laborers, pop_specialists, iron, carbon, water, gold, platinum, vegetation, stability_current FROM colonies WHERE owner_uuid = (SELECT username FROM users WHERE id=?)`, uid)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var c Colony
			var bJson string
			rows.Scan(&c.ID, &c.SystemID, &c.Name, &bJson, &c.PopLaborers, &c.PopSpecialists, &c.Iron, &c.Carbon, &c.Water, &c.Gold, &c.Platinum, &c.Vegetation, &c.StabilityCurrent)
			json.Unmarshal([]byte(bJson), &c.Buildings)
			resp.MyColonies = append(resp.MyColonies, c)
		}
	}

	// Fetch Fleets
	fRows, err := db.Query(`SELECT id, status, dest_system, ark_ship, fighters FROM fleets WHERE owner_uuid = (SELECT username FROM users WHERE id=?)`, uid)
	if err == nil {
		defer fRows.Close()
		for fRows.Next() {
			var f Fleet
			fRows.Scan(&f.ID, &f.Status, &f.DestSystem, &f.ArkShip, &f.Fighters)
			resp.MyFleets = append(resp.MyFleets, f)
		}
	}

	json.NewEncoder(w).Encode(resp)
}

func handleBankBurn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID int    `json:"colony_id"`
		Item     string `json:"item"`
		Amount   int    `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad JSON", 400)
		return
	}

	// Simple Base Prices map for MVP
	BasePrices := map[string]int{"iron": 1, "carbon": 2, "water": 1, "vegetation": 2, "gold": 50, "platinum": 100}
	basePrice, ok := BasePrices[req.Item]
	if !ok {
		http.Error(w, "Invalid Item", 400)
		return
	}

	stateLock.Lock()
	defer stateLock.Unlock()

	var currentAmount int
	var userID int
	// Safe query using whitelisted item check above
	query := fmt.Sprintf("SELECT %s, (SELECT id FROM users WHERE username=owner_uuid) FROM colonies WHERE id=?", req.Item)
	err := db.QueryRow(query, req.ColonyID).Scan(&currentAmount, &userID)
	if err != nil {
		http.Error(w, "Colony/Item error", 404)
		return
	}

	if currentAmount < req.Amount {
		http.Error(w, "Insufficient Resources", 402)
		return
	}

	eff := GetEfficiency(req.ColonyID, req.Item)
	multiplier := 1.0
	if eff > 0.1 { multiplier = 1.0 / eff } else { multiplier = 10.0 }
	
	totalCredits := int(float64(basePrice) * multiplier * float64(req.Amount))

	tx, _ := db.Begin()
	updateCol := fmt.Sprintf("UPDATE colonies SET %s = %s - ? WHERE id=?", req.Item, req.Item)
	tx.Exec(updateCol, req.Amount, req.ColonyID)
	tx.Exec("UPDATE users SET credits = credits + ? WHERE id=?", totalCredits, userID)
	tx.Commit()

	json.NewEncoder(w).Encode(map[string]interface{}{"burned": req.Amount, "credits": totalCredits})
}

func calculateFuelCost(distance int, isPeer bool) int {
	cost := distance * 10 
	if isPeer { return int(float64(cost) * 2.5) }
	return cost * 10
}

func handleFleetLaunch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FleetID    int    `json:"fleet_id"`
		DestSystem string `json:"dest_system"`
		Distance   int    `json:"distance"`
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

	cost := calculateFuelCost(req.Distance, false)
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
	if c.Buildings == nil { c.Buildings = make(map[string]int) }
	c.Buildings[req.Structure] += req.Amount
	newBJson, _ := json.Marshal(c.Buildings)

	db.Exec("UPDATE colonies SET iron=iron-?, carbon=carbon-?, buildings_json=? WHERE id=?", 
		neededIron, neededCarbon, string(newBJson), req.ColonyID)
	w.Write([]byte("Build Complete"))
}
