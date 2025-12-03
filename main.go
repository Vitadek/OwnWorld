package main

import (
	"fmt"
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

	fedKey := os.Getenv("FEDERATION_KEY")
	client := &http.Client{Timeout: 10 * time.Second} // Increased timeout for sync

	nodeList := strings.Split(seeds, ",")
	for _, seed := range nodeList {
		seed = strings.TrimSpace(seed)
		if seed == "" { continue }

		// --- STEP 1: Handshake (Authentication) ---
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

		hsReq, _ := http.NewRequest("POST", targetURL, bytes.NewBuffer(compressed))
		hsReq.Header.Set("Content-Type", "application/x-ownworld-fed")
		hsReq.Header.Set("X-Fed-Key", fedKey)

		resp, err := client.Do(hsReq)
		if err != nil {
			ErrorLog.Printf("Seed %s unreachable: %v", seed, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
			continue
		}

		InfoLog.Printf("Handshake accepted by %s. Starting Sync...", seed)

		// --- STEP 2: Paginated Sync Loop ---

		// Determine where to start (Resume capability)
		var localMaxDay int
		err = db.QueryRow("SELECT COALESCE(MAX(day_id), 0) FROM daily_snapshots").Scan(&localMaxDay)
		if err != nil { localMaxDay = 0 }

		batchSize := 50
		totalSynced := 0

		for {
			syncURL := fmt.Sprintf("%s/federation/sync?since_day=%d&limit=%d", seed, "/federation/sync", localMaxDay, batchSize)
			if !strings.HasPrefix(seed, "http") {
				// Fix URL construction if seed lacks protocol
				syncURL = fmt.Sprintf("http://%s/federation/sync?since_day=%d&limit=%d", seed, localMaxDay, batchSize)
			}

			syncReq, _ := http.NewRequest("GET", syncURL, nil)
			syncReq.Header.Set("X-Fed-Key", fedKey)

			respSync, err := client.Do(syncReq)
			if err != nil {
				ErrorLog.Printf("Sync chunk failed: %v", err)
				break
			}

			if respSync.StatusCode != 200 {
				respSync.Body.Close()
				break
			}

			// Define structure strictly
			type SnapshotItem struct {
				DayID     int    `json:"day_id"`
				Blob      []byte `json:"blob"`
				FinalHash string `json:"hash"`
			}
			var batch []SnapshotItem

			if err := json.NewDecoder(respSync.Body).Decode(&batch); err != nil {
				respSync.Body.Close()
				ErrorLog.Printf("Bad Sync Packet: %v", err)
				break
			}
			respSync.Body.Close()

			if len(batch) == 0 {
				// No more data from server
				break
			}

			// Commit Batch to DB
			tx, _ := db.Begin()
			for _, h := range batch {
				tx.Exec("INSERT OR REPLACE INTO daily_snapshots (day_id, state_blob, final_hash) VALUES (?, ?, ?)",
					h.DayID, h.Blob, h.FinalHash)

				PreviousHash = h.FinalHash
				CurrentTick = h.DayID * 100
				localMaxDay = h.DayID // Update cursor for next loop
			}
			tx.Commit()

			totalSynced += len(batch)
			InfoLog.Printf("Synced chunk of %d days (Total: %d). Head is now Day %d.", len(batch), totalSynced, localMaxDay)

			// Optimization: If we received fewer items than requested, we are at the end.
			if len(batch) < batchSize {
				break
			}
			// Otherwise, loop again to get the next batch...
		}

		if totalSynced > 0 {
			InfoLog.Printf("Sync Complete. Universe valid up to Tick %d", CurrentTick)
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
	go snapshotPeers()
	go bootstrapFederation()
	go runGameLoop()

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
	mux.HandleFunc("/api/construct", handleConstruct)
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

	// Secure Server Config (Slow Loris Protection)
	server := &http.Server{
		Addr:         ":8080",
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	
	InfoLog.Printf("Node %s Listening on :8080", ServerUUID)
	server.ListenAndServe()
}
