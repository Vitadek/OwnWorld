package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// --- Structs ---

type Colony struct {
	ID            int            `json:"id"`
	OwnerID       int            `json:"owner_id"`
	Name          string         `json:"name"`
	Location      []int          `json:"location"`
	Buildings     map[string]int `json:"buildings"`
	
	// Stats
	Population    int            `json:"population"`
	
	// Resources (Expanded for strict JSON compliance)
	Food          int            `json:"food"`
	Water         int            `json:"water"`
	Iron          int            `json:"iron"`
	Diamond       int            `json:"diamond"`
	Platinum      int            `json:"platinum"`
	Starfuel      int            `json:"starfuel"`
	
	// Society
	Health        float64        `json:"health"`
	Intelligence  float64        `json:"intelligence"`
	Crime         float64        `json:"crime"`
	Happiness     float64        `json:"happiness"`
	
	// Defense
	DefenseRating int            `json:"defense_rating"`
}

type Fleet struct {
	ID           int    `json:"id"`
	OriginColony int    `json:"origin_colony"`
	Status       string `json:"status"`
	
	// Composition
	Fighters     int `json:"fighters"`
	Probes       int `json:"probes"`
	Colonizers   int `json:"colonizers"`
	Destroyers   int `json:"destroyers"`
}

type IntelReport struct {
	ServerUUID string   `json:"server_uuid"`
	ServerURL  string   `json:"server_url"`
	Location   []int    `json:"location"`
	Colonies   []Colony `json:"colonies"`
	Peers      []string `json:"peers"`
	LastStamp  string   `json:"last_stamp"`
	LastTick   int      `json:"last_tick"`
	Status     string   `json:"status"`
}

type LedgerEntry struct {
	Tick        int    `json:"tick"`
	Timestamp   int64  `json:"timestamp"`
	PrevHash    string `json:"prev_hash"`
	StateHash   string `json:"state_hash"`
	FinalHash   string `json:"final_hash"`
}

type LedgerPayload struct {
	UUID      string      `json:"uuid"`
	Tick      int         `json:"tick"`
	StampHash string      `json:"stamp_hash"`
	Entry     LedgerEntry `json:"entry"`
	
	// Election Data
	PeerCount int   `json:"peer_count"`
	Genesis   int64 `json:"genesis"`
}

// --- Globals ---

var (
	ServerUUID   string
	ServerLoc    []int
	MyGenesis    int64 // When this server was born
	
	dbFile       = "./data/ownworld.db"
	db           *sql.DB
	intelMap     = make(map[string]IntelReport)
	stateLock    sync.Mutex
	
	// Ledger & Sync State
	CurrentTick  int = 0
	PreviousHash string = "GENESIS_BLOCK"
	
	// Synchronization
	IsLeader     bool = true // Default to leader until we meet someone better
	LeaderUUID   string
	PhaseOffset  time.Duration = 0 // Adjustment to align ticks

	// Logger
	InfoLog  *log.Logger
	ErrorLog *log.Logger
)

// --- Configuration ---

var ShipCosts = map[string]map[string]int{
	"probe":     {"iron": 50, "starfuel": 10},
	"fighter":   {"iron": 500, "diamond": 50, "starfuel": 50},
	"colonizer": {"iron": 5000, "food": 5000, "water": 5000, "starfuel": 500},
	"destroyer": {"iron": 10000, "diamond": 5000, "platinum": 1000, "starfuel": 2000},
}

var BuildingCosts = map[string]map[string]int{
	"farm":            {"iron": 10},
	"well":            {"iron": 10},
	"school":          {"iron": 50},
	"iron_mine":       {"food": 500},
	"police_station":  {"iron": 200},
	"diamond_mine":    {"iron": 1000},
	"hospital":        {"iron": 500, "diamond": 10},
	"platinum_mine":   {"iron": 2000, "diamond": 500},
	"defense_battery": {"iron": 2000, "diamond": 100},
	"rd_lab":          {"diamond": 200, "platinum": 50},
	"starfuel_refinery":{"iron": 10000, "diamond": 2000, "platinum": 1000},
}

// --- Initialization ---

