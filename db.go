package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func initDB() {
	if err := os.MkdirAll("./data", 0755); err != nil {
		panic(err)
	}
	dsn := dbFile + "?_journal_mode=WAL&_busy_timeout=5000"
	var err error
	db, err = sql.Open("sqlite3", dsn)
	if err != nil { panic(err) }
	
	db.Exec("PRAGMA journal_mode=WAL;")

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
		star_type TEXT, owner_uuid TEXT, tax_rate REAL DEFAULT 0.0,
		is_federated BOOLEAN DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS planets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, system_id TEXT, efficiency_seed TEXT, type TEXT,
		FOREIGN KEY(system_id) REFERENCES solar_systems(id)
	);
	CREATE TABLE IF NOT EXISTS colonies (
		id INTEGER PRIMARY KEY AUTOINCREMENT, system_id TEXT, owner_uuid TEXT, name TEXT, buildings_json TEXT,
		iron INTEGER DEFAULT 0, carbon INTEGER DEFAULT 0, gold INTEGER DEFAULT 0,
		uranium INTEGER DEFAULT 0, platinum INTEGER DEFAULT 0, diamond INTEGER DEFAULT 0,
		food INTEGER DEFAULT 0, water INTEGER DEFAULT 0, oxygen INTEGER DEFAULT 0, vegetation INTEGER DEFAULT 0,
		fuel INTEGER DEFAULT 0, -- V3.1: Added Fuel for Ship Construction
		pop_laborers INTEGER DEFAULT 0, pop_specialists INTEGER DEFAULT 0, pop_elites INTEGER DEFAULT 0,
		stability_current REAL DEFAULT 100.0, stability_target REAL DEFAULT 100.0, 
		crime_rate REAL DEFAULT 0.0, martial_law BOOLEAN DEFAULT 0
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

	var lastTick int
	var lastHash string
	err := db.QueryRow("SELECT tick, final_hash FROM ledger ORDER BY tick DESC LIMIT 1").Scan(&lastTick, &lastHash)
	if err == nil {
		CurrentTick = lastTick
		PreviousHash = lastHash
	}
}

func initIdentity() {
	var uuid string
	err := db.QueryRow("SELECT value FROM system_meta WHERE key='server_uuid'").Scan(&uuid)
	
	if err == sql.ErrNoRows {
		InfoLog.Println("First Boot Detected. Generating Identity...")
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		randBytes := make([]byte, 16)
		rand.Read(randBytes)
		
		genState := GenesisState{Seed: hex.EncodeToString(randBytes), Timestamp: time.Now().Unix(), PubKey: hex.EncodeToString(pub)}
		genJSON, _ := json.Marshal(genState)
		
		ServerUUID = hashBLAKE3(genJSON)
		
		tx, _ := db.Begin()
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('server_uuid', ?)", ServerUUID)
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('genesis_hash', ?)", hashBLAKE3(genJSON))
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('public_key', ?)", hex.EncodeToString(pub))
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('private_key', ?)", hex.EncodeToString(priv))
		tx.Commit()
		
		PrivateKey = priv
		PublicKey = pub
		LeaderUUID = ServerUUID
	} else {
		ServerUUID = uuid
		var privStr, pubStr string
		db.QueryRow("SELECT value FROM system_meta WHERE key='private_key'").Scan(&privStr)
		db.QueryRow("SELECT value FROM system_meta WHERE key='public_key'").Scan(&pubStr)
		privBytes, _ := hex.DecodeString(privStr)
		pubBytes, _ := hex.DecodeString(pubStr)
		PrivateKey = ed25519.PrivateKey(privBytes)
		PublicKey = ed25519.PublicKey(pubBytes)
		LeaderUUID = ServerUUID
	}
}
