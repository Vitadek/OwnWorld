package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"lukechampine.com/blake3"
	_ "github.com/mattn/go-sqlite3"
)

// --- Core Database & Identity ---
func initDB() {
	os.MkdirAll("./data", 0755)
	
	// 1. Enable WAL Mode (Non-blocking reads) + 5s Timeout
	var err error
	db, err = sql.Open("sqlite3", dbFile+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil { panic(err) }

	// Force WAL
	db.Exec("PRAGMA journal_mode=WAL;")

	// 2. Define V3 Schema (UUIDs, Economy, Event Sourcing)
	schema := `
	CREATE TABLE IF NOT EXISTS system_meta (key TEXT PRIMARY KEY, value TEXT);

	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		global_uuid TEXT UNIQUE,
		username TEXT, 
		password_hash TEXT, 
		credits INTEGER DEFAULT 0,
		is_local BOOLEAN DEFAULT 1,
		ed25519_pubkey TEXT
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
		id INTEGER PRIMARY KEY AUTOINCREMENT, 
		system_id TEXT, 
		owner_uuid TEXT, 
		name TEXT,
		
		pop_laborers INTEGER DEFAULT 100,
		pop_specialists INTEGER DEFAULT 0,
		pop_elites INTEGER DEFAULT 0,

		food INTEGER DEFAULT 1000, water INTEGER DEFAULT 1000,
		iron INTEGER DEFAULT 0, carbon INTEGER DEFAULT 0, gold INTEGER DEFAULT 0,
		platinum INTEGER DEFAULT 0, uranium INTEGER DEFAULT 0, diamond INTEGER DEFAULT 0,
		vegetation INTEGER DEFAULT 0, oxygen INTEGER DEFAULT 1000,
		fuel INTEGER DEFAULT 0,

		stability_current REAL DEFAULT 100.0,
		stability_target REAL DEFAULT 100.0,
		martial_law BOOLEAN DEFAULT 0,
		buildings_json TEXT
	);

	CREATE TABLE IF NOT EXISTS fleets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, 
		owner_uuid TEXT, 
		status TEXT, 
		origin_system TEXT,
		dest_system TEXT,
		departure_tick INTEGER,
		arrival_tick INTEGER,
		ark_ship INTEGER DEFAULT 0,
		fighters INTEGER DEFAULT 0, 
		frigates INTEGER DEFAULT 0, 
		haulers INTEGER DEFAULT 0, 
		fuel INTEGER DEFAULT 0,
		start_coords TEXT, dest_coords TEXT
	);

	CREATE TABLE IF NOT EXISTS transaction_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT, 
		tick INTEGER, 
		action_type TEXT, 
		payload_blob BLOB
	);
	CREATE INDEX IF NOT EXISTS idx_tx_tick ON transaction_log(tick);

	CREATE TABLE IF NOT EXISTS daily_snapshots (
		day_id INTEGER PRIMARY KEY, 
		state_blob BLOB,
		final_hash TEXT
	);
	`
	
	if _, err := db.Exec(schema); err != nil { panic(err) }

	initIdentity()

	// Resume Ledger State
	var lastTick int
	var lastHash string
	err = db.QueryRow("SELECT tick, final_hash FROM daily_snapshots ORDER BY day_id DESC LIMIT 1").Scan(&lastTick, &lastHash)
	if err == nil {
		// CurrentTick = lastTick 
	}
}

func initIdentity() {
	var uuid string
	err := db.QueryRow("SELECT value FROM system_meta WHERE key='server_uuid'").Scan(&uuid)
	
	if err == sql.ErrNoRows {
		fmt.Println("FIRST BOOT: Generating Galaxy Identity...")
		
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		privHex := hex.EncodeToString(priv)
		pubHex := hex.EncodeToString(pub)

		// Fix: Use crypto/rand safely
		rndBytes := make([]byte, 8)
		rand.Read(rndBytes)
		genesisData := fmt.Sprintf("GENESIS-%d-%x", time.Now().UnixNano(), rndBytes)
		
		hash := blake3.Sum256([]byte(genesisData))
		uuid = hex.EncodeToString(hash[:])
		
		tx, _ := db.Begin()
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('server_uuid', ?)", uuid)
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('genesis_hash', ?)", uuid)
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('priv_key', ?)", privHex)
		tx.Exec("INSERT INTO system_meta (key, value) VALUES ('pub_key', ?)", pubHex)
		tx.Commit()
		
		PrivateKey = priv
		PublicKey = pub
		fmt.Printf("Identity Created: %s\n", uuid)
	} else {
		var privHex, pubHex string
		db.QueryRow("SELECT value FROM system_meta WHERE key='priv_key'").Scan(&privHex)
		db.QueryRow("SELECT value FROM system_meta WHERE key='pub_key'").Scan(&pubHex)
		
		privBytes, _ := hex.DecodeString(privHex)
		pubBytes, _ := hex.DecodeString(pubHex)
		
		PrivateKey = ed25519.PrivateKey(privBytes)
		PublicKey = ed25519.PublicKey(pubBytes)
	}
	
	ServerUUID = uuid
	LeaderUUID = ServerUUID
}
