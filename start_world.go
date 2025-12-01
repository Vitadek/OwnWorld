package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"os"
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
	Food          int            `json:"food"`
	Water         int            `json:"water"`
	
	// Resources
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
	Location   []int    `json:"location"` // The remote server's coordinates
	Colonies   []Colony `json:"colonies"`
	Peers      []string `json:"peers"`    // Gossip: Other networks this server knows
}

// --- Globals ---

var (
	ServerUUID string
	ServerLoc  []int // [x, y, z] Deterministic location
	dbFile     = "./data/ownworld.db"
	db         *sql.DB
	intelMap   = make(map[string]IntelReport) // Cache of scanned worlds
	stateLock  sync.Mutex
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
	"iron_mine":       {"food": 500}, // Costs Food (Labor)
	"police_station":  {"iron": 200},
	"diamond_mine":    {"iron": 1000},
	"hospital":        {"iron": 500, "diamond": 10},
	"platinum_mine":   {"iron": 2000, "diamond": 500},
	"defense_battery": {"iron": 2000, "diamond": 100},
	"rd_lab":          {"diamond": 200, "platinum": 50},
	"starfuel_refinery":{"iron": 10000, "diamond": 2000, "platinum": 1000},
}

// --- DB & Core Logic ---

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
	`
	if _, err := db.Exec(schema); err != nil { panic(err) }
	
	// Deterministic Identity & Location
	ServerUUID = fmt.Sprintf("sector-%d", time.Now().UnixNano())
	
	// Calculate unique Galactic Coordinates from the UUID hash
	hash := sha256.Sum256([]byte(ServerUUID))
	// Use first 3 bytes for X, Y, Z
	x := int(hash[0])*100 + int(hash[1])
	y := int(hash[2])*100 + int(hash[3])
	z := int(hash[4])*100 + int(hash[5])
	ServerLoc = []int{x, y, z}
}

func tickWorld() {
	stateLock.Lock()
	defer stateLock.Unlock()

	rows, _ := db.Query(`SELECT id, population, food, water, buildings_json, 
		iron, diamond, platinum, starfuel, 
		health, intelligence, crime, happiness FROM colonies`)
	
	type ColUpdate struct {
		ID int; Pop, Food, Water, Iron, Dia, Plat, Fuel int
		Health, Intel, Crime, Happy float64
	}
	var updates []ColUpdate

	for rows.Next() {
		var u ColUpdate
		var bJson string
		rows.Scan(&u.ID, &u.Pop, &u.Food, &u.Water, &bJson, 
			&u.Iron, &u.Dia, &u.Plat, &u.Fuel, 
			&u.Health, &u.Intel, &u.Crime, &u.Happy)
		
		var b map[string]int
		json.Unmarshal([]byte(bJson), &b)

		// Efficiency & Production
		eff := 1.0 + (float64(b["rd_lab"]) * (u.Intel / 200.0))

		u.Food += int(float64(b["farm"]*100) * eff)
		u.Water += int(float64(b["well"]*100) * eff)
		u.Iron += int(float64(b["iron_mine"]*10) * eff)
		u.Dia += int(float64(b["diamond_mine"]*5) * eff)
		u.Plat += int(float64(b["platinum_mine"]*2) * eff)
		u.Fuel += int(float64(b["starfuel_refinery"]*1) * eff)

		// Society stats
		targetCrime := (float64(u.Pop)/1000.0) - (float64(b["police_station"])*2.0)
		if targetCrime < 0 { targetCrime = 0 }
		u.Crime += (targetCrime - u.Crime) * 0.1 

		crowding := float64(u.Pop) / (5000.0 * math.Log10(float64(u.Pop)+10))
		healing := float64(b["hospital"]) * 1.5 * (1.0 + u.Intel/100.0)
		targetHealth := u.Health - crowding + healing
		if targetHealth > 100 { targetHealth = 100 }; if targetHealth < 0 { targetHealth = 0 }
		u.Health += (targetHealth - u.Health) * 0.1

		targetHappy := u.Health - (u.Crime * 5.0)
		if u.Food < u.Pop { targetHappy -= 20 }
		if u.Water < u.Pop { targetHappy -= 20 }
		if targetHappy > 100 { targetHappy = 100 }; if targetHappy < 0 { targetHappy = 0 }
		u.Happy += (targetHappy - u.Happy) * 0.1

		// Consumption (Reduced)
		consumption := int(float64(u.Pop) / 3.0)
		if consumption < 1 && u.Pop > 0 { consumption = 1 }

		u.Food -= consumption
		u.Water -= consumption

		if u.Food < 0 { u.Food = 0 }; if u.Water < 0 { u.Water = 0 }

		// Growth / Death
		logDampener := math.Log(float64(u.Pop) + 10.0)
		
		if u.Happy > 60 && u.Food > 0 {
			growth := float64(u.Pop) * 0.01 * (u.Happy/100.0)
			u.Pop += int(growth / logDampener)
		} else if u.Food == 0 || u.Water == 0 {
			death := float64(u.Pop) * 0.02
			u.Pop -= int(death / logDampener)
		}
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
}

// --- Handlers ---

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{Username, Password string}; json.NewDecoder(r.Body).Decode(&req)
	
	hash := sha256.Sum256([]byte(req.Password)); hashStr := hex.EncodeToString(hash[:])
	res, err := db.Exec("INSERT INTO users (username, password_hash) VALUES (?,?)", req.Username, hashStr)
	if err != nil { http.Error(w, "Username taken", 400); return }
	uid, _ := res.LastInsertId()

	// Starter Colony
	bJson, _ := json.Marshal(map[string]int{"farm":10, "well":10, "school":1, "iron_mine":5})
	
	resCol, _ := db.Exec(`INSERT INTO colonies (user_id, name, population, food, water, iron, buildings_json, loc_x, loc_y) 
		VALUES (?, ?, 1000, 20000, 20000, 1000, ?, ?, ?)`, 
		uid, req.Username+"'s Prime", string(bJson), rand.Intn(100), rand.Intn(100))
	
	// Create Starter Fleet (1 Probe)
	colID, _ := resCol.LastInsertId()
	db.Exec(`INSERT INTO fleets (user_id, origin_colony, status, fighters, probes, colonizers, destroyers) 
		VALUES (?, ?, 'IDLE', 0, 1, 0, 0)`, uid, colID)
	
	json.NewEncoder(w).Encode(map[string]int{"user_id": int(uid)})
}

func handleBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID int `json:"colony_id"`
		Structure string `json:"structure"`
		Amount int `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", 400); return
	}
	if req.Amount < 1 { req.Amount = 1 }

	unitCost := BuildingCosts[req.Structure]
	if unitCost == nil { http.Error(w, "Unknown Structure", 400); return }

	var c Colony; var bJson string
	err := db.QueryRow("SELECT buildings_json, iron, diamond, platinum, food FROM colonies WHERE id=?", req.ColonyID).Scan(&bJson, &c.Iron, &c.Diamond, &c.Platinum, &c.Food)
	if err != nil { http.Error(w, "Colony not found", 404); return }

	totalIron := unitCost["iron"] * req.Amount
	totalDia := unitCost["diamond"] * req.Amount
	totalPlat := unitCost["platinum"] * req.Amount
	totalFood := unitCost["food"] * req.Amount 

	if c.Iron < totalIron || c.Diamond < totalDia || c.Platinum < totalPlat || c.Food < totalFood {
		http.Error(w, "Insufficient Funds", 400); return
	}

	db.Exec("UPDATE colonies SET iron=iron-?, diamond=diamond-?, platinum=platinum-?, food=food-? WHERE id=?", totalIron, totalDia, totalPlat, totalFood, req.ColonyID)
	
	var b map[string]int; json.Unmarshal([]byte(bJson), &b)
	b[req.Structure] += req.Amount
	newJson, _ := json.Marshal(b)
	db.Exec("UPDATE colonies SET buildings_json=? WHERE id=?", string(newJson), req.ColonyID)
	
	w.Write([]byte(fmt.Sprintf("Built %d %s", req.Amount, req.Structure)))
}

