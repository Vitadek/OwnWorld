package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	mrand "math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lukechampine.com/blake3"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pierrec/lz4/v4"
	"golang.org/x/time/rate"
)

// --- Phase 1.2: Memory Management ---
var bufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// --- Phase 2.1: Crypto & Compression Helpers ---

func compressLZ4(src []byte) []byte {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)
	zw := lz4.NewWriter(buf)
	zw.Write(src)
	zw.Close()
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out
}

func decompressLZ4(src []byte) []byte {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)
	zr := lz4.NewReader(bytes.NewReader(src))
	io.Copy(buf, zr)
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out
}

func hashBLAKE3(data []byte) string {
	sum := blake3.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// --- Phase 1.3: Schema (Mutable State) ---

type User struct {
	ID                         int    `json:"id"`
	GlobalUUID                 string `json:"global_uuid"`
	Username                   string `json:"username"`
	PasswordHash               string `json:"password_hash"`
	Credits                    int    `json:"credits"`
	IsLocal                    bool   `json:"is_local"`
	Ed25519EncryptedPrivateKey []byte `json:"ed25519_encrypted_private_key"`
}

type SolarSystem struct {
	ID        string  `json:"id"`
	X         int     `json:"x"`
	Y         int     `json:"y"`
	Z         int     `json:"z"`
	StarType  string  `json:"star_type"`
	OwnerUUID string  `json:"owner_uuid"`
	TaxRate   float64 `json:"tax_rate"`
}

type Planet struct {
	ID             int    `json:"id"`
	SystemID       string `json:"system_id"`
	EfficiencySeed string `json:"efficiency_seed"`
	Type           string `json:"type"`
}

type Colony struct {
	ID            int            `json:"id"`
	SystemID      string         `json:"system_id"`
	OwnerUUID     string         `json:"owner_uuid"`
	Name          string         `json:"name"`
	BuildingsJSON string         `json:"buildings_json"`
	Buildings     map[string]int `json:"buildings,omitempty"`

	// Resources
	Iron       int `json:"iron"`
	Carbon     int `json:"carbon"`
	Water      int `json:"water"`
	Gold       int `json:"gold"`
	Platinum   int `json:"platinum"`
	Uranium    int `json:"uranium"`
	Diamond    int `json:"diamond"`
	Vegetation int `json:"vegetation"`

	// Stats
	PopLaborers      int     `json:"pop_laborers"`
	PopSpecialists   int     `json:"pop_specialists"`
	PopElites        int     `json:"pop_elites"`
	StabilityCurrent float64 `json:"stability_current"`
	StabilityTarget  float64 `json:"stability_target"`
	MartialLaw       bool    `json:"martial_law"`
}

type Fleet struct {
	ID            int    `json:"id"`
	OwnerUUID     string `json:"owner_uuid"`
	Status        string `json:"status"` // ORBIT, TRANSIT
	Fuel          int    `json:"fuel"`
	OriginSystem  string `json:"origin_system"`
	DestSystem    string `json:"dest_system"`
	DepartureTick int    `json:"departure_tick"`
	ArrivalTick   int    `json:"arrival_tick"`
	StartCoords   string `json:"start_coords"`
	DestCoords    string `json:"dest_coords"`

	// Composition
	ArkShip  int `json:"ark_ship"`
	Fighters int `json:"fighters"`
	Frigates int `json:"frigates"`
	Haulers  int `json:"haulers"`
}

// --- Phase 3.1: Network Protocol ---

type Heartbeat struct {
	UUID      string `json:"uuid"`
	Tick      int    `json:"tick"`
	Timestamp int64  `json:"timestamp"`
	Signature []byte `json:"signature"`
}

type HandshakeRequest struct {
	UUID        string `json:"uuid"`
	GenesisHash string `json:"genesis_hash"`
	PublicKey   string `json:"public_key"`
	Address     string `json:"address"`
}

type Peer struct {
	UUID      string            `json:"uuid"`
	Address   string            `json:"address"`
	LastTick  int               `json:"last_tick"`
	LastHash  string            `json:"last_hash"` // Phase 6: Fork Detection
	LastSeen  time.Time         `json:"last_seen"`
	PublicKey ed25519.PublicKey `json:"-"`
	Status    string            `json:"status"`
}

// --- Phase 4.1: Consensus Structures ---

type LedgerEntry struct {
	Tick      int    `json:"tick"`
	Timestamp int64  `json:"timestamp"`
	PrevHash  string `json:"prev_hash"`
	FinalHash string `json:"final_hash"`
}

type LedgerPayload struct {
	UUID      string      `json:"uuid"`
	Tick      int         `json:"tick"`
	StampHash string      `json:"stamp_hash"`
	PeerCount int         `json:"peer_count"`
	Entry     LedgerEntry `json:"entry"`
}

// --- Globals ---

var (
	dbFile = "./data/ownworld.db"
	db     *sql.DB

	// Identity
	ServerUUID string
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey

	// Networking
	peers            = make(map[string]*Peer)
	peerLock         sync.RWMutex
	immigrationQueue = make(chan HandshakeRequest, 50)
	ipLimiters       = make(map[string]*rate.Limiter)
	ipLock           sync.Mutex
	
	// Phase 6: Public Discovery (Atomic Snapshot)
	mapSnapshot atomic.Value 

	// Time & Consensus
	CurrentTick   int           = 0
	PreviousHash  string        = "GENESIS"
	IsLeader      bool          = true
	LeaderUUID    string        = ""
	PhaseOffset   time.Duration = 0
	stateLock     sync.Mutex

	// Configuration (Phase 6)
	Config struct {
		CommandControl bool   // If false, disable User APIs
		PeeringMode    string // "promiscuous" or "strict"
	}

	// Logging
	InfoLog  *log.Logger
	ErrorLog *log.Logger
)

// --- Configuration ---

var BuildingCosts = map[string]map[string]int{
	"farm":          {"iron": 10},
	"well":          {"iron": 10},
	"iron_mine":     {"carbon": 50},
	"gold_mine":     {"iron": 500, "carbon": 100},
	"urban_housing": {"iron": 100, "carbon": 100},
}

// --- Initialization ---

func setupLogging() {
	logDir := "./logs"
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		os.Mkdir(logDir, 0755)
	}
	fInfo, _ := os.OpenFile(filepath.Join(logDir, "server.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	fErr, _ := os.OpenFile(filepath.Join(logDir, "error.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	InfoLog = log.New(fInfo, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLog = log.New(fErr, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
}

func initConfig() {
	// Phase 6.1: Node Modes
	Config.CommandControl = true
	if os.Getenv("OWNWORLD_COMMAND_CONTROL") == "false" {
		Config.CommandControl = false
	}
	
	Config.PeeringMode = "promiscuous"
	if mode := os.Getenv("OWNWORLD_PEERING_MODE"); mode == "strict" {
		Config.PeeringMode = "strict"
	}
}

func initDB() {
	if err := os.MkdirAll("./data", 0755); err != nil { panic(err) }
	dsn := dbFile + "?_journal_mode=WAL&_busy_timeout=5000"
	var err error
	db, err = sql.Open("sqlite3", dsn)
	if err != nil { panic(err) }
	if err = db.Ping(); err != nil { panic(err) }
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil { panic(err) }
	createSchema()
	initIdentity()
}

func createSchema() {
	schemaMut := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		global_uuid TEXT UNIQUE,
		username TEXT, password_hash TEXT,
		credits INTEGER DEFAULT 0, is_local BOOLEAN DEFAULT 0,
		ed25519_encrypted_private_key BLOB
	);
	CREATE TABLE IF NOT EXISTS solar_systems (
		id TEXT PRIMARY KEY, 
		x INTEGER, y INTEGER, z INTEGER,
		star_type TEXT, owner_uuid TEXT, tax_rate REAL DEFAULT 0.0
	);
	CREATE TABLE IF NOT EXISTS planets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, system_id TEXT, efficiency_seed TEXT, type TEXT,
		FOREIGN KEY(system_id) REFERENCES solar_systems(id)
	);
	CREATE TABLE IF NOT EXISTS colonies (
		id INTEGER PRIMARY KEY AUTOINCREMENT, system_id TEXT, owner_uuid TEXT, name TEXT, buildings_json TEXT,
		iron INTEGER DEFAULT 0, carbon INTEGER DEFAULT 0, water INTEGER DEFAULT 0, gold INTEGER DEFAULT 0, 
		platinum INTEGER DEFAULT 0, uranium INTEGER DEFAULT 0, diamond INTEGER DEFAULT 0, vegetation INTEGER DEFAULT 0,
		pop_laborers INTEGER DEFAULT 0, pop_specialists INTEGER DEFAULT 0, pop_elites INTEGER DEFAULT 0,
		stability_current REAL DEFAULT 100.0, stability_target REAL DEFAULT 100.0, martial_law BOOLEAN DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS fleets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, owner_uuid TEXT, status TEXT, fuel INTEGER DEFAULT 0,
		origin_system TEXT, dest_system TEXT, departure_tick INTEGER, arrival_tick INTEGER,
		start_coords TEXT, dest_coords TEXT,
		ark_ship INTEGER DEFAULT 0, fighters INTEGER DEFAULT 0, frigates INTEGER DEFAULT 0, haulers INTEGER DEFAULT 0
	);
	`
	schemaImmutable := `
	CREATE TABLE IF NOT EXISTS transaction_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT, tick INTEGER, action_type TEXT, payload_blob BLOB
	);
	CREATE INDEX IF NOT EXISTS idx_tx_tick ON transaction_log(tick);
	CREATE TABLE IF NOT EXISTS daily_snapshots (
		day_id INTEGER PRIMARY KEY, state_blob BLOB, final_hash TEXT
	);
	CREATE TABLE IF NOT EXISTS system_meta (
		key TEXT PRIMARY KEY, value TEXT
	);
	`
	if _, err := db.Exec(schemaMut); err != nil { panic(err) }
	if _, err := db.Exec(schemaImmutable); err != nil { panic(err) }
	
	// Resume Ledger
	var lastTick int
	var lastHash string
	err := db.QueryRow("SELECT tick, final_hash FROM ledger ORDER BY tick DESC LIMIT 1").Scan(&lastTick, &lastHash)
	if err == nil {
		CurrentTick = lastTick
		PreviousHash = lastHash
	}
}

type GenesisState struct {
	Seed      string `json:"seed"`
	Timestamp int64  `json:"timestamp"`
	PubKey    string `json:"pub_key"`
}

func initIdentity() {
	var uuid string
	err := db.QueryRow("SELECT value FROM system_meta WHERE key='server_uuid'").Scan(&uuid)
	if err == sql.ErrNoRows {
		InfoLog.Println("First Boot Detected. Generating Identity...")
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil { panic(err) }
		randBytes := make([]byte, 16); rand.Read(randBytes)
		genState := GenesisState{Seed: hex.EncodeToString(randBytes), Timestamp: time.Now().Unix(), PubKey: hex.EncodeToString(pub)}
		genJSON, _ := json.Marshal(genState)
		ServerUUID = hashBLAKE3(genJSON)
		tx, _ := db.Begin()
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('server_uuid', ?)", ServerUUID)
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('genesis_hash', ?)", hashBLAKE3(genJSON))
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('public_key', ?)", hex.EncodeToString(pub))
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('private_key', ?)", hex.EncodeToString(priv))
		tx.Commit()
		PrivateKey = priv; PublicKey = pub
		LeaderUUID = ServerUUID
	} else {
		ServerUUID = uuid
		var privStr, pubStr string
		db.QueryRow("SELECT value FROM system_meta WHERE key='private_key'").Scan(&privStr)
		db.QueryRow("SELECT value FROM system_meta WHERE key='public_key'").Scan(&pubStr)
		privBytes, _ := hex.DecodeString(privStr); pubBytes, _ := hex.DecodeString(pubStr)
		PrivateKey = ed25519.PrivateKey(privBytes); PublicKey = ed25519.PublicKey(pubBytes)
		LeaderUUID = ServerUUID
	}
}

// --- Phase 3.2 & 6.2: Security & Hardening ---

func getLimiter(ip string) *rate.Limiter {
	ipLock.Lock(); defer ipLock.Unlock()
	limiter, exists := ipLimiters[ip]
	if !exists { limiter = rate.NewLimiter(1, 3); ipLimiters[ip] = limiter }
	return limiter
}

func middlewareSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Layer 1: Rate Limit
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !getLimiter(ip).Allow() { http.Error(w, "Rate Limit", 429); return }

		contentType := r.Header.Get("Content-Type")

		// Mode A: Federation Traffic
		if contentType == "application/x-ownworld-fed" {
			if !strings.Contains(r.URL.Path, "handshake") {
				senderUUID := r.Header.Get("X-Server-UUID")
				peerLock.RLock(); _, known := peers[senderUUID]; peerLock.RUnlock()
				if !known { http.Error(w, "Unknown Peer", 403); return }
			}
			next.ServeHTTP(w, r); return
		}

		// Mode B: Client Traffic
		if contentType == "application/json" {
			// Phase 6.1: Node Modes Check
			if strings.HasPrefix(r.URL.Path, "/api/") && !Config.CommandControl {
				http.Error(w, "Node is in Infrastructure Mode (No User API)", 503)
				return
			}
			next.ServeHTTP(w, r); return
		}
		
		http.Error(w, "Bad Type", 415)
	})
}

// --- Phase 5: Simulation Logic ---

func GetEfficiency(planetID int, resource string) float64 {
	input := fmt.Sprintf("%d-%s", planetID, resource)
	hash := blake3.Sum256([]byte(input))
	val := binary.BigEndian.Uint16(hash[:2])
	return (float64(val)/65535.0)*2.4 + 0.1
}

func tickWorld() {
	stateLock.Lock()
	defer stateLock.Unlock()

	CurrentTick++

	// 1. Colony Simulation
	rows, _ := db.Query(`SELECT id, buildings_json, pop_laborers, iron, carbon, water, gold, vegetation FROM colonies`)
	type ColUpdate struct {
		ID int; Iron, Carbon, Water, Gold, Veg int
		PopLab int
	}
	var updates []ColUpdate

	for rows.Next() {
		var c Colony
		var bJson string
		rows.Scan(&c.ID, &bJson, &c.PopLaborers, &c.Iron, &c.Carbon, &c.Water, &c.Gold, &c.Vegetation)
		json.Unmarshal([]byte(bJson), &c.Buildings)
		pid := c.ID 
		effIron := GetEfficiency(pid, "iron")
		effWater := GetEfficiency(pid, "water")
		prodIron := int(float64(c.Buildings["iron_mine"]*10) * effIron)
		prodWater := int(float64(c.Buildings["well"]*20) * effWater)
		prodFood := int(float64(c.Buildings["farm"]*20))
		consFood := c.PopLaborers / 10 
		consWater := c.PopLaborers / 10
		c.Iron += prodIron
		c.Water += prodWater - consWater
		c.Vegetation += prodFood - consFood 
		if c.Water < 0 { c.Water = 0 }
		if c.Vegetation < 0 { c.Vegetation = 0 }
		if c.Water > 0 && c.Vegetation > 0 {
			c.PopLaborers += c.PopLaborers / 100 
		} else {
			c.PopLaborers -= c.PopLaborers / 50 
		}
		updates = append(updates, ColUpdate{
			ID: c.ID, Iron: c.Iron, Carbon: c.Carbon, Water: c.Water, Gold: c.Gold, Veg: c.Vegetation, PopLab: c.PopLaborers,
		})
	}
	rows.Close()
	tx, _ := db.Begin()
	stmt, _ := tx.Prepare(`UPDATE colonies SET iron=?, carbon=?, water=?, gold=?, vegetation=?, pop_laborers=? WHERE id=?`)
	for _, u := range updates {
		stmt.Exec(u.Iron, u.Carbon, u.Water, u.Gold, u.Veg, u.PopLab, u.ID)
	}
	stmt.Close(); tx.Commit()

	// 2. Fleet Movement
	fRows, _ := db.Query("SELECT id, dest_system, arrival_tick FROM fleets WHERE status='TRANSIT'")
	for fRows.Next() {
		var fid, arrival int
		var dest string
		fRows.Scan(&fid, &dest, &arrival)
		if CurrentTick >= arrival {
			db.Exec("UPDATE fleets SET status='ORBIT', origin_system=? WHERE id=?", dest, fid)
			InfoLog.Printf("Fleet %d arrived at %s", fid, dest)
		}
	}
	fRows.Close()

	// 3. Hash Chain
	payload := fmt.Sprintf("%d-%s", CurrentTick, PreviousHash)
	finalBytes := blake3.Sum256([]byte(payload))
	PreviousHash = hex.EncodeToString(finalBytes[:])

	recalculateLeader()
}

// --- Phase 4.2 & 6.2: Consensus & Fork Detection ---

func recalculateLeader() {
	peerLock.RLock()
	defer peerLock.RUnlock()

	type Candidate struct {
		UUID      string
		Tick      int
		PeerCount int 
	}
	candidates := []Candidate{{UUID: ServerUUID, Tick: CurrentTick, PeerCount: len(peers)}}
	for _, p := range peers {
		candidates = append(candidates, Candidate{UUID: p.UUID, Tick: p.LastTick, PeerCount: 0})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Tick != candidates[j].Tick {
			return candidates[i].Tick > candidates[j].Tick
		}
		return candidates[i].UUID > candidates[j].UUID
	})

	BestNode := candidates[0]
	LeaderUUID = BestNode.UUID
	IsLeader = (LeaderUUID == ServerUUID)

	allUUIDs := make([]string, 0, len(peers)+1)
	allUUIDs = append(allUUIDs, ServerUUID)
	for uuid := range peers { allUUIDs = append(allUUIDs, uuid) }
	sort.Strings(allUUIDs)

	myRank := 0
	for i, id := range allUUIDs {
		if id == ServerUUID { myRank = i; break }
	}

	totalNodes := len(allUUIDs)
	slotDuration := 5000 / totalNodes
	PhaseOffset = time.Duration(slotDuration * myRank) * time.Millisecond
}

func handleSyncLedger(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body); decompressed := decompressLZ4(body)
	var req LedgerPayload; json.Unmarshal(decompressed, &req)

	peerLock.Lock()
	defer peerLock.Unlock()

	// Phase 6.2: Fork Detection ("The Highlander Rule")
	if p, ok := peers[req.UUID]; ok {
		// If they are sending a new tick, their PREVIOUS hash must match what we recorded as their LAST hash.
		// Allow gap of 1 (normal), reject if history rewritten.
		if req.Tick == p.LastTick + 1 {
			if req.Entry.PrevHash != p.LastHash {
				ErrorLog.Printf("SECURITY: FORK DETECTED from %s. Expected Prev %s, Got %s. BANNING.", req.UUID, p.LastHash, req.Entry.PrevHash)
				delete(peers, req.UUID) // The Highlander Rule: There can be only one history.
				http.Error(w, "Fork Detected: Banned", 403)
				return
			}
		}
		
		p.LastTick = req.Tick
		p.LastHash = req.Entry.FinalHash // Update known state
		p.LastSeen = time.Now()
	}

	stateLock.Lock()
	defer stateLock.Unlock()

	if req.Tick > CurrentTick + 5 {
		InfoLog.Printf("SYNC SNAP: Jumping from %d to %d (Leader: %s)", CurrentTick, req.Tick, req.UUID)
		CurrentTick = req.Tick
		PreviousHash = req.Entry.FinalHash
	}
	w.WriteHeader(http.StatusOK)
}

// --- Phase 6.3: Public Discovery ---

func snapshotPeers() {
	ticker := time.NewTicker(60 * time.Second)
	for {
		<-ticker.C
		peerLock.RLock()
		
		// Convert map to slice for JSON
		list := make([]Peer, 0, len(peers))
		for _, p := range peers {
			list = append(list, *p)
		}
		peerLock.RUnlock()
		
		data, _ := json.Marshal(list)
		mapSnapshot.Store(data) // Atomic store
	}
}

func handleMap(w http.ResponseWriter, r *http.Request) {
	data := mapSnapshot.Load()
	if data == nil {
		w.Write([]byte("[]"))
		return
	}
	
	// Phase 6.3: Pagination (Zero-Allocation Slicing would happen here in full impl)
	// For now, serve full atomic blob.
	w.Header().Set("Content-Type", "application/json")
	w.Write(data.([]byte))
}

func runGameLoop() {
	InfoLog.Println("Starting Galaxy Engine...")
	ticker := time.NewTicker(5 * time.Second)
	
	for {
		<-ticker.C
		if PhaseOffset > 0 { time.Sleep(PhaseOffset) }
		tickWorld()
	}
}

// --- Handlers: Gameplay ---

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{Username, Password string}; json.NewDecoder(r.Body).Decode(&req)
	hash := hashBLAKE3([]byte(req.Password))
	var count int
	db.QueryRow("SELECT count(*) FROM users WHERE username=?", req.Username).Scan(&count)
	if count > 0 { http.Error(w, "Taken", 400); return }
	res, _ := db.Exec("INSERT INTO users (username, password_hash, is_local) VALUES (?,?, 1)", req.Username, hash)
	uid, _ := res.LastInsertId()
	sysUUID := fmt.Sprintf("sys-%d-%d", uid, time.Now().UnixNano())
	
	mrand.Seed(time.Now().UnixNano())
	x, y, z := mrand.Intn(100)-50, mrand.Intn(100)-50, mrand.Intn(100)-50 
	
	db.Exec("INSERT INTO solar_systems (id, x, y, z, star_type, owner_uuid) VALUES (?,?,?,?, 'G2V', ?)", sysUUID, x, y, z, ServerUUID)
	db.Exec("INSERT INTO planets (system_id, efficiency_seed, type) VALUES (?, ?, 'TERRAN')", sysUUID, "SEED")
	bJson, _ := json.Marshal(map[string]int{"farm": 5, "well": 5, "urban_housing": 10})
	db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, buildings_json, pop_laborers, water, vegetation, iron) 
	         VALUES (?, ?, ?, ?, 1000, 5000, 5000, 500)`, sysUUID, req.Username, req.Username+"'s Prime", string(bJson))
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "registered", "user_id": uid, "system_id": sysUUID})
}

