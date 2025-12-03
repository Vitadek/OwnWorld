package main

import (
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
	go runGameLoop()

	mux := http.NewServeMux()

	// Federation
	mux.HandleFunc("/federation/handshake", handleHandshake)
	mux.HandleFunc("/federation/sync", handleSyncLedger)
	mux.HandleFunc("/federation/map", handleMap)

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
