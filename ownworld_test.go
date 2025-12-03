package main

import (
	"bytes"
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TestLZ4RoundTrip verifies that data survives the compression/decompression cycle
// without corruption, utilizing the sync.Pool buffers.
func TestLZ4RoundTrip(t *testing.T) {
	original := []byte("The spice must flow. The spice must flow. The spice must flow.")
	
	// Compress
	compressed := compressLZ4(original)
	if len(compressed) == 0 {
		t.Fatal("Compressed data is empty")
	}

	// Decompress
	restored := decompressLZ4(compressed)
	
	if !bytes.Equal(original, restored) {
		t.Errorf("Mismatch.\nOriginal: %s\nRestored: %s", original, restored)
	}

	// Sanity check: LZ4 frame format usually has some overhead for small strings, 
	// but for larger data it should be smaller. We just check valid execution here.
	t.Logf("Original Len: %d, Compressed Len: %d", len(original), len(compressed))
}

// TestBLAKE3 verifies the hashing function returns the expected hex string length (64 chars for 32 bytes)
// and is deterministic.
func TestBLAKE3(t *testing.T) {
	input := []byte("genesis_block_seed")
	
	hash1 := hashBLAKE3(input)
	hash2 := hashBLAKE3(input)

	if hash1 != hash2 {
		t.Error("BLAKE3 is not deterministic")
	}

	if len(hash1) != 64 {
		t.Errorf("Expected hash length 64 (hex encoded 32 bytes), got %d", len(hash1))
	}
}

// TestEfficiency verifies the procedural generation logic produces values 
// strictly within the 0.1 to 2.5 range.
func TestEfficiency(t *testing.T) {
	min, max := 100.0, 0.0
	
	// Run 1000 simulations
	for i := 0; i < 1000; i++ {
		eff := GetEfficiency(i, "iron")
		
		if eff < 0.1 || eff > 2.5 {
			t.Errorf("Efficiency %f out of bounds [0.1, 2.5] for Planet %d", eff, i)
		}

		if eff < min { min = eff }
		if eff > max { max = eff }
	}

	t.Logf("Efficiency Range Observed: [%f, %f]", min, max)
}

// TestSchemaIntegrity creates an in-memory SQLite database and attempts to apply the schema.
// This ensures SQL syntax errors are caught before runtime.
func TestSchemaIntegrity(t *testing.T) {
	// 1. Setup In-Memory DB
	var err error
	// Overwrite the global 'db' variable for this test
	db, err = sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open memory db: %v", err)
	}
	defer db.Close()

	// 2. Run Schema Creation
	// We catch panics because createSchema() panics on error
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("createSchema panicked: %v", r)
		}
	}()
	
	createSchema()

	// 3. Verify Tables Exist
	tables := []string{"users", "solar_systems", "planets", "colonies", "fleets", "transaction_log"}
	for _, tbl := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&name)
		if err != nil {
			t.Errorf("Table '%s' was not created", tbl)
		}
	}

	// 4. Verify WAL Mode Pragma execution (Mock check)
	// Note: In :memory: databases, WAL mode might behave differently, 
	// but we check if the connection accepts the command.
	_, err = db.Exec("PRAGMA journal_mode=WAL;")
	if err != nil {
		t.Errorf("Failed to set WAL mode: %v", err)
	}
}

// TestLeaderElectionLogic verifies the sorting mechanism for consensus.
func TestLeaderElectionLogic(t *testing.T) {
	// Mock globals
	ServerUUID = "node-A"
	CurrentTick = 100
	
	// Setup peers map
	// Node B: Higher tick (Should win)
	// Node C: Same tick, Lower UUID (Should lose to B, but win against A if A had same tick)
	peerLock.Lock()
	peers = make(map[string]*Peer)
	peers["node-B"] = &Peer{UUID: "node-B", LastTick: 105}
	peers["node-C"] = &Peer{UUID: "node-C", LastTick: 100}
	peerLock.Unlock()

	recalculateLeader()

	if LeaderUUID != "node-B" {
		t.Errorf("Leader Election Failed. Expected node-B (Tick 105), Got %s", LeaderUUID)
	}

	// Test TDMA Phase Offset
	// With 3 nodes (A, B, C), sorted alphabetically:
	// A (Rank 0), B (Rank 1), C (Rank 2)
	// Total 3 nodes. 5000ms / 3 = 1666ms per slot.
	// Node A is Rank 0 -> Offset 0
	
	// Wait, RecalculateLeader logic sorts peers descending by score for LEADER,
	// but sorts allUUIDs alphabetically for TDMA.
	// allUUIDs: node-A, node-B, node-C.
	// node-A is index 0. Offset should be 0.
	
	expectedOffset := time.Duration(0)
	if PhaseOffset != expectedOffset {
		t.Errorf("TDMA Offset Failed. Expected %v, Got %v", expectedOffset, PhaseOffset)
	}
}
