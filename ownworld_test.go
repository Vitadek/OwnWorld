package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// setupTestEnv initializes an in-memory database and schema for isolated testing.
func setupTestEnv(t *testing.T) {
	var err error
	// Use :memory: to avoid touching the real database on disk
	db, err = sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}

	// Manually apply schema here to avoid "undefined: createSchema" build errors
	// if db.go is not picked up correctly by the test runner.
	schema := `
	CREATE TABLE IF NOT EXISTS system_meta (key TEXT PRIMARY KEY, value TEXT);
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		global_uuid TEXT UNIQUE,
		username TEXT, password_hash TEXT,
		credits INTEGER DEFAULT 0, is_local BOOLEAN DEFAULT 1,
		ed25519_pubkey TEXT
	);
	CREATE TABLE IF NOT EXISTS solar_systems (
		id TEXT PRIMARY KEY, x INTEGER, y INTEGER, z INTEGER,
		star_type TEXT, owner_uuid TEXT, tax_rate REAL DEFAULT 0.0, is_federated BOOLEAN DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS planets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, system_id TEXT, efficiency_seed TEXT, type TEXT,
		FOREIGN KEY(system_id) REFERENCES solar_systems(id)
	);
	CREATE TABLE IF NOT EXISTS colonies (
		id INTEGER PRIMARY KEY AUTOINCREMENT, system_id TEXT, owner_uuid TEXT, name TEXT,
		pop_laborers INTEGER DEFAULT 100, pop_specialists INTEGER DEFAULT 0, pop_elites INTEGER DEFAULT 0,
		food INTEGER DEFAULT 1000, water INTEGER DEFAULT 1000, iron INTEGER DEFAULT 0,
		carbon INTEGER DEFAULT 0, gold INTEGER DEFAULT 0, platinum INTEGER DEFAULT 0,
		uranium INTEGER DEFAULT 0, diamond INTEGER DEFAULT 0, vegetation INTEGER DEFAULT 0,
		oxygen INTEGER DEFAULT 1000, fuel INTEGER DEFAULT 0,
		stability_current REAL DEFAULT 100.0, stability_target REAL DEFAULT 100.0,
		martial_law BOOLEAN DEFAULT 0, buildings_json TEXT
	);
	CREATE TABLE IF NOT EXISTS fleets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, owner_uuid TEXT, status TEXT,
		origin_system TEXT, dest_system TEXT, departure_tick INTEGER, arrival_tick INTEGER,
		ark_ship INTEGER DEFAULT 0, fighters INTEGER DEFAULT 0, frigates INTEGER DEFAULT 0,
		haulers INTEGER DEFAULT 0, fuel INTEGER DEFAULT 0, start_coords TEXT, dest_coords TEXT
	);
	CREATE TABLE IF NOT EXISTS transaction_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT, tick INTEGER, action_type TEXT, payload_blob BLOB
	);
	CREATE TABLE IF NOT EXISTS daily_snapshots (
		day_id INTEGER PRIMARY KEY, state_blob BLOB, final_hash TEXT
	);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}

	// We can skip initIdentity for simple logic tests or mock it if needed
	// initIdentity() 
	ServerUUID = "test-server-uuid"
}

// Helper to make JSON requests
func executeRequest(handler http.HandlerFunc, method, path string, payload interface{}) *httptest.ResponseRecorder {
	var body []byte
	if payload != nil {
		body, _ = json.Marshal(payload)
	}
	req, _ := http.NewRequest(method, path, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// Test 1: Homestead Start (Registration)
func TestHomesteadStart(t *testing.T) {
	setupTestEnv(t)

	payload := map[string]string{
		"username": "CommanderShepard",
		"password": "securepassword123",
	}

	rr := executeRequest(handleRegister, "POST", "/api/register", payload)

	if rr.Code != 200 {
		t.Errorf("Registration failed. Code: %d, Body: %s", rr.Code, rr.Body.String())
	}

	// Verify User
	var count int
	db.QueryRow("SELECT count(*) FROM users WHERE username='CommanderShepard'").Scan(&count)
	if count != 1 {
		t.Errorf("User not created in DB")
	}

	// Verify Colony
	var colID int
	var pop int
	err := db.QueryRow("SELECT id, pop_laborers FROM colonies WHERE owner_uuid='CommanderShepard'").Scan(&colID, &pop)
	if err != nil {
		t.Errorf("Homestead colony not created: %v", err)
	}
	if pop != 1000 {
		t.Errorf("Wrong starting population. Expected 1000, got %d", pop)
	}

	// CRITICAL: Verify Starting Fleet is a SCOUT, not an Ark
	var arkCount, fighters int
	err = db.QueryRow("SELECT ark_ship, fighters FROM fleets WHERE owner_uuid='CommanderShepard'").Scan(&arkCount, &fighters)
	if err != nil {
		t.Errorf("Starting fleet not created")
	}
	if arkCount != 0 {
		t.Errorf("Start Error: Player given free Ark Ship! (Expected 0)")
	}
	if fighters != 1 {
		t.Errorf("Start Error: Player not given Scout (Fighter).")
	}
}

// Test 2: Infrastructure Gate (Building Shipyard & Constructing Ark)
func TestShipyardConstruction(t *testing.T) {
	setupTestEnv(t)

	db.Exec("INSERT INTO users (username, credits) VALUES ('BuilderBob', 1000)")
	db.Exec("INSERT INTO solar_systems (id, x, y, z) VALUES ('sys-1', 0, 0, 0)")
	
	res, _ := db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, iron, carbon, food, fuel, pop_laborers, buildings_json) 
		VALUES ('sys-1', 'BuilderBob', 'BobPrime', 10000, 5000, 10000, 1000, 500, '{}')`)
	colID, _ := res.LastInsertId()

	reqConstruct := map[string]interface{}{
		"colony_id": colID,
		"unit":      "ark_ship",
		"amount":    1,
	}
	rr := executeRequest(handleConstruct, "POST", "/api/construct", reqConstruct)
	if rr.Code != 400 {
		t.Errorf("Security Flaw: Allowed Ark construction without Shipyard! Code: %d", rr.Code)
	}

	reqBuild := map[string]interface{}{
		"colony_id": int(colID),
		"structure": "shipyard",
		"amount":    1,
	}
	rr = executeRequest(handleBuild, "POST", "/api/build", reqBuild)
	if rr.Code != 200 {
		t.Fatalf("Build Shipyard failed: %s", rr.Body.String())
	}

	rr = executeRequest(handleConstruct, "POST", "/api/construct", reqConstruct)
	if rr.Code != 200 {
		t.Errorf("Construction failed with valid resources and shipyard: %s", rr.Body.String())
	}

	var iron, food, fuel, pop int
	db.QueryRow("SELECT iron, food, fuel, pop_laborers FROM colonies WHERE id=?", colID).Scan(&iron, &food, &fuel, &pop)
	
	if iron != 4000 { t.Errorf("Resource Iron incorrect. Got %d, Expected 4000", iron) }
	if food != 5000 { t.Errorf("Resource Food incorrect. Got %d, Expected 5000", food) }
	if fuel != 500 { t.Errorf("Resource Fuel incorrect. Got %d, Expected 500", fuel) }
	if pop != 400 { t.Errorf("Crew deduction incorrect. Got %d, Expected 400", pop) }

	var arkCount int
	db.QueryRow("SELECT ark_ship FROM fleets WHERE owner_uuid='BuilderBob' AND status='ORBIT'").Scan(&arkCount)
	if arkCount != 1 {
		t.Errorf("Ark Fleet not found in DB.")
	}
}

