package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mrand "math/rand" // Now used in bootstrapFederation
	"net/http"
	"os"
	"strings"
	"time"
)

func initConfig() {
	// Default to true unless explicitly disabled
	Config.CommandControl = true
	if os.Getenv("OWNWORLD_COMMAND_CONTROL") == "false" {
		Config.CommandControl = false 
	}

	// Default to promiscuous unless strict is requested
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
			Location:    ServerLoc,
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
		
		var respData HandshakeResponse
		json.NewDecoder(resp.Body).Decode(&respData)
		resp.Body.Close()
		
		if respData.Status == "Queued" || respData.Status == "Accepted" {
			InfoLog.Printf("✅ Connected to Galaxy via Seed %s", seed)
			
			if len(respData.Location) == 3 {
				if ServerLoc[0] == 0 && ServerLoc[1] == 0 && ServerLoc[2] == 0 {
					// USE mrand HERE to prevent stacking
					mrand.Seed(time.Now().UnixNano())
					jitterX := mrand.Intn(10) - 5
					jitterY := mrand.Intn(10) - 5
					jitterZ := mrand.Intn(10) - 5

					ServerLoc = []int{
						respData.Location[0] + jitterX,
						respData.Location[1] + jitterY,
						respData.Location[2] + jitterZ,
					}
					
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

	// Start Background Services
	go processImmigration()
	go startHeartbeatLoop()
	go bootstrapFederation()
	go runGameLoop()

	mux := http.NewServeMux()
	
	// Federation Endpoints
	mux.HandleFunc("/federation/handshake", handleHandshake)
	mux.HandleFunc("/federation/sync", handleSyncLedger)
	mux.HandleFunc("/federation/map", handleMap)
	mux.HandleFunc("/federation/transaction", handleFederationTransaction)
	mux.HandleFunc("/federation/heartbeat", handleHeartbeat)

	// Client API Endpoints
	mux.HandleFunc("/api/register", handleRegister)
	mux.HandleFunc("/api/deploy", handleDeploy)
	mux.HandleFunc("/api/build", handleBuild)
	mux.HandleFunc("/api/construct", handleConstruct)
	mux.HandleFunc("/api/bank/burn", handleBankBurn)
	mux.HandleFunc("/api/fleet/launch", handleFleetLaunch)
	mux.HandleFunc("/api/state", handleState)

	// Public Status Check
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"uuid": ServerUUID, "tick": CurrentTick, "leader": LeaderUUID,
			"location": ServerLoc,
		})
	})

	// Wrap Middleware
	handler := middlewareSecurity(mux)
	handler = middlewareCORS(handler)

	// Secure Server Config
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