func setupLogging() {
	logDir := "./logs"
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		os.Mkdir(logDir, 0755)
	}
	fInfo, _ := os.OpenFile(filepath.Join(logDir, "server.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	fErr, _ := os.OpenFile(filepath.Join(logDir, "error.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	mwInfo := io.MultiWriter(os.Stdout, fInfo)
	mwErr := io.MultiWriter(os.Stderr, fErr)
	InfoLog = log.New(mwInfo, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLog = log.New(mwErr, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
}

func initDB() {
	os.MkdirAll("./data", 0755)
	var err error
	db, err = sql.Open("sqlite3", dbFile)
	if err != nil { panic(err) }

	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE, password_hash TEXT, star_coins INTEGER DEFAULT 1000
	);
	CREATE TABLE IF NOT EXISTS colonies (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, name TEXT,
		loc_x INTEGER, loc_y INTEGER, loc_z INTEGER, buildings_json TEXT,
		population INTEGER DEFAULT 1000,
		food INTEGER DEFAULT 5000, water INTEGER DEFAULT 5000,
		iron INTEGER DEFAULT 500, diamond INTEGER DEFAULT 0, 
		platinum INTEGER DEFAULT 0, starfuel INTEGER DEFAULT 0,
		health REAL DEFAULT 100.0, intelligence REAL DEFAULT 50.0, 
		crime REAL DEFAULT 0.0, happiness REAL DEFAULT 100.0
	);
	CREATE TABLE IF NOT EXISTS fleets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, origin_colony INTEGER,
		status TEXT,
		fighters INTEGER, probes INTEGER, colonizers INTEGER, destroyers INTEGER
	);
	CREATE TABLE IF NOT EXISTS ledger (
		tick INTEGER PRIMARY KEY,
		timestamp INTEGER,
		prev_hash TEXT,
		state_dump_hash TEXT,
		final_hash TEXT
	);
	-- Metadata for Server Identity
	CREATE TABLE IF NOT EXISTS meta (
		key TEXT PRIMARY KEY,
		value TEXT
	);
	`
	if _, err := db.Exec(schema); err != nil { panic(err) }
	
	// Server Identity
	ServerUUID = fmt.Sprintf("sector-%d", time.Now().UnixNano())
	hash := sha256.Sum256([]byte(ServerUUID))
	ServerLoc = []int{int(hash[0])*100+int(hash[1]), int(hash[2])*100+int(hash[3]), int(hash[4])*100+int(hash[5])}

	// Load or Create Genesis Time
	var genStr string
	err = db.QueryRow("SELECT value FROM meta WHERE key='genesis'").Scan(&genStr)
	if err != nil {
		MyGenesis = time.Now().Unix()
		db.Exec("INSERT INTO meta (key, value) VALUES ('genesis', ?)", fmt.Sprintf("%d", MyGenesis))
	} else {
		fmt.Sscanf(genStr, "%d", &MyGenesis)
	}
	LeaderUUID = ServerUUID // Assume I am leader until proven otherwise

	// Resume Ledger
	var lastHash string
	var lastTick int
	err = db.QueryRow("SELECT tick, final_hash FROM ledger ORDER BY tick DESC LIMIT 1").Scan(&lastTick, &lastHash)
	if err == nil {
		CurrentTick = lastTick
		PreviousHash = lastHash
		InfoLog.Printf("Resumed Ledger at Tick %d", CurrentTick)
	}
}

// --- Ledger & Sync ---

func snapshotWorld() {
	CurrentTick++
	
	rows, _ := db.Query("SELECT id, population, iron, starfuel FROM colonies ORDER BY id ASC")
	defer rows.Close()
	
	var buffer bytes.Buffer
	for rows.Next() {
		var id, pop, i, s int
		rows.Scan(&id, &pop, &i, &s)
		buffer.WriteString(fmt.Sprintf("%d:%d:%d:%d|", id, pop, i, s))
	}
	
	stateHashBytes := sha256.Sum256(buffer.Bytes())
	stateHash := hex.EncodeToString(stateHashBytes[:])

	ts := time.Now().Unix()
	payload := fmt.Sprintf("%d-%d-%s-%s", CurrentTick, ts, PreviousHash, stateHash)
	finalBytes := sha256.Sum256([]byte(payload))
	finalHash := hex.EncodeToString(finalBytes[:])

	db.Exec("INSERT INTO ledger (tick, timestamp, prev_hash, state_dump_hash, final_hash) VALUES (?,?,?,?,?)", 
		CurrentTick, ts, PreviousHash, stateHash, finalHash)
	
	PreviousHash = finalHash

	if CurrentTick % 5 == 0 {
		go broadcastLedger()
	}
}

func broadcastLedger() {
	stateLock.Lock()
	peers := make(map[string]string)
	peerCount := len(intelMap)
	for _, intel := range intelMap {
		if intel.ServerURL != "" { peers[intel.ServerUUID] = intel.ServerURL }
	}
	stateLock.Unlock()

	entry := LedgerEntry{Tick: CurrentTick, Timestamp: time.Now().Unix(), FinalHash: PreviousHash}
	db.QueryRow("SELECT prev_hash FROM ledger WHERE tick=?", CurrentTick).Scan(&entry.PrevHash)

	payload := LedgerPayload{
		UUID: ServerUUID, Tick: CurrentTick, StampHash: PreviousHash, Entry: entry,
		PeerCount: peerCount, Genesis: MyGenesis, // Election Data
	}

	data, _ := json.Marshal(payload)
	for _, url := range peers {
		go func(target string) {
			http.Post(target+"/federation/sync_ledger", "application/json", bytes.NewBuffer(data))
		}(url)
	}
}

func tickWorld() {
	stateLock.Lock()
	defer stateLock.Unlock()

	rows, _ := db.Query(`SELECT id, population, food, water, buildings_json, iron, diamond, platinum, starfuel, health, intelligence, crime, happiness FROM colonies`)
	type ColUpdate struct {
		ID int; Pop, Food, Water, Iron, Dia, Plat, Fuel int
		Health, Intel, Crime, Happy float64
	}
	var updates []ColUpdate

	for rows.Next() {
		var u ColUpdate; var bJson string
		rows.Scan(&u.ID, &u.Pop, &u.Food, &u.Water, &bJson, &u.Iron, &u.Dia, &u.Plat, &u.Fuel, &u.Health, &u.Intel, &u.Crime, &u.Happy)
		var b map[string]int; json.Unmarshal([]byte(bJson), &b)

		eff := 1.0 + (float64(b["rd_lab"]) * (u.Intel / 200.0))
		u.Food += int(float64(b["farm"]*100) * eff); u.Water += int(float64(b["well"]*100) * eff)
		u.Iron += int(float64(b["iron_mine"]*10) * eff); u.Dia += int(float64(b["diamond_mine"]*5) * eff)
		u.Plat += int(float64(b["platinum_mine"]*2) * eff); u.Fuel += int(float64(b["starfuel_refinery"]*1) * eff)

		targetCrime := (float64(u.Pop)/1000.0) - (float64(b["police_station"])*2.0)
		if targetCrime < 0 { targetCrime = 0 }
		u.Crime += (targetCrime - u.Crime) * 0.1

		crowding := float64(u.Pop) / (5000.0 * math.Log10(float64(u.Pop)+10))
		healing := float64(b["hospital"]) * 1.5 * (1.0 + u.Intel/100.0)
		u.Health += (u.Health - crowding + healing - u.Health) * 0.1
		if u.Health > 100 { u.Health = 100 }

		targetHappy := u.Health - (u.Crime * 5.0)
		if u.Food < u.Pop { targetHappy -= 20 }
		if u.Water < u.Pop { targetHappy -= 20 }
		u.Happy += (targetHappy - u.Happy) * 0.1

		cons := int(float64(u.Pop) / 3.0); if cons < 1 && u.Pop > 0 { cons = 1 }
		u.Food -= cons; u.Water -= cons
		if u.Food < 0 { u.Food = 0 }; if u.Water < 0 { u.Water = 0 }

		logDamp := math.Log(float64(u.Pop) + 10.0)
		if u.Happy > 60 && u.Food > 0 { u.Pop += int((float64(u.Pop)*0.01) / logDamp) }
		if u.Food == 0 { u.Pop -= int((float64(u.Pop)*0.02) / logDamp) }
		if u.Pop < 1 { u.Pop = 1 }

		updates = append(updates, u)
	}
	rows.Close()

	tx, _ := db.Begin()
	stmt, _ := tx.Prepare(`UPDATE colonies SET population=?, food=?, water=?, iron=?, diamond=?, platinum=?, starfuel=?, health=?, intelligence=?, crime=?, happiness=? WHERE id=?`)
	for _, u := range updates {
		stmt.Exec(u.Pop, u.Food, u.Water, u.Iron, u.Dia, u.Plat, u.Fuel, u.Health, u.Intel, u.Crime, u.Happy, u.ID)
	}
	stmt.Close(); tx.Commit()

	snapshotWorld()
	InfoLog.Printf("Tick %d. Leader: %s. Phase Offset: %v", CurrentTick, LeaderUUID[:8], PhaseOffset)
}

// --- Handlers ---

func handleSyncLedger(w http.ResponseWriter, r *http.Request) {
	var req LedgerPayload; json.NewDecoder(r.Body).Decode(&req)
	
	stateLock.Lock()
	defer stateLock.Unlock()
	
	intel, exists := intelMap[req.UUID]
	if !exists { return }

	// --- ELECTION LOGIC ---
	myPeerCount := len(intelMap)
	remoteWin := false

	// Rule 1: Most Peers Wins
	if req.PeerCount > myPeerCount {
		remoteWin = true
	} else if req.PeerCount == myPeerCount {
		// Rule 2: Oldest Wins (Lower Genesis Timestamp)
		if req.Genesis < MyGenesis {
			remoteWin = true
		}
	}

	if remoteWin {
		IsLeader = false
		LeaderUUID = req.UUID
		
		// CALCULATE PHASE OFFSET
		// Remote timestamp is when they processed the tick. 
		// We want to process our next tick at their (timestamp + 5s).
		// Currently, our main loop sleeps 5s. We need to adjust that sleep.
		
		// Estimate latency (very rough)
		latency := 50 * time.Millisecond 
		remoteTickTime := time.Unix(req.Entry.Timestamp, 0).Add(latency)
		nextTarget := remoteTickTime.Add(5 * time.Second)
		
		// How long until the target time?
		timeUntil := time.Until(nextTarget)
		
		// If we are significantly misaligned (>100ms), adjust PhaseOffset
		// This will be picked up by the main loop
		if math.Abs(timeUntil.Seconds() - 5.0) > 0.1 {
			PhaseOffset = timeUntil - (5 * time.Second)
			InfoLog.Printf("Syncing to Leader %s. Adjusting clock by %v", req.UUID[:8], PhaseOffset)
		}
	} else {
		// I am stronger (or equal/older), I remain leader
		IsLeader = true
		LeaderUUID = ServerUUID
		PhaseOffset = 0
	}

	// --- Ledger Verification ---
	isValid := false
	if intel.LastStamp == "" || req.Entry.PrevHash == intel.LastStamp {
		isValid = true
	} else {
		ErrorLog.Printf("LEDGER MISMATCH %s", req.UUID)
	}

	if isValid {
		intel.LastStamp = req.Entry.FinalHash
		intel.LastTick = req.Entry.Tick
		intel.Status = "VERIFIED"
		intelMap[req.UUID] = intel
	} else {
		intel.Status = "AUDIT_REQUIRED"
		intelMap[req.UUID] = intel
		go requestAudit(intel.ServerURL, req.UUID, intel.LastTick, req.Entry.Tick)
	}
}

func requestAudit(url, uuid string, startTick, endTick int) {
	payload := map[string]int{"start": startTick, "end": endTick}
	data, _ := json.Marshal(payload)
	resp, err := http.Post(url+"/federation/audit", "application/json", bytes.NewBuffer(data))
	if err != nil { return }
	var history []LedgerEntry
	json.NewDecoder(resp.Body).Decode(&history)
	InfoLog.Printf("Audit received from %s", uuid)
}

func handleAudit(w http.ResponseWriter, r *http.Request) {
	var req struct { Start int; End int }; json.NewDecoder(r.Body).Decode(&req)
	if req.End - req.Start > 100 { req.End = req.Start + 100 }
	rows, _ := db.Query("SELECT tick, timestamp, prev_hash, final_hash FROM ledger WHERE tick >= ? AND tick <= ?", req.Start, req.End)
	var history []LedgerEntry
	for rows.Next() {
		var l LedgerEntry
		rows.Scan(&l.Tick, &l.Timestamp, &l.PrevHash, &l.FinalHash)
		history = append(history, l)
	}
	json.NewEncoder(w).Encode(history)
}

func handleAddPeer(w http.ResponseWriter, r *http.Request) {
	var req struct { TargetURL string }; json.NewDecoder(r.Body).Decode(&req)
	
	// Count peers for election data
	stateLock.Lock(); pc := len(intelMap); stateLock.Unlock()

	me := LedgerPayload{
		UUID: ServerUUID, Tick: CurrentTick, StampHash: PreviousHash,
		PeerCount: pc, Genesis: MyGenesis,
	}
	data, _ := json.Marshal(me)

	InfoLog.Printf("Handshaking %s", req.TargetURL)
	resp, err := http.Post(req.TargetURL+"/federation/handshake", "application/json", bytes.NewBuffer(data))
	if err != nil { ErrorLog.Printf("Handshake err: %v", err); http.Error(w, "Fail", 500); return }

	var remote LedgerPayload; json.NewDecoder(resp.Body).Decode(&remote)
	status := "VERIFIED"
	if remote.Tick == 0 || remote.StampHash == "" { status = "UNVERIFIED" }

	stateLock.Lock()
	intelMap[remote.UUID] = IntelReport{
		ServerUUID: remote.UUID, ServerURL: req.TargetURL, 
		LastStamp: remote.StampHash, LastTick: remote.Tick, Status: status,
	}
	stateLock.Unlock()

	json.NewEncoder(w).Encode(map[string]string{"status": status, "uuid": remote.UUID})
}

func handleFederationHandshake(w http.ResponseWriter, r *http.Request) {
	var remote LedgerPayload; json.NewDecoder(r.Body).Decode(&remote)
	InfoLog.Printf("Handshake from %s", remote.UUID)
	stateLock.Lock(); pc := len(intelMap); stateLock.Unlock()
	me := LedgerPayload{
		UUID: ServerUUID, Tick: CurrentTick, StampHash: PreviousHash,
		PeerCount: pc, Genesis: MyGenesis,
	}
	json.NewEncoder(w).Encode(me)
}

func handleReceiveFleet(w http.ResponseWriter, r *http.Request) {
	var req struct { OriginUUID string; Fleet Fleet; StampHash string; StampTick int }
	json.NewDecoder(r.Body).Decode(&req)
	
	stateLock.Lock(); intel, exists := intelMap[req.OriginUUID]; stateLock.Unlock()
	if exists {
		if intel.Status == "AUDIT_REQUIRED" {
			json.NewEncoder(w).Encode(map[string]string{"error": "Security Alert: Audit Required"})
			return
		}
	}

	if len(req.StampHash) != 64 {
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid Security Stamp"})
		return
	}

	InfoLog.Printf("Fleet accepted from %s", req.OriginUUID)
	resp := map[string]interface{}{"message": "Fleet Accepted"}

	if req.Fleet.Probes > 0 {
		rows, _ := db.Query("SELECT id, name, population, buildings_json FROM colonies")
		var cols []Colony
		for rows.Next() {
			var c Colony; var bJson string; rows.Scan(&c.ID, &c.Name, &c.Population, &bJson)
			var b map[string]int; json.Unmarshal([]byte(bJson), &b)
			c.DefenseRating = b["defense_battery"] * 1000
			cols = append(cols, c)
		}
		var peers []string
		stateLock.Lock(); for uuid := range intelMap { peers = append(peers, uuid) }; stateLock.Unlock()
		resp["intel"] = IntelReport{ServerUUID: ServerUUID, Location: ServerLoc, Colonies: cols, Peers: peers}
		resp["scan_result"] = "Sector Mapped."
	}

	if req.Fleet.Colonizers > 0 {
		bJson, _ := json.Marshal(map[string]int{"farm":5, "well":5})
		db.Exec(`INSERT INTO colonies (user_id, name, population, food, water, buildings_json) VALUES (0, 'Outpost', 500, 2000, 2000, ?)`, string(bJson))
		resp["colonization"] = "Outpost Established."
	}
	
	if req.Fleet.Destroyers > 0 {
		var tid, pop int; var bJson string
		err := db.QueryRow("SELECT id, population, buildings_json FROM colonies ORDER BY RANDOM() LIMIT 1").Scan(&tid, &pop, &bJson)
		if err == nil {
			var b map[string]int; json.Unmarshal([]byte(bJson), &b)
			def := b["defense_battery"] * 2000
			atk := req.Fleet.Destroyers * 2500
			if atk > def {
				db.Exec("DELETE FROM colonies WHERE id=?", tid)
				resp["combat_log"] = fmt.Sprintf("VICTORY. Colony Destroyed (Atk %d vs Def %d)", atk, def)
			} else {
				resp["combat_log"] = fmt.Sprintf("DEFEAT. Fleet Intercepted (Atk %d vs Def %d)", atk, def)
			}
		} else { resp["combat_log"] = "No targets found." }
	}

	json.NewEncoder(w).Encode(resp)
}

func handleFleetLaunch(w http.ResponseWriter, r *http.Request) {
	var req struct{FleetID int; TargetURL string}; json.NewDecoder(r.Body).Decode(&req)
	var f Fleet
	err := db.QueryRow("SELECT fighters, probes, colonizers, destroyers FROM fleets WHERE id=?", req.FleetID).Scan(&f.Fighters, &f.Probes, &f.Colonizers, &f.Destroyers)
	if err != nil { http.Error(w, "Fleet not found", 404); return }

	payload := map[string]interface{}{
		"origin_uuid": ServerUUID, "fleet": f,
		"stamp_hash": PreviousHash, "stamp_tick": CurrentTick,
	}
	data, _ := json.Marshal(payload)
	resp, err := http.Post(req.TargetURL+"/federation/receive_fleet", "application/json", bytes.NewBuffer(data))
	
	if err != nil { ErrorLog.Printf("Launch failed: %v", err); http.Error(w, "Unreachable", 500); return }

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	
	if errStr, ok := result["error"]; ok {
		http.Error(w, fmt.Sprintf("Remote Rejected: %s", errStr), 403)
		return 
	}

	if f.Probes > 0 {
		if intel, ok := result["intel"]; ok {
			bits, _ := json.Marshal(intel)
			var report IntelReport; json.Unmarshal(bits, &report)
			report.ServerURL = req.TargetURL
			stateLock.Lock(); intelMap[report.ServerUUID] = report; stateLock.Unlock()
		}
	}
	db.Exec("DELETE FROM fleets WHERE id=?", req.FleetID)
	json.NewEncoder(w).Encode(result)
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{Username, Password string}; json.NewDecoder(r.Body).Decode(&req)
	hash := sha256.Sum256([]byte(req.Password)); hashStr := hex.EncodeToString(hash[:])
	res, err := db.Exec("INSERT INTO users (username, password_hash) VALUES (?,?)", req.Username, hashStr)
	if err != nil { http.Error(w, "Taken", 400); return }
	uid, _ := res.LastInsertId()
	bJson, _ := json.Marshal(map[string]int{"farm":10, "well":10, "school":1, "iron_mine":5})
	resCol, _ := db.Exec(`INSERT INTO colonies (user_id, name, population, food, water, iron, buildings_json, loc_x, loc_y) VALUES (?, ?, 1000, 20000, 20000, 1000, ?, ?, ?)`, uid, req.Username+"'s Prime", string(bJson), rand.Intn(100), rand.Intn(100))
	colID, _ := resCol.LastInsertId()
	db.Exec(`INSERT INTO fleets (user_id, origin_colony, status, fighters, probes, colonizers, destroyers) VALUES (?, ?, 'IDLE', 0, 1, 0, 0)`, uid, colID)
	InfoLog.Printf("New User: %s", req.Username)
	json.NewEncoder(w).Encode(map[string]int{"user_id": int(uid)})
}

func handleState(w http.ResponseWriter, r *http.Request) {
	uidStr := r.Header.Get("X-User-ID")
	var uid int; fmt.Sscanf(uidStr, "%d", &uid)
	type StateResp struct { ServerUUID string; ServerLoc []int; Intel map[string]IntelReport; MyColonies []Colony; MyFleets []Fleet; Costs map[string]map[string]int; ShipCosts map[string]map[string]int }
	resp := StateResp{ServerUUID: ServerUUID, ServerLoc: ServerLoc, Intel: intelMap, Costs: BuildingCosts, ShipCosts: ShipCosts}
	rows, _ := db.Query(`SELECT id, name, buildings_json, population, food, water, iron, diamond, platinum, starfuel, health, intelligence, crime, happiness FROM colonies WHERE user_id=?`, uid)
	for rows.Next() {
		var c Colony; var bJson string
		rows.Scan(&c.ID, &c.Name, &bJson, &c.Population, &c.Food, &c.Water, &c.Iron, &c.Diamond, &c.Platinum, &c.Starfuel, &c.Health, &c.Intelligence, &c.Crime, &c.Happiness)
		json.Unmarshal([]byte(bJson), &c.Buildings); resp.MyColonies = append(resp.MyColonies, c)
	}
	rows.Close()
	fRows, _ := db.Query("SELECT id, origin_colony, fighters, probes, colonizers, destroyers FROM fleets WHERE origin_colony IN (SELECT id FROM colonies WHERE user_id=?)", uid)
	for fRows.Next() {
		var f Fleet; fRows.Scan(&f.ID, &f.OriginColony, &f.Fighters, &f.Probes, &f.Colonizers, &f.Destroyers)
		resp.MyFleets = append(resp.MyFleets, f)
	}
	json.NewEncoder(w).Encode(resp)
}

func handleBuild(w http.ResponseWriter, r *http.Request) {
	var req struct { ColonyID int `json:"colony_id"`; Structure string `json:"structure"`; Amount int `json:"amount"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil { http.Error(w, "JSON", 400); return }
	if req.Amount < 1 { req.Amount = 1 }
	unitCost := BuildingCosts[req.Structure]; if unitCost == nil { http.Error(w, "Unknown", 400); return }
	var c Colony; var bJson string
	db.QueryRow("SELECT buildings_json, iron, diamond, platinum, food FROM colonies WHERE id=?", req.ColonyID).Scan(&bJson, &c.Iron, &c.Diamond, &c.Platinum, &c.Food)
	tIron := unitCost["iron"]*req.Amount; tDia := unitCost["diamond"]*req.Amount; tPlat := unitCost["platinum"]*req.Amount; tFood := unitCost["food"]*req.Amount 
	if c.Iron < tIron || c.Diamond < tDia || c.Platinum < tPlat || c.Food < tFood { http.Error(w, "Funds", 400); return }
	db.Exec("UPDATE colonies SET iron=iron-?, diamond=diamond-?, platinum=platinum-?, food=food-? WHERE id=?", tIron, tDia, tPlat, tFood, req.ColonyID)
	var b map[string]int; json.Unmarshal([]byte(bJson), &b); b[req.Structure] += req.Amount
	newJson, _ := json.Marshal(b); db.Exec("UPDATE colonies SET buildings_json=? WHERE id=?", string(newJson), req.ColonyID)
	w.Write([]byte("OK"))
}

func handleShipBuild(w http.ResponseWriter, r *http.Request) {
	var req struct{ColonyID int; ShipType string; Amount int}; json.NewDecoder(r.Body).Decode(&req)
	if req.Amount < 1 { req.Amount = 1 }
	cost := ShipCosts[req.ShipType]; if cost == nil { http.Error(w, "Unknown", 400); return }
	var c Colony; db.QueryRow("SELECT iron, diamond, platinum, starfuel, food, water FROM colonies WHERE id=?", req.ColonyID).Scan(&c.Iron, &c.Diamond, &c.Platinum, &c.Starfuel, &c.Food, &c.Water)
	tIron := cost["iron"]*req.Amount; tDia := cost["diamond"]*req.Amount; tPlat := cost["platinum"]*req.Amount; tFuel := cost["starfuel"]*req.Amount; tFood := cost["food"]*req.Amount; tWater := cost["water"]*req.Amount
	if c.Iron < tIron || c.Diamond < tDia || c.Platinum < tPlat || c.Starfuel < tFuel || c.Food < tFood || c.Water < tWater { http.Error(w, "Funds", 400); return }
	db.Exec("UPDATE colonies SET iron=iron-?, diamond=diamond-?, platinum=platinum-?, starfuel=starfuel-?, food=food-?, water=water-? WHERE id=?", tIron, tDia, tPlat, tFuel, tFood, tWater, req.ColonyID)
	var fid int; err := db.QueryRow("SELECT id FROM fleets WHERE origin_colony=? AND status='IDLE'", req.ColonyID).Scan(&fid)
	if err == sql.ErrNoRows { res, _ := db.Exec("INSERT INTO fleets (origin_colony, status, fighters, probes, colonizers, destroyers) VALUES (?, 'IDLE', 0,0,0,0)", req.ColonyID); id64, _ := res.LastInsertId(); fid = int(id64) }
	col := fmt.Sprintf("%ss", req.ShipType); db.Exec(fmt.Sprintf("UPDATE fleets SET %s = %s + ? WHERE id=?", col, col), req.Amount, fid)
	w.Write([]byte("OK"))
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct{Username, Password string}; json.NewDecoder(r.Body).Decode(&req)
	hash := sha256.Sum256([]byte(req.Password)); hashStr := hex.EncodeToString(hash[:])
	var id int; err := db.QueryRow("SELECT id FROM users WHERE username=? AND password_hash=?", req.Username, hashStr).Scan(&id)
	if err!=nil { http.Error(w,"Fail",401); return }
	json.NewEncoder(w).Encode(map[string]int{"user_id": id})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-User-ID")
		if r.Method == "OPTIONS" { return }
		next.ServeHTTP(w, r)
	})
}