// Updated handleShipBuild to accept Amount
func handleShipBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ColonyID int `json:"colony_id"`
		ShipType string `json:"ship_type"`
		Amount   int    `json:"amount"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Amount < 1 { req.Amount = 1 }

	cost := ShipCosts[req.ShipType]
	if cost == nil { http.Error(w, "Unknown Ship", 400); return }

	var c Colony
	err := db.QueryRow("SELECT iron, diamond, platinum, starfuel, food, water FROM colonies WHERE id=?", req.ColonyID).Scan(&c.Iron, &c.Diamond, &c.Platinum, &c.Starfuel, &c.Food, &c.Water)
	if err != nil { http.Error(w, "Colony not found", 404); return }

	// Calc Totals
	tIron := cost["iron"] * req.Amount
	tDia := cost["diamond"] * req.Amount
	tPlat := cost["platinum"] * req.Amount
	tFuel := cost["starfuel"] * req.Amount
	tFood := cost["food"] * req.Amount
	tWater := cost["water"] * req.Amount

	if c.Iron < tIron || c.Diamond < tDia || c.Platinum < tPlat || c.Starfuel < tFuel || c.Food < tFood || c.Water < tWater {
		http.Error(w, "Insufficient Resources", 400); return
	}

	db.Exec("UPDATE colonies SET iron=iron-?, diamond=diamond-?, platinum=platinum-?, starfuel=starfuel-?, food=food-?, water=water-? WHERE id=?",
		tIron, tDia, tPlat, tFuel, tFood, tWater, req.ColonyID)

	var fid int
	err = db.QueryRow("SELECT id FROM fleets WHERE origin_colony=? AND status='IDLE'", req.ColonyID).Scan(&fid)
	if err == sql.ErrNoRows {
		res, _ := db.Exec("INSERT INTO fleets (origin_colony, status, fighters, probes, colonizers, destroyers) VALUES (?, 'IDLE', 0,0,0,0)", req.ColonyID)
		id64, _ := res.LastInsertId(); fid = int(id64)
	}

	colName := fmt.Sprintf("%ss", req.ShipType)
	db.Exec(fmt.Sprintf("UPDATE fleets SET %s = %s + ? WHERE id=?", colName, colName), req.Amount, fid)
	
	w.Write([]byte(fmt.Sprintf("Built %d %s", req.Amount, req.ShipType)))
}

func handleFleetLaunch(w http.ResponseWriter, r *http.Request) {
	var req struct{FleetID int; TargetURL string}; json.NewDecoder(r.Body).Decode(&req)

	var f Fleet
	err := db.QueryRow("SELECT fighters, probes, colonizers, destroyers FROM fleets WHERE id=?", req.FleetID).Scan(&f.Fighters, &f.Probes, &f.Colonizers, &f.Destroyers)
	if err != nil { http.Error(w, "Fleet not found", 404); return }

	payload := map[string]interface{}{"origin_uuid": ServerUUID, "fleet": f}
	data, _ := json.Marshal(payload)
	resp, err := http.Post(req.TargetURL+"/federation/receive_fleet", "application/json", bytes.NewBuffer(data))
	
	if err != nil { http.Error(w, "Target Unreachable", 500); return }

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if f.Probes > 0 {
		if intel, ok := result["intel"]; ok {
			bits, _ := json.Marshal(intel)
			var report IntelReport
			json.Unmarshal(bits, &report)
			stateLock.Lock(); intelMap[report.ServerUUID] = report; stateLock.Unlock()
		}
	}
	db.Exec("DELETE FROM fleets WHERE id=?", req.FleetID)
	json.NewEncoder(w).Encode(result)
}

func handleReceiveFleet(w http.ResponseWriter, r *http.Request) {
	var req struct { OriginUUID string; Fleet Fleet }; json.NewDecoder(r.Body).Decode(&req)
	
	resp := map[string]interface{}{"message": "Fleet Detected"}

	if req.Fleet.Probes > 0 {
		rows, _ := db.Query("SELECT id, name, population, buildings_json FROM colonies")
		var cols []Colony
		for rows.Next() {
			var c Colony; var bJson string
			rows.Scan(&c.ID, &c.Name, &c.Population, &bJson)
			var b map[string]int; json.Unmarshal([]byte(bJson), &b)
			c.DefenseRating = b["defense_battery"] * 1000
			cols = append(cols, c)
		}
		
		var peers []string
		stateLock.Lock()
		for uuid := range intelMap { peers = append(peers, uuid) }
		stateLock.Unlock()

		resp["intel"] = IntelReport{
			ServerUUID: ServerUUID, 
			Location: ServerLoc,
			Colonies: cols,
			Peers: peers,
		}
		resp["scan_result"] = fmt.Sprintf("Sector Mapped. Coordinates: %v. Peers: %d", ServerLoc, len(peers))
	}

	if req.Fleet.Colonizers > 0 {
		bJson, _ := json.Marshal(map[string]int{"farm":5, "well":5})
		db.Exec(`INSERT INTO colonies (user_id, name, population, food, water, buildings_json) VALUES (0, 'Outpost of '+?, 500, 2000, 2000, ?)`, req.OriginUUID, string(bJson))
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
		} else {
			resp["combat_log"] = "No targets found."
		}
	}

	json.NewEncoder(w).Encode(resp)
}

func handleState(w http.ResponseWriter, r *http.Request) {
	uidStr := r.Header.Get("X-User-ID")
	var uid int; fmt.Sscanf(uidStr, "%d", &uid)

	type StateResp struct {
		ServerUUID string; ServerLoc []int
		Intel map[string]IntelReport
		MyColonies []Colony; MyFleets []Fleet
		Costs map[string]map[string]int // Send config to client
		ShipCosts map[string]map[string]int
	}
	resp := StateResp{
		ServerUUID: ServerUUID, 
		ServerLoc: ServerLoc, 
		Intel: intelMap,
		Costs: BuildingCosts,
		ShipCosts: ShipCosts,
	}

	rows, _ := db.Query(`SELECT id, name, buildings_json, population, food, water, 
		iron, diamond, platinum, starfuel, health, intelligence, crime, happiness 
		FROM colonies WHERE user_id=?`, uid)
	for rows.Next() {
		var c Colony; var bJson string
		rows.Scan(&c.ID, &c.Name, &bJson, &c.Population, &c.Food, &c.Water,
			&c.Iron, &c.Diamond, &c.Platinum, &c.Starfuel, 
			&c.Health, &c.Intelligence, &c.Crime, &c.Happiness)
		json.Unmarshal([]byte(bJson), &c.Buildings)
		resp.MyColonies = append(resp.MyColonies, c)
	}
	rows.Close()

	fRows, _ := db.Query("SELECT id, origin_colony, fighters, probes, colonizers, destroyers FROM fleets WHERE origin_colony IN (SELECT id FROM colonies WHERE user_id=?)", uid)
	for fRows.Next() {
		var f Fleet
		fRows.Scan(&f.ID, &f.OriginColony, &f.Fighters, &f.Probes, &f.Colonizers, &f.Destroyers)
		resp.MyFleets = append(resp.MyFleets, f)
	}

	json.NewEncoder(w).Encode(resp)
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

func handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct{Username, Password string}; json.NewDecoder(r.Body).Decode(&req)
	hash := sha256.Sum256([]byte(req.Password)); hashStr := hex.EncodeToString(hash[:])
	var id int; err := db.QueryRow("SELECT id FROM users WHERE username=? AND password_hash=?", req.Username, hashStr).Scan(&id)
	if err!=nil { http.Error(w,"Fail",401); return }
	json.NewEncoder(w).Encode(map[string]int{"user_id": id})
}

func main() {
	initDB()
	go func() { for range time.Tick(5 * time.Second) { tickWorld() } }()

	mux := http.NewServeMux()
	mux.HandleFunc("/login", handleLogin)
	mux.HandleFunc("/register", handleRegister)
	mux.HandleFunc("/state", handleState)
	mux.HandleFunc("/build", handleBuild)
	mux.HandleFunc("/build/ship", handleShipBuild)
	mux.HandleFunc("/fleet/launch", handleFleetLaunch)
	mux.HandleFunc("/federation/receive_fleet", handleReceiveFleet)

	fmt.Printf("World %s (Loc: %v) Listening on :8080\n", ServerUUID, ServerLoc)
	http.ListenAndServe(":8080", corsMiddleware(mux))
}
