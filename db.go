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
	_ "modernc.org/sqlite" 
)

// --- Core Database & Identity ---
func initDB() {
	os.MkdirAll("./data", 0755)

	// 1. Enable WAL Mode (Non-blocking reads) + 5s Timeout
	var err error
	// Note: Using "sqlite" driver name for modernc.org/sqlite, but passing the flags
	db, err = sql.Open("sqlite", dbFile+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil { panic(err) }

	// Force WAL execution if the connection string didn't trigger it immediately
	db.Exec("PRAGMA journal_mode=WAL;")

	// 2. The V3 Schema
	schema := `
	-- Identity & Config (Excluded from Game State Hash)
	CREATE TABLE IF NOT EXISTS system_meta (key TEXT PRIMARY KEY, value TEXT);

	-- Users (Global Identity)
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		global_uuid TEXT UNIQUE,          -- BLAKE3 Hash of PubKey
		username TEXT, 
		password_hash TEXT, 
		credits INTEGER DEFAULT 0,        -- Global Currency
		is_local BOOLEAN DEFAULT 1,       -- 1=Local Player, 0=Visitor
		ed25519_pubkey TEXT,              -- For verification
		ed25519_priv_enc TEXT             -- Encrypted Private Key (Optional)
	);

	-- Solar Systems (The Map)
	CREATE TABLE IF NOT EXISTS solar_systems (
		id TEXT PRIMARY KEY,              -- UUID
		x INTEGER, y INTEGER, z INTEGER,  -- Cluster Coordinates
		star_type TEXT, 
		owner_uuid TEXT, 
		tax_rate REAL DEFAULT 0.0,
		is_federated BOOLEAN DEFAULT 0
	);

	-- Planets (Details)
	CREATE TABLE IF NOT EXISTS planets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, system_id TEXT, efficiency_seed TEXT, type TEXT,
		FOREIGN KEY(system_id) REFERENCES solar_systems(id)
	);

	-- Colonies (The Economy)
	CREATE TABLE IF NOT EXISTS colonies (
		id INTEGER PRIMARY KEY AUTOINCREMENT, 
		system_id TEXT,
		owner_uuid TEXT,
		name TEXT,
		
		-- Population Classes
		pop_laborers INTEGER DEFAULT 100,
		pop_specialists INTEGER DEFAULT 0,
		pop_elites INTEGER DEFAULT 0,

		-- Resources (Tier 1 & 2)
		food INTEGER DEFAULT 1000, water INTEGER DEFAULT 1000,
		iron INTEGER DEFAULT 0, carbon INTEGER DEFAULT 0, gold INTEGER DEFAULT 0,
		platinum INTEGER DEFAULT 0, uranium INTEGER DEFAULT 0, diamond INTEGER DEFAULT 0,
		vegetation INTEGER DEFAULT 0, oxygen INTEGER DEFAULT 1000,

		-- Stability
		stability_current REAL DEFAULT 100.0,
		stability_target REAL DEFAULT 100.0,
		martial_law BOOLEAN DEFAULT 0,
		buildings_json TEXT,
		fuel INTEGER DEFAULT 0
	);

	-- Fleets (The Military/Logistics)
	CREATE TABLE IF NOT EXISTS fleets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, 
		owner_uuid TEXT, 
		status TEXT,             -- ORBIT, TRANSIT
		
		-- Navigation
		origin_system TEXT,
		dest_system TEXT,
		departure_tick INTEGER,
		arrival_tick INTEGER,
		start_coords TEXT,       -- JSON [x,y,z]
		dest_coords TEXT,        -- JSON [x,y,z]

		-- Composition
		ark_ship INTEGER DEFAULT 0, -- The Expansion Unit
		fighters INTEGER DEFAULT 0, 
		frigates INTEGER DEFAULT 0, 
		haulers INTEGER DEFAULT 0, 
		fuel INTEGER DEFAULT 0
	);

	-- Immutable Logs (Event Sourcing)
	CREATE TABLE IF NOT EXISTS transaction_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT, 
		tick INTEGER, 
		action_type TEXT, 
		payload_blob BLOB
	);
	
	CREATE TABLE IF NOT EXISTS daily_snapshots (
		day_id INTEGER PRIMARY KEY, 
		state_blob BLOB,
		final_hash TEXT
	);
	`
	
	if _, err := db.Exec(schema); err != nil { panic(err) }

	initIdentity()
}

func initIdentity() {
	var uuid string
	err := db.QueryRow("SELECT value FROM system_meta WHERE key='server_uuid'").Scan(&uuid)
	
	if err == sql.ErrNoRows {
		fmt.Println("FIRST BOOT: Generating Galaxy Identity...")
		
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		privHex := hex.EncodeToString(priv)
		pubHex := hex.EncodeToString(pub)

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