func main() {
	daemonPtr := flag.Bool("d", false, "Run in background (daemon mode)")
	flag.Parse()

	if *daemonPtr {
		if os.Getenv("OWNWORLD_DAEMON") != "1" {
			cmd := exec.Command(os.Args[0], "-d")
			cmd.Env = append(os.Environ(), "OWNWORLD_DAEMON=1")
			cmd.Start()
			fmt.Println("OwnWorld Server started in background. PID:", cmd.Process.Pid)
			os.Exit(0)
		}
	}

	setupLogging()
	initDB()
	
	InfoLog.Println("Starting Game Loop...")
	go func() {
		for {
			startTime := time.Now()
			tickWorld()
			sleepTime := (5 * time.Second) + PhaseOffset
			if sleepTime < 100*time.Millisecond { sleepTime = 100 * time.Millisecond }
			PhaseOffset = 0 
			elapsed := time.Since(startTime)
			finalSleep := sleepTime - elapsed
			if finalSleep > 0 { time.Sleep(finalSleep) }
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/login", handleLogin)
	mux.HandleFunc("/register", handleRegister)
	mux.HandleFunc("/state", handleState)
	mux.HandleFunc("/build", handleBuild)
	mux.HandleFunc("/build/ship", handleShipBuild)
	mux.HandleFunc("/fleet/launch", handleFleetLaunch)
	mux.HandleFunc("/federation/handshake", handleFederationHandshake)
	mux.HandleFunc("/federation/add_peer", handleAddPeer)
	mux.HandleFunc("/federation/receive_fleet", handleReceiveFleet)
	mux.HandleFunc("/federation/sync_ledger", handleSyncLedger)
	mux.HandleFunc("/federation/audit", handleAudit)

	InfoLog.Printf("World %s Listening on :8080", ServerUUID)
	if !*daemonPtr {
		fmt.Printf("World %s Listening on :8080\nLogs at ./logs/\n", ServerUUID)
	}
	
	http.ListenAndServe(":8080", corsMiddleware(mux))
}
