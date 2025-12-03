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
		resp.Body.Close()
		
		InfoLog.Printf("Handshake response from %s: %s", seed, resp.Status)
	}
}

func main() {
	setupLogging()
	initConfig()
	initDB() 

	InfoLog.Println("OWNWORLD BOOT SEQUENCE (V3.1)")
	InfoLog.Printf("Mode: %v | Control: %v", Config.PeeringMode, Config.CommandControl)

	go processImmigration()
	go snapshotPeers()
	go bootstrapFederation()
	// go runGameLoop() // Defined in simulation.go

	mux := http.NewServeMux()
	
	// Federation
	mux.HandleFunc("/federation/handshake", handleHandshake)
	mux.HandleFunc("/federation/sync", handleSyncLedger)
	mux.HandleFunc("/federation/map", handleMap)
	mux.HandleFunc("/federation/transaction", handleFederationTransaction)

	// Client API
	mux.HandleFunc("/api/register", handleRegister)
	mux.HandleFunc("/api/deploy", handleDeploy)
	mux.HandleFunc("/api/build", handleBuild)
	mux.HandleFunc("/api/construct", handleConstruct) // V3.1: Shipyard Construction
	mux.HandleFunc("/api/bank/burn", handleBankBurn)
	mux.HandleFunc("/api/fleet/launch", handleFleetLaunch)
	mux.HandleFunc("/api/state", handleState)

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"uuid": ServerUUID, "tick": CurrentTick, "leader": LeaderUUID,
		})
	})

	handler := middlewareSecurity(mux)
	handler = middlewareCORS(handler)

	server := &http.Server{
		Addr:         ":8080",
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	
	InfoLog.Printf("Node %s Listening on :8080", ServerUUID)
	server.ListenAndServe()
}
