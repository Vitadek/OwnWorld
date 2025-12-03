package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

func initConfig() {
	Config.CommandControl = true
	if os.Getenv("OWNWORLD_COMMAND_CONTROL") == "false" {
		Config.CommandControl = false
	}

	Config.PeeringMode = "promiscuous"
	if mode := os.Getenv("OWNWORLD_PEERING_MODE"); mode == "strict" {
		Config.PeeringMode = "strict"
	}
}

// Phase 3.2: Bootstrapping
func bootstrapFederation() {
	// 1. Get Seeds
	seeds := os.Getenv("SEED_NODES")
	if seeds == "" {
		InfoLog.Println("No SEED_NODES found. Starting as Lonely/Genesis Node.")
		return
	}

	// 2. Iterate and Attempt Handshake
	nodeList := strings.Split(seeds, ",")
	for _, seed := range nodeList {
		seed = strings.TrimSpace(seed)
		if seed == "" {
			continue
		}
		InfoLog.Printf("Attempting handshake with seed: %s", seed)

		// 3. Prepare Handshake Payload
		var myGenHash string
		err := db.QueryRow("SELECT value FROM system_meta WHERE key='genesis_hash'").Scan(&myGenHash)
		if err != nil {
			ErrorLog.Printf("Database Error reading Genesis Hash: %v", err)
			return
		}

		req := HandshakeRequest{
			UUID:        ServerUUID,
			GenesisHash: myGenHash,
			PublicKey:   hex.EncodeToString(PublicKey), // Using global PublicKey
			Address:     "http://localhost:8080",       // In production, use active discovery
		}

		payload, _ := json.Marshal(req)
		compressed := compressLZ4(payload)

		// 4. Send Request
		// We use a custom client with a short timeout to avoid hanging on bad seeds
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Post(seed+"/federation/handshake", "application/x-ownworld-fed", bytes.NewBuffer(compressed))
		if err != nil {
			ErrorLog.Printf("Seed %s unreachable: %v", seed, err)
			continue
		}
		defer resp.Body.Close()

		// 5. Handle Response
		if resp.StatusCode == http.StatusAccepted {
			InfoLog.Printf("Seed %s accepted handshake. Joined Federation.", seed)
			// Success! We don't need to try other seeds immediately, 
			// but could continue if we wanted a robust initial peer set.
			return
		} else if resp.StatusCode == http.StatusForbidden {
			// Phase 3.2 Safety: "If mismatch -> Panic/Wipe DB"
			// This indicates our Genesis Hash does not match the Seed's.
			ErrorLog.Fatalf("CRITICAL: Seed %s rejected us (Genesis Mismatch). Your world data is incompatible with this Federation.", seed)
		}
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
	go bootstrapFederation()
	go runGameLoop()

	mux := http.NewServeMux()

	// Federation
	mux.HandleFunc("/federation/handshake", handleHandshake)
	mux.HandleFunc("/federation/sync", handleSyncLedger)
	mux.HandleFunc("/federation/map", handleMap)
	mux.HandleFunc("/federation/transaction", handleFederationTransaction) // <--- New Route

	// Client API
	mux.HandleFunc("/api/register", handleRegister)
	mux.HandleFunc("/api/build", handleBuild)
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"uuid": ServerUUID, "tick": CurrentTick, "leader": LeaderUUID,
		})
	})

	server := &http.Server{
		Addr:         ":8080",
		Handler:      middlewareSecurity(mux),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	InfoLog.Printf("Node %s Listening on :8080", ServerUUID)
	if err := server.ListenAndServe(); err != nil {
		ErrorLog.Fatal(err)
	}
}