func handleBuild(w http.ResponseWriter, r *http.Request) {
	var req struct { ColonyID int `json:"colony_id"`; Structure string `json:"structure"`; Amount int `json:"amount"` }
	json.NewDecoder(r.Body).Decode(&req)
	if req.Amount < 1 { req.Amount = 1 }
	cost, ok := BuildingCosts[req.Structure]
	if !ok { http.Error(w, "Unknown Structure", 400); return }
	stateLock.Lock(); defer stateLock.Unlock()
	var c Colony; var bJson string
	err := db.QueryRow("SELECT buildings_json, iron, carbon, water FROM colonies WHERE id=?", req.ColonyID).Scan(&bJson, &c.Iron, &c.Carbon, &c.Water)
	if err != nil { http.Error(w, "Colony Not Found", 404); return }
	neededIron := cost["iron"] * req.Amount; neededCarbon := cost["carbon"] * req.Amount
	if c.Iron < neededIron || c.Carbon < neededCarbon { http.Error(w, "Insufficient Funds", 402); return }
	json.Unmarshal([]byte(bJson), &c.Buildings)
	if c.Buildings == nil { c.Buildings = make(map[string]int) }
	c.Buildings[req.Structure] += req.Amount
	newBJson, _ := json.Marshal(c.Buildings)
	db.Exec("UPDATE colonies SET iron=iron-?, carbon=carbon-?, buildings_json=? WHERE id=?", neededIron, neededCarbon, string(newBJson), req.ColonyID)
	w.Write([]byte("Build Complete"))
}