// Test 3: Deployment Logic (Colonizing a new world)
func TestDeployment(t *testing.T) {
	setupTestEnv(t)

	db.Exec("INSERT INTO users (username) VALUES ('ExplorerAlice')")
	db.Exec("INSERT INTO solar_systems (id) VALUES ('sys-new')")
	
	res, _ := db.Exec(`INSERT INTO fleets (owner_uuid, status, origin_system, dest_system, ark_ship) 
		VALUES ('ExplorerAlice', 'ORBIT', 'sys-new', 'sys-new', 1)`)
	fleetID, _ := res.LastInsertId()

	payload := map[string]interface{}{
		"fleet_id": int(fleetID),
		"name":     "Alice New World",
	}
	rr := executeRequest(handleDeploy, "POST", "/api/deploy", payload)

	if rr.Code != 200 {
		t.Fatalf("Deployment failed: %s", rr.Body.String())
	}

	var count int
	db.QueryRow("SELECT count(*) FROM colonies WHERE system_id='sys-new' AND owner_uuid='ExplorerAlice'").Scan(&count)
	if count != 1 {
		t.Errorf("New colony record not found.")
	}

	var fleetCount int
	db.QueryRow("SELECT count(*) FROM fleets WHERE id=?", fleetID).Scan(&fleetCount)
	if fleetCount != 0 {
		t.Errorf("Logic Error: Ark Ship fleet was NOT deleted after deployment.")
	}
}

// Test 4: Occupancy Check (Anti-Griefing)
func TestDeploymentConflict(t *testing.T) {
	setupTestEnv(t)

	db.Exec("INSERT INTO solar_systems (id) VALUES ('sys-taken')")
	db.Exec("INSERT INTO users (username) VALUES ('Occupant')")
	db.Exec("INSERT INTO colonies (system_id, owner_uuid) VALUES ('sys-taken', 'Occupant')")

	db.Exec("INSERT INTO users (username) VALUES ('Invader')")
	res, _ := db.Exec(`INSERT INTO fleets (owner_uuid, status, origin_system, ark_ship) 
		VALUES ('Invader', 'ORBIT', 'sys-taken', 1)`)
	fleetID, _ := res.LastInsertId()

	payload := map[string]interface{}{
		"fleet_id": int(fleetID),
		"name":     "Invader Base",
	}
	rr := executeRequest(handleDeploy, "POST", "/api/deploy", payload)

	if rr.Code != 409 { 
		t.Errorf("Security Fail: Allowed colonization of occupied system! Code: %d", rr.Code)
	}
}
