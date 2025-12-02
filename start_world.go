package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	mrand "math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lukechampine/blake3"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pierrec/lz4/v4"
	"golang.org/x/time/rate"
)

// --- Constants & Config ---

const (
	DBPath          = "./data/ownworld.db"
	MinTickDuration = 4000
	MaxTickDuration = 6000
	MaxLogSize      = 1024 * 1024 // 1MB payload limit
)

var (
	// Federation Config
	FederationName = os.Getenv("FEDERATION_NAME") // If empty, runs solo
	SeedNodes      = strings.Split(os.Getenv("SEED_NODES"), ",")
)

// --- Structs (Updated Schema) ---

type Colony struct {
	ID            int            `json:"id"`
	SystemID      string         `json:"system_id"` // Solar System grouping
	OwnerID       int            `json:"owner_id"`
	Name          string         `json:"name"`
	Location      []int          `json:"location"`
	Buildings     map[string]int `json:"buildings"`
	Population    int            `json:"population"`
	
	// Resources (Tier 1 & 2)
	Food, Water             int `json:"food,water"`
	Iron, Carbon, Gold      int `json:"iron,carbon,gold"`
	Platinum, Uranium, Dia  int `json:"platinum,uranium,diamond"`
	Vegetation, Oxygen      int `json:"vegetation,oxygen"`
	
	// Stats
	Health, Intel, Crime, Happy float64 `json:"health,intelligence,crime,happiness"`
	DefenseRating int `json:"defense_rating"`
}

type SolarSystem struct {
	ID          string `json:"id"`
	StarType    string `json:"star_type"` // Affects planetary efficiency
	OwnerUUID   string `json:"owner_uuid"`
	IsFederated bool   `json:"is_federated"`
}

type Fleet struct {
	ID           int    `json:"id"`
	OriginColony int    `json:"origin_colony"`
	Status       string `json:"status"`
	// Composition
	ArkShip      int `json:"ark_ship"` // For starting new colonies
	Fighters     int `json:"fighters"`
	Frigates     int `json:"frigates"` // Requires assembly
	Haulers      int `json:"haulers"`
	Fuel         int `json:"fuel"`     // Item_StarFuel
}

type Peer struct {
	UUID        string
	Url         string
	PublicKey   ed25519.PublicKey
	GenesisHash string
	PeerCount   int
	CurrentTick int64
	LastSeen    time.Time
	Reputation  int // Slashing logic
}

// --- Globals ---

var (
	// Identity
	ServerUUID     string
	GenesisHash    string
	PrivateKey     ed25519.PrivateKey
	PublicKey      ed25519.PublicKey

	// Database
	db *sql.DB
	
	// Memory Pools (Performance)
	bufferPool = sync.Pool{New: func() interface{} { return new(bytes.Buffer) }}

	// Consensus State
	Peers         = make(map[string]*Peer)
	peersLock     sync.RWMutex
	CurrentTick   int64 = 0
	TickDuration  int64 = 5000 // Dynamic Slewing
	MyRank        int   = 0
	TotalPeers    int   = 1
	PhaseOffset   int64 = 0 // TDMA Stagger
	
	// Security
	rateLimiter = rate.NewLimiter(1, 5) // 1 request per 5s per IP (Simplified global for demo)
)

// --- 1. Core Database Logic (WAL + Event Sourcing) ---

