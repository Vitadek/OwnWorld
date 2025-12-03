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

// Phase 3.3 & 4.3: Secure Transaction Handling
func handleFederationTransaction(w http.ResponseWriter, r *http.Request) {
	// 1. Decompress
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

	// 2. Verify Signature (Security)
	// We verify that the payload was indeed signed by the peer's known public key
	if !VerifySignature(peer.PublicKey, req.Payload, req.Signature) {
		ErrorLog.Printf("SECURITY: Invalid Signature from %s", req.UUID)
		http.Error(w, "Invalid Signature", 401)
		return
	}

	// 3. Lag Switch Protection (Consensus)
	// If the transaction is too old (> 5 ticks) or from the future (> 2 ticks), reject it.
	// This prevents players from unplugging their router, moving fleets, and plugging it back in.
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

	// 4. Process Logic
	switch req.Type {
	case "FLEET_ARRIVAL":
		var f Fleet
		json.Unmarshal(req.Payload, &f)
		InfoLog.Printf("FLEET ARRIVAL: Fleet %d from %s arrived at %s", f.ID, req.UUID, f.DestSystem)
		// Insert fleet into DB with status ORBIT
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
	sysUUID := fmt.Sprintf("sys-%d-%d", uid, time.Now().UnixNano())

	rand.Seed(time.Now().UnixNano())
	x, y, z := rand.Intn(100)-50, rand.Intn(100)-50, rand.Intn(100)-50

	db.Exec("INSERT INTO solar_systems (id, x, y, z, star_type, owner_uuid) VALUES (?,?,?,?, 'G2V', ?)", sysUUID, x, y, z, ServerUUID)
	db.Exec("INSERT INTO planets (system_id, efficiency_seed, type) VALUES (?, ?, 'TERRAN')", sysUUID, "SEED")
	bJson, _ := json.Marshal(map[string]int{"farm": 5, "well": 5, "urban_housing": 10})
	db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, buildings_json, pop_laborers, water, vegetation, iron) 
	         VALUES (?, ?, ?, ?, 1000, 5000, 5000, 500)`, sysUUID, req.Username, req.Username+"'s Prime", string(bJson))
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "registered", "user_id": uid, "system_id": sysUUID})
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