// --- Networking Handlers ---

func processImmigration() {
	for req := range immigrationQueue {
		time.Sleep(2 * time.Second)

		// Phase 6.1: Peering Mode Check
		if Config.PeeringMode == "strict" {
			// In a real app, check a whitelist DB table here.
			// For this implementation, we simply reject everyone in Strict Mode unless hardcoded.
			// InfoLog.Printf("Strict Mode: Rejecting %s", req.UUID)
			continue
		}

		peerLock.RLock(); _, exists := peers[req.UUID]; peerLock.RUnlock()
		if exists { continue }
		var myGenHash string
		db.QueryRow("SELECT value FROM system_meta WHERE key='genesis_hash'").Scan(&myGenHash)
		if req.GenesisHash != myGenHash { continue }
		pubBytes, _ := hex.DecodeString(req.PublicKey)
		peer := &Peer{
			UUID: req.UUID, Address: req.Address, LastSeen: time.Now(), 
			PublicKey: ed25519.PublicKey(pubBytes),
			Status: "VERIFIED",
		}
		peerLock.Lock(); peers[req.UUID] = peer; peerLock.Unlock()
		InfoLog.Printf("IMMIGRATION: Peer %s added.", req.UUID)
		recalculateLeader()
	}
}

func handleHandshake(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body); decompressed := decompressLZ4(body)
	var req HandshakeRequest; json.Unmarshal(decompressed, &req)
	select {
	case immigrationQueue <- req:
		w.WriteHeader(http.StatusAccepted); w.Write([]byte("Queued"))
	default:
		http.Error(w, "Full", 503)
	}
}

func main() {
	setupLogging()
	initConfig()
	initDB()

	InfoLog.Println("OWNWORLD BOOT SEQUENCE")
	InfoLog.Println("Phase 1-3: Systems... [OK]")
	InfoLog.Println("Phase 4: Consensus... [OK]")
	InfoLog.Println("Phase 5: Simulation... [OK]")
	InfoLog.Printf("Phase 6: Infrastructure... [OK] (Mode: %s, Ctrl: %v)", Config.PeeringMode, Config.CommandControl)
	
	go processImmigration()
	go snapshotPeers()
	go runGameLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/federation/handshake", handleHandshake)
	mux.HandleFunc("/federation/sync", handleSyncLedger)
	mux.HandleFunc("/federation/map", handleMap)
	
	mux.HandleFunc("/api/register", handleRegister)
	mux.HandleFunc("/api/build", handleBuild)
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"uuid": ServerUUID, "tick": CurrentTick, "leader": LeaderUUID,
		})
	})

	server := &http.Server{
		Addr: ":8080", Handler: middlewareSecurity(mux),
		ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second,
	}
	InfoLog.Printf("Node %s Listening on :8080", ServerUUID)
	server.ListenAndServe()
}
