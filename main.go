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

func bootstrapFederation() {
	seeds := os.Getenv("SEED_NODES")
	if seeds == "" {
		InfoLog.Println("No SEED_NODES found. Starting as Lonely/Genesis Node.")
		return
	}

	nodeList := strings.Split(seeds, ",")
	for _, seed := range nodeList {
		seed = strings.TrimSpace(seed)
		if seed == "" { continue }
		
		var myGenHash string
		err := db.QueryRow("SELECT value FROM system_meta WHERE key='genesis_hash'").Scan(&myGenHash)
		if err != nil { continue }

		req := HandshakeRequest{
			UUID:        ServerUUID,
			GenesisHash: myGenHash,
			PublicKey:   hex.EncodeToString(PublicKey),
			Address:     "http://localhost:8080",
			Location:    ServerLoc, // Send my location (or 0,0,0 if new)
		}
		payload, _ := json.Marshal(req)
		compressed := compressLZ4(payload)

		targetURL := seed + "/federation/handshake"
		if !strings.HasPrefix(seed, "http") {
			targetURL = "http://" + seed + "/federation/handshake"
		}

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Post(targetURL, "application/x-ownworld-fed", bytes.NewBuffer(compressed))
		if err != nil {
			ErrorLog.Printf("Seed %s unreachable: %v", seed, err)
			continue
		}
		
		// Read Response to get Seed's Location
		var respData HandshakeResponse
		json.NewDecoder(resp.Body).Decode(&respData)
		resp.Body.Close()
		
		if respData.Status == "Queued" || respData.Status == "Accepted" {
			InfoLog.Printf("✅ Connected to Galaxy via Seed %s", seed)
			
			// UPDATE LOCAL COORDS based on Seed
			// This ensures we spawn in the "Cluster" not in the Void
			if len(respData.Location) == 3 {
				// We don't override ServerLoc here immediately unless we are brand new (0,0,0)
				// If we are new:
				if ServerLoc[0] == 0 && ServerLoc[1] == 0 && ServerLoc[2] == 0 {
					ServerLoc = respData.Location
					// In reality, we'd add a small random offset here for the Server itself,
					// but for now let's just adopt the cluster center.
					// Persist:
					db.Exec("INSERT OR REPLACE INTO system_meta (key, value) VALUES ('loc_x', ?)", fmt.Sprint(ServerLoc[0]))
					db.Exec("INSERT OR REPLACE INTO system_meta (key, value) VALUES ('loc_y', ?)", fmt.Sprint(ServerLoc[1]))
					db.Exec("INSERT OR REPLACE INTO system_meta (key, value) VALUES ('loc_z', ?)", fmt.Sprint(ServerLoc[2]))
					InfoLog.Printf("📍 Server Cluster Location Set: %v", ServerLoc)
				}
			}
			break
		}
	}
}

func main() {
	setupLogging()
	initConfig()
	initDB() 

	InfoLog.Println("OWNWORLD BOOT SEQUENCE (V3.1)")
	InfoLog.Printf("Mode: %v | Control: %v", Config.PeeringMode, Config.CommandControl)

	go processImmigration()
	go startHeartbeatLoop() // Starts the heartbeat broadcaster
	go bootstrapFederation()
	go runGameLoop()

	mux := http.NewServeMux()
	
	// Federation
	mux.HandleFunc("/federation/handshake", handleHandshake)
	mux.HandleFunc("/federation/sync", handleSyncLedger)
	mux.HandleFunc("/federation/map", handleMap)
	mux.HandleFunc("/federation/transaction", handleFederationTransaction)
	mux.HandleFunc("/federation/heartbeat", handleHeartbeat) // Needs to be wired here

	// Client API
	mux.HandleFunc("/api/register", handleRegister)
	mux.HandleFunc("/api/deploy", handleDeploy)
	mux.HandleFunc("/api/build", handleBuild)
	mux.HandleFunc("/api/construct", handleConstruct)
	mux.HandleFunc("/api/bank/burn", handleBankBurn)
	mux.HandleFunc("/api/fleet/launch", handleFleetLaunch)
	mux.HandleFunc("/api/state", handleState)

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"uuid": ServerUUID, "tick": CurrentTick, "leader": LeaderUUID,
			"location": ServerLoc,
		})
	})

	handler := middlewareSecurity(mux)
	handler = middlewareCORS(handler)

	server := &http.Server{
		Addr:         ":8080",
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	
	InfoLog.Printf("Node %s Listening on :8080", ServerUUID)
	if err := server.ListenAndServe(); err != nil {
		ErrorLog.Fatal(err)
	}
}
