package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	mrand "math/rand"
	"net/http"
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
	// Stub for now to fix import error
	w.Write([]byte("Sync OK"))
}

// --- Client Handlers ---

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	json.NewDecoder(r.Body).Decode(&req)
	
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	userUUID := hashBLAKE3(pub) 
	pubHex := hex.EncodeToString(pub)
	privEnc := encryptKey(priv, req.Password)
	passHash := hashBLAKE3([]byte(req.Password))

	_, err := db.Exec(`INSERT INTO users (global_uuid, username, password_hash, is_local, ed25519_pubkey, ed25519_priv_enc) 
	                   VALUES (?, ?, ?, 1, ?, ?)`, userUUID, req.Username, passHash, pubHex, privEnc)
	
	if err != nil { http.Error(w, "Taken", 400); return }
	
	var uid int
	db.QueryRow("SELECT id FROM users WHERE global_uuid=?", userUUID).Scan(&uid)

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

	db.Exec("INSERT OR IGNORE INTO solar_systems (id, owner_uuid) VALUES (?, ?)", sysID, ServerUUID)

	startBuilds := `{"farm": 5, "iron_mine": 5, "urban_housing": 10}`
	db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, pop_laborers, food, iron, buildings_json) 
	         VALUES (?, ?, ?, 100, 2000, 1000, ?)`, sysID, userUUID, req.Username+" Prime", startBuilds)

	// Spawn Ark Ship
	db.Exec(`INSERT INTO fleets (owner_uuid, status, origin_system, ark_ship, fuel) 
			 VALUES (?, 'ORBIT', ?, 1, 2000)`, userUUID, sysID)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "registered", 
		"user_uuid": userUUID,
		"system_id": sysID,
		"message": "Identity Secured. Colony Founded. Ark Ship Ready.",
	})
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

	if err != nil { http.Error(w, "Fleet Not Found", 404); return }
	if f.Status != "ORBIT" { http.Error(w, "Fleet in transit", 400); return }

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

func handleConstruct(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Stub"))
}
func handleBuild(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Stub"))
}
func handleDeploy(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Stub"))
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
	if eff < 0.1 { eff = 0.1 } 
	
	multiplier := 1.0 / eff
	payout := int(float64(req.Amount) * 1.0 * multiplier)
	
	w.Write([]byte(fmt.Sprintf("Burned %d for %d credits", req.Amount, payout)))
}