func initDB() {
	os.MkdirAll("./data", 0755)
	
	// WAL Mode + Busy Timeout (Critical for concurrency)
	var err error
	db, err = sql.Open("sqlite3", DBPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil { panic(err) }

	// Schema Updates
	schema := `
	CREATE TABLE IF NOT EXISTS system_meta (key TEXT PRIMARY KEY, value TEXT);
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE, password_hash TEXT, credits INTEGER DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS colonies (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, system_id TEXT, name TEXT,
		loc_x INTEGER, loc_y INTEGER, loc_z INTEGER, buildings_json TEXT,
		population INTEGER DEFAULT 100,
		food INTEGER DEFAULT 1000, water INTEGER DEFAULT 1000,
		iron INTEGER DEFAULT 0, carbon INTEGER DEFAULT 0, gold INTEGER DEFAULT 0,
		platinum INTEGER DEFAULT 0, uranium INTEGER DEFAULT 0, diamond INTEGER DEFAULT 0,
		vegetation INTEGER DEFAULT 0, oxygen INTEGER DEFAULT 1000,
		health REAL DEFAULT 100.0, intelligence REAL DEFAULT 50.0, crime REAL DEFAULT 0.0, happiness REAL DEFAULT 100.0
	);
	CREATE TABLE IF NOT EXISTS fleets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, origin_colony INTEGER,
		status TEXT, ark_ship INTEGER DEFAULT 0, fighters INTEGER DEFAULT 0, 
		frigates INTEGER DEFAULT 0, haulers INTEGER DEFAULT 0, fuel INTEGER DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS transaction_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT, tick INTEGER, action_type TEXT, payload_blob BLOB
	);
	CREATE TABLE IF NOT EXISTS daily_snapshots (
		day_id INTEGER PRIMARY KEY, state_blob BLOB, final_hash TEXT
	);
	CREATE TABLE IF NOT EXISTS market_orders (
		id TEXT PRIMARY KEY, seller_uuid TEXT, item TEXT, price INTEGER, qty INTEGER
	);
	`
	if _, err := db.Exec(schema); err != nil { panic(err) }

	initIdentity()
}

// Genesis Bind & Key Generation
func initIdentity() {
	var privKeyHex, uuid, genHash string
	
	// Try load from DB
	err := db.QueryRow("SELECT value FROM system_meta WHERE key='server_uuid'").Scan(&uuid)
	
	if err == sql.ErrNoRows {
		// --- FIRST BOOT: GENESIS ---
		fmt.Println("🚀 Initializing New World Genesis...")
		
		// 1. Generate Ed25519 Keys
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		PrivateKey = priv
		PublicKey = pub
		privKeyHex = hex.EncodeToString(priv)
		
		// 2. Create Genesis State (Deterministic)
		genesisState := map[string]interface{}{"timestamp": time.Now().Unix(), "seed": mrand.Int63()}
		genBytes, _ := json.Marshal(genesisState)
		
		// 3. Bind Identity: UUID = BLAKE3(Genesis)
		hash := blake3.Sum256(genBytes)
		uuid = hex.EncodeToString(hash[:])
		GenesisHash = uuid // Simplified: Genesis hash IS the UUID base
		
		// 4. Persist
		tx, _ := db.Begin()
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('server_uuid', ?)", uuid)
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('genesis_hash', ?)", uuid)
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('priv_key', ?)", privKeyHex)
		tx.Commit()
		
		// 5. Hard State Reset Logic check
		// In a real app, check FEDERATION_NAME env vs stored value. If mismatch, wipe DB.
		
	} else {
		// --- RESUME ---
		ServerUUID = uuid
		db.QueryRow("SELECT value FROM system_meta WHERE key='genesis_hash'").Scan(&GenesisHash)
		db.QueryRow("SELECT value FROM system_meta WHERE key='priv_key'").Scan(&privKeyHex)
		bytes, _ := hex.DecodeString(privKeyHex)
		PrivateKey = ed25519.PrivateKey(bytes)
		PublicKey = PrivateKey.Public().(ed25519.PublicKey)
	}
	
	fmt.Printf("✅ Server Identity Loaded: %s\n", ServerUUID[:8])
}

// --- 2. Crypto & Compression Helpers ---

func CompressLZ4(src []byte) []byte {
	buf := bufferPool.Get().(*bytes.Buffer)
	defer bufferPool.Put(buf)
	buf.Reset()
	
	// Create LZ4 writer
	w := lz4.NewWriter(buf)
	w.Write(src)
	w.Close()
	
	// Return copy (pool safety)
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out
}

func DecompressLZ4(src []byte) []byte {
	r := lz4.NewReader(bytes.NewReader(src))
	out, _ := io.ReadAll(r)
	return out
}

func SignMessage(msg []byte) []byte {
	return ed25519.Sign(PrivateKey, msg)
}

func VerifySignature(pub ed25519.PublicKey, msg, sig []byte) bool {
	return ed25519.Verify(pub, msg, sig)
}

// Efficiency Seed (BLAKE3 Deterministic Randomness)
func GetEfficiency(planetID int, resource string) float64 {
	hash := blake3.Sum256([]byte(fmt.Sprintf("%d-%s-%s", planetID, resource, ServerUUID)))
	val := int(hash[0]) // 0-255
	// Range: 0.5 (Poor) to 2.5 (Rich)
	return 0.5 + (float64(val) / 128.0)
}

// --- 3. Consensus & Time (TDMA) ---

func RunElection() {
	peersLock.Lock()
	defer peersLock.Unlock()

	type Candidate struct {
		UUID  string
		Score int64
	}
	var candidates []Candidate
	
	// Add Self
	// Score = (Tick << 16) | PeerCount
	myScore := (CurrentTick << 16) | int64(len(Peers))
	candidates = append(candidates, Candidate{ServerUUID, myScore})
	
	// Add Peers (Filter Banned)
	for uuid, p := range Peers {
		if p.Reputation > -1 { // -1 = Banned
			score := (p.CurrentTick << 16) | int64(p.PeerCount)
			candidates = append(candidates, Candidate{uuid, score})
		}
	}
	
	// Sort Descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
	
	// Find My Rank
	TotalPeers = len(candidates)
	for i, c := range candidates {
		if c.UUID == ServerUUID {
			MyRank = i
			break
		}
	}
	
	// Calculate Offset (TDMA)
	if TotalPeers > 0 {
		PhaseOffset = (TickDuration / int64(TotalPeers)) * int64(MyRank)
	}
	
	// Clock Slewing Logic (Simplified)
	// If I'm not leader (Rank 0), adjust TickDuration slightly based on Leader's heartbeat
}

func tickLoop() {
	// Start Election Loop
	go func() {
		for range time.Tick(10 * time.Second) { RunElection() }
	}()

	for {
		// 1. Calculate Target Time (Aligned to Global Grid + Stagger Offset)
		now := time.Now().UnixMilli()
		currentTickStart := (now / TickDuration) * TickDuration
		target := currentTickStart + TickDuration + PhaseOffset
		
		// 2. Sleep until slot
		sleep := time.Until(time.UnixMilli(target))
		if sleep > 0 { time.Sleep(sleep) }
		
		// 3. Execute
		tickWorld()
	}
}

// --- 4. Game Logic (Economy & Simulation) ---

func tickWorld() {
	atomic.AddInt64(&CurrentTick, 1)
	
	// Snapshot Logic (Hybrid Event Sourcing)
	// Only full snapshot every 100 ticks, otherwise just delta logs
	if CurrentTick % 100 == 0 {
		// ... Snapshot logic using BLAKE3 ...
	}
	
	// Insert "TICK" into transaction log
	db.Exec("INSERT INTO transaction_log (tick, action_type) VALUES (?, 'TICK')", CurrentTick)
	
	// --- Simulation ---
	rows, _ := db.Query(`SELECT id, population, buildings_json, iron, carbon, gold, food FROM colonies`)
	defer rows.Close()

	var updates []Colony
	
	for rows.Next() {
		var c Colony; var bJson string
		rows.Scan(&c.ID, &c.Population, &bJson, &c.Iron, &c.Carbon, &c.Gold, &c.Food)
		var b map[string]int; json.Unmarshal([]byte(bJson), &b)

		// 1. Evening Factor (Corruption)
		// Multiplier drops as empire grows. (Simplified: assume 1 colony for math here)
		efficiency := 1.0 // In real code: 1.0 / Log2(UserTotalColonies)

		// 2. Mining (With Efficiency Seeds)
		c.Iron += int(float64(b["iron_mine"] * 10) * GetEfficiency(c.ID, "iron") * efficiency)
		c.Carbon += int(float64(b["carbon_siphon"] * 5) * GetEfficiency(c.ID, "carbon") * efficiency)
		c.Gold += int(float64(b["gold_mine"] * 1) * GetEfficiency(c.ID, "gold") * efficiency)
		
		// 3. Refining (Steel)
		steelCap := b["refinery"] * 10
		if c.Iron >= steelCap && c.Carbon >= steelCap {
			c.Iron -= steelCap
			c.Carbon -= steelCap
			// In real schema, add Steel column. For now, assume it goes to inventory.
		}

		// 4. Consumption
		c.Food -= c.Population / 10
		
		updates = append(updates, c)
	}

	// Batch Update (Optimization)
	tx, _ := db.Begin()
	stmt, _ := tx.Prepare(`UPDATE colonies SET iron=?, carbon=?, gold=?, food=? WHERE id=?`)
	for _, u := range updates {
		stmt.Exec(u.Iron, u.Carbon, u.Gold, u.Food, u.ID)
	}
	stmt.Commit()
}

// --- 5. Handlers & Networking (Layer Cake) ---

// Helper for "Dual Mode" handling
func isProtobuf(r *http.Request) bool {
	ct := r.Header["Content-Type"]
	return len(ct) > 0 && ct[0] == "application/x-protobuf"
}

func handleFederationMessage(w http.ResponseWriter, r *http.Request) {
	// LAYER 1: IP Rate Limit
	// (Simplified)
	if !rateLimiter.Allow() {
		http.Error(w, "Rate Limit Exceeded", 429)
		return
	}

	// LAYER 2: Allowlist / Handshake Check
	uuid := r.Header.Get("X-Server-UUID")
	peersLock.RLock()
	_, known := Peers[uuid]
	peersLock.RUnlock()
	
	if !known && r.URL.Path != "/federation/handshake" {
		http.Error(w, "Unknown Peer", 403)
		return
	}

	// LAYER 3: Handle Payload
	if isProtobuf(r) {
		// FAST PATH (Robot)
		body, _ := io.ReadAll(r.Body)
		decompressed := DecompressLZ4(body)
		
		// Validate Signature (Ed25519)
		sig, _ := hex.DecodeString(r.Header.Get("X-Signature"))
		pubKey := Peers[uuid].PublicKey // Get cached key
		
		// Probabilistic Verify (10% for Heartbeats, 100% for Critical)
		msgType := r.Header.Get("X-Msg-Type")
		mustVerify := msgType == "FLEET" || mrand.Float32() < 0.1
		
		if mustVerify && !VerifySignature(pubKey, decompressed, sig) {
			// SLASHING LOGIC HERE
			fmt.Println("🚨 INVALID SIGNATURE from", uuid)
			return
		}
		
		// Process (Mock)
		w.Write(CompressLZ4([]byte("ACK")))
		
	} else {
		// SLOW PATH (JSON / Debug)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// The Bank (Burn Mechanic)
func handleBankBurn(w http.ResponseWriter, r *http.Request) {
	var req struct { UserID int; ColonyID int; Resource string; Amount int }
	json.NewDecoder(r.Body).Decode(&req)
	
	// 1. Calculate Scarcity Price
	basePrice := 1.0 // Credits per unit
	efficiency := GetEfficiency(req.ColonyID, req.Resource)
	
	// High Efficiency (Rich Planet) = Low Payout
	// Low Efficiency (Poor Planet) = High Payout
	payoutRate := basePrice * (1.0 / efficiency)
	totalCredits := int(float64(req.Amount) * payoutRate)
	
	// 2. Transaction
	tx, _ := db.Begin()
	// Deduct Resource (SQL Injection safe in real app)
	query := fmt.Sprintf("UPDATE colonies SET %s = %s - ? WHERE id = ? AND %s >= ?", req.Resource, req.Resource, req.Resource)
	res, _ := tx.Exec(query, req.Amount, req.ColonyID, req.Amount)
	
	rows, _ := res.RowsAffected()
	if rows == 0 {
		tx.Rollback()
		http.Error(w, "Insufficient Funds", 400)
		return
	}
	
	// Add Credits
	tx.Exec("UPDATE users SET credits = credits + ? WHERE id = ?", totalCredits, req.UserID)
	tx.Commit()
	
	json.NewEncoder(w).Encode(map[string]interface{}{
		"burned": req.Amount,
		"credits_earned": totalCredits,
		"rate": payoutRate,
	})
}

// The "Ark" Start
func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{Username, Password string}; json.NewDecoder(r.Body).Decode(&req)
	
	// 1. Create User
	// Note: Use BLAKE3 for passwords in real app too
	hash := blake3.Sum256([]byte(req.Password))
	res, err := db.Exec("INSERT INTO users (username, password_hash) VALUES (?,?)", req.Username, hex.EncodeToString(hash[:]))
	if err != nil { http.Error(w, "Taken", 400); return }
	uid, _ := res.LastInsertId()
	
	// 2. NO Free Colony. Give an "Ark Ship" instead.
	// Create a virtual "Orbit" fleet (ColonyID 0 usually implies Deep Space or Temp)
	db.Exec(`INSERT INTO fleets (user_id, origin_colony, status, ark_ship, fuel) VALUES (?, 0, 'ORBIT', 1, 1000)`, uid)
	
	json.NewEncoder(w).Encode(map[string]int{"user_id": int(uid), "ark_ship": 1})
}

// Fuel Logic
func calculateFuelCost(origin, target []int, mass int, targetUUID string) int {
	dist := 0.0 // Distance Math
	for i := 0; i < 3; i++ { dist += math.Pow(float64(origin[i]-target[i]), 2) }
	dist = math.Sqrt(dist)
	
	multiplier := 10.0 // Universe Penalty
	if targetUUID == ServerUUID {
		multiplier = 1.0 // Local
	} else {
		peersLock.RLock()
		if _, ok := Peers[targetUUID]; ok {
			multiplier = 2.5 // Federated
		}
		peersLock.RUnlock()
	}
	
	return int(dist * float64(mass) * multiplier)
}

func main() {
	// 1. Setup
	initDB()
	
	// 2. Bootstrap (Seeds)
	if len(SeedNodes) > 0 && SeedNodes[0] != "" {
		// go bootstrapFederation() 
	}
	
	// 3. Start TDMA Tick Loop
	go tickLoop()

	// 4. HTTP Server with Timeouts (Slow Loris Fix)
	mux := http.NewServeMux()
	mux.HandleFunc("/register", handleRegister)
	mux.HandleFunc("/bank/burn", handleBankBurn)
	mux.HandleFunc("/federation/msg", handleFederationMessage) // Dual Mode
	
	server := &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  15 * time.Second,
	}

	fmt.Printf("🌌 Galaxy Node %s (Genesis: %s) Listening on :8080\n", ServerUUID[:8], GenesisHash[:8])
	server.ListenAndServe()
}
