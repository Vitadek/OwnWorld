package main

import (
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

// --- Client Handlers ---

// V3.1: Homestead Start (Colony + Scout ONLY)
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
	
	rand.Seed(time.Now().UnixNano())
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

	// 1. Create System
	db.Exec("INSERT INTO solar_systems (id, x, y, z, star_type, owner_uuid) VALUES (?,?,?,?, 'G2V', ?)", 
		sysUUID, rand.Intn(100), rand.Intn(100), rand.Intn(100), ServerUUID)

	// 2. Create Colony (Homestead)
	bJson, _ := json.Marshal(map[string]int{"farm": 5, "well": 5, "urban_housing": 10})
	// Note: 'pop_laborers' etc. map to new schema
	db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, buildings_json, pop_laborers, water, food, iron) 
	         VALUES (?, ?, ?, ?, 1000, 5000, 5000, 500)`, sysUUID, req.Username, req.Username+"'s Prime", string(bJson))

	// 3. Create Scout Fleet (Vision Only, No Ark)
	db.Exec(`INSERT INTO fleets (owner_uuid, status, fuel, origin_system, dest_system, ark_ship, fighters) 
			 VALUES (?, 'ORBIT', 1000, ?, ?, 0, 1)`, req.Username, sysUUID, sysUUID)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "registered", 
		"user_id": uid, 
		"message": "Homestead Established. Scout Deployed. Build a Shipyard to Expand.",
		"system_id": sysUUID,
	})
}

// V3.1: Construct Unit (The Expansion Gate)
func handleConstruct(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID int    `json:"colony_id"`
		Unit     string `json:"unit"` // "ark_ship"
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
	// Fetch resources matching new schema
	err := db.QueryRow("SELECT buildings_json, system_id, owner_uuid, iron, food, fuel, pop_laborers FROM colonies WHERE id=?", req.ColonyID).Scan(&bJson, &c.SystemID, &c.OwnerUUID, &c.Iron, &c.Food, &c.Fuel, &c.PopLaborers)
	
	if err == nil {
		c.Buildings = make(map[string]int)
		json.Unmarshal([]byte(bJson), &c.Buildings)
	} else {
		http.Error(w, "Colony Not Found", 404)
		return
	}
	
	// Requirement: Shipyard
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

	// Deduct
	db.Exec("UPDATE colonies SET iron=iron-?, food=food-?, fuel=fuel-?, pop_laborers=pop_laborers-? WHERE id=?",
		neededIron, neededFood, neededFuel, neededCrew, req.ColonyID)

	// Create Fleet
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
	w.Write([]byte("{}"))
}
func handleSyncLedger(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Sync Stub"))
}
