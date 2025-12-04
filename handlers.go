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
	"os"
	"strconv"
	"sync/atomic"
	"time"

	pb "ownworld/pkg/federation"
	"google.golang.org/protobuf/proto"
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
		}

		peerLock.Lock()
		Peers[req.UUID] = newPeer
		peerLock.Unlock()

		recalculateLeader()
	}
}

func handleHandshake(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	decompressed := decompressLZ4(body)
	var req HandshakeRequest
	if err := json.Unmarshal(decompressed, &req); err != nil {
		http.Error(w, "Bad Request", 400)
		return
	}
	
	resp := HandshakeResponse{
		Status:   "Queued",
		UUID:     ServerUUID,
		Location: ServerLoc, // defined in globals.go
	}

	select {
	case immigrationQueue <- req:
		w.WriteHeader(http.StatusAccepted)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	default:
		http.Error(w, "Queue Full", 503)
	}
}

func handleFederationTransaction(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/x-protobuf" {
		http.Error(w, "Protobuf Required", 415)
		return
	}

	senderUUID := r.Header.Get("X-Server-UUID")
	if senderUUID == "" {
		http.Error(w, "Missing UUID Header", 401)
		return
	}

	peerLock.RLock()
	peer, known := Peers[senderUUID]
	peerLock.RUnlock()

	if !known {
		http.Error(w, "Unknown Peer", 403)
		return
	}

	body, _ := io.ReadAll(r.Body)
	decompressed := decompressLZ4(body)

	var packet pb.Packet
	if err := proto.Unmarshal(decompressed, &packet); err != nil {
		ErrorLog.Printf("Proto Error: %v", err)
		http.Error(w, "Invalid Proto", 400)
		return
	}

	var isValidSig bool

	switch msg := packet.Content.(type) {
	case *pb.Packet_Heartbeat:
		signedMsg := fmt.Sprintf("%s:%d", packet.SenderUuid, msg.Heartbeat.Tick)
		isValidSig = VerifySignature(peer.PublicKey, []byte(signedMsg), packet.Signature)

		if isValidSig {
			peerLock.Lock()
			if p, ok := Peers[packet.SenderUuid]; ok {
				p.LastTick = msg.Heartbeat.Tick
				p.PeerCount = int(msg.Heartbeat.PeerCount)
				p.LastSeen = time.Now()
			}
			peerLock.Unlock()
			
			if packet.SenderUuid == LeaderUUID {
				syncClock(msg.Heartbeat.Tick)
			}
		}

	case *pb.Packet_FleetMove:
		isValidSig = VerifySignature(peer.PublicKey, msg.FleetMove.CompressedFleetData, packet.Signature)

		if isValidSig {
			InfoLog.Printf("Fleet Arrived from %s", packet.SenderUuid)
			var f Fleet
			if err := json.Unmarshal(msg.FleetMove.CompressedFleetData, &f); err == nil {
				db.Exec(`INSERT INTO fleets (owner_uuid, status, origin_system, dest_system, 
				         ark_ship, fighters, frigates, haulers, fuel) 
				         VALUES (?, 'ORBIT', ?, ?, ?, ?, ?, ?, ?)`,
					f.OwnerUUID, msg.FleetMove.OriginSystem, msg.FleetMove.DestSystem,
					f.ArkShip, f.Fighters, f.Frigates, f.Haulers, f.Fuel)
			}
		}

	case *pb.Packet_MarketOrder:
		orderMsg := fmt.Sprintf("ORDER:%s:%s:%d:%d",
			msg.MarketOrder.OrderId, msg.MarketOrder.Item,
			msg.MarketOrder.Price, msg.MarketOrder.Quantity)

		isValidSig = VerifySignature(peer.PublicKey, []byte(orderMsg), packet.Signature)

		if isValidSig {
			db.Exec(`INSERT OR REPLACE INTO market_orders (id, seller_uuid, item, price, qty) 
			         VALUES (?, ?, ?, ?, ?)`,
				msg.MarketOrder.OrderId, msg.MarketOrder.SellerUuid,
				msg.MarketOrder.Item, msg.MarketOrder.Price, msg.MarketOrder.Quantity)
		}
	}

	if !isValidSig {
		ErrorLog.Printf("🚨 Invalid Signature from %s", packet.SenderUuid)
		http.Error(w, "Invalid Signature", 401)
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
	w.Header().Set("Cache-Control", "public, max-age=60")
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad JSON", 400)
		return
	}

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	userUUID := hashBLAKE3(pub)
	pubHex := hex.EncodeToString(pub)
	privEnc := encryptKey(priv, req.Password)
	passHash := hashBLAKE3([]byte(req.Password))

	_, err := db.Exec(`INSERT INTO users (global_uuid, username, password_hash, is_local, ed25519_pubkey, ed25519_priv_enc) 
	                   VALUES (?, ?, ?, 1, ?, ?)`, userUUID, req.Username, passHash, pubHex, privEnc)

	if err != nil {
		http.Error(w, "Username Taken", 400)
		return
	}

	mrand.Seed(time.Now().UnixNano())
	var sysID string
	found := false
	
	var sysX, sysY, sysZ int

	// Goldilocks Search with Cluster Spawning
	for i := 0; i < 50; i++ {
		uuidBytes, _ := hex.DecodeString(userUUID)
		offsetX := mrand.Intn(40) - 20
		offsetY := mrand.Intn(40) - 20
		offsetZ := mrand.Intn(40) - 20

		// ServerLoc is defined in globals.go
		sysX = ServerLoc[0] + offsetX
		sysY = ServerLoc[1] + offsetY
		sysZ = ServerLoc[2] + offsetZ
		
		tempID := (sysX * 10000) + (sysY * 100) + sysZ
		if tempID < 0 { tempID = -tempID }
		
		if GetEfficiency(tempID, "food") > 0.9 {
			sysID = fmt.Sprintf("sys-%d-%d-%d", sysX, sysY, sysZ)
			found = true
			break
		}
	}
	if !found {
		sysID = fmt.Sprintf("sys-%d-%d-%d", ServerLoc[0], ServerLoc[1], ServerLoc[2])
		sysX, sysY, sysZ = ServerLoc[0], ServerLoc[1], ServerLoc[2]
	}

	db.Exec("INSERT OR IGNORE INTO solar_systems (id, x, y, z, star_type, owner_uuid) VALUES (?, ?, ?, ?, 'G2V', ?)", 
		sysID, sysX, sysY, sysZ, ServerUUID)

	startBuilds := `{"farm": 5, "iron_mine": 5, "urban_housing": 10}`
	db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, pop_laborers, food, iron, buildings_json) 
	         VALUES (?, ?, ?, 100, 2000, 1000, ?)`, sysID, userUUID, req.Username+" Prime", startBuilds)

	// Ark Ship Spawn (Corrected to include Haulers if needed, but Ark is special)
	db.Exec(`INSERT INTO fleets (owner_uuid, status, origin_system, ark_ship, fuel) 
			 VALUES (?, 'ORBIT', ?, 1, 2000)`, userUUID, sysID)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "registered", 
		"user_uuid": userUUID,
		"system_id": sysID,
		"location":  []int{sysX, sysY, sysZ},
		"message":   "Identity Secured. Colony Founded. Ark Ship Ready.",
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

	if err != nil {
		http.Error(w, "Fleet Not Found", 404)
		return
	}
	if f.Status != "ORBIT" {
		http.Error(w, "Fleet in transit", 400)
		return
	}

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

	w.Write([]byte(fmt.Sprintf("Fleet Launched. Cost: %d. Arrival Tick: %d", cost, arrivalTick)))
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
	basePrice := 1.0
	payout := int(float64(req.Amount) * basePrice * multiplier)

	tx, _ := db.Begin()
	res, _ := tx.Exec(fmt.Sprintf("UPDATE colonies SET %s = %s - ? WHERE id=? AND %s >= ?", req.Item, req.Item, req.Item), req.Amount, req.ColonyID, req.Amount)
	if n, _ := res.RowsAffected(); n == 0 {
		tx.Rollback()
		http.Error(w, "Insufficient Funds", 400)
		return
	}
	tx.Exec("UPDATE users SET credits = credits + ? WHERE id=?", payout, req.UserID)
	tx.Commit()

	w.Write([]byte(fmt.Sprintf("Burned %d for %d credits (Rate: %.2f)", req.Amount, payout, multiplier)))
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
	if !ok { http.Error(w, "Unknown Unit", 400); return }

	stateLock.Lock()
	defer stateLock.Unlock()

	var c Colony
	var bJson string
	err := db.QueryRow(`SELECT buildings_json, system_id, owner_uuid, iron, food, fuel, carbon, gold, pop_laborers 
	                    FROM colonies WHERE id=?`, req.ColonyID).
	                    Scan(&bJson, &c.SystemID, &c.OwnerUUID, &c.Iron, &c.Food, &c.Fuel, &c.Carbon, &c.Gold, &c.PopLaborers)

	if err != nil { http.Error(w, "Colony Not Found", 404); return }
	
	json.Unmarshal([]byte(bJson), &c.Buildings)
	if c.Buildings["shipyard"] < 1 {
		http.Error(w, "Shipyard Required", 400); return
	}

	if c.Iron < costs["iron"]*req.Amount || 
	   c.Food < costs["food"]*req.Amount || 
	   c.Fuel < costs["fuel"]*req.Amount || 
	   c.Carbon < costs["carbon"]*req.Amount ||
	   c.Gold < costs["gold"]*req.Amount ||
	   c.PopLaborers < costs["pop_laborers"]*req.Amount {
		http.Error(w, "Insufficient Resources", 402); return
	}

	db.Exec(`UPDATE colonies SET iron=iron-?, food=food-?, fuel=fuel-?, carbon=carbon-?, gold=gold-?, pop_laborers=pop_laborers-? WHERE id=?`,
		costs["iron"]*req.Amount, costs["food"]*req.Amount, costs["fuel"]*req.Amount, 
		costs["carbon"]*req.Amount, costs["gold"]*req.Amount, costs["pop_laborers"]*req.Amount, 
		req.ColonyID)

	arkCount := 0
	if req.Unit == "ark_ship" { arkCount = req.Amount }
	
	// Only insert new fleet if not adding to existing (Simplified: Always new fleet for now)
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
	if !ok { http.Error(w, "Unknown Structure", 400); return }

	stateLock.Lock()
	defer stateLock.Unlock()

	var c Colony
	var bJson string
	err := db.QueryRow("SELECT buildings_json, iron, carbon, gold FROM colonies WHERE id=?", req.ColonyID).Scan(&bJson, &c.Iron, &c.Carbon, &c.Gold)
	if err != nil { http.Error(w, "Colony Not Found", 404); return }

	if c.Iron < cost["iron"]*req.Amount || c.Carbon < cost["carbon"]*req.Amount || c.Gold < cost["gold"]*req.Amount {
		http.Error(w, "Insufficient Resources", 402); return
	}

	json.Unmarshal([]byte(bJson), &c.Buildings)
	if c.Buildings == nil { c.Buildings = make(map[string]int) }
	c.Buildings[req.Structure] += req.Amount
	newBJson, _ := json.Marshal(c.Buildings)

	db.Exec("UPDATE colonies SET iron=iron-?, carbon=carbon-?, gold=gold-?, buildings_json=? WHERE id=?",
		cost["iron"]*req.Amount, cost["carbon"]*req.Amount, cost["gold"]*req.Amount, string(newBJson), req.ColonyID)

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
