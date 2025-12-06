package main

import (
	"bytes"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mrand "math/rand"
	"net/http"
	"os"
	"strings"
	"time"
)

func initConfig() {
    // Default to FALSE for safety (Requires OWNWORLD_COMMAND_CONTROL=true env var to enable registration)
	Config.CommandControl = false 
	if os.Getenv("OWNWORLD_COMMAND_CONTROL") == "true" {
		Config.CommandControl = true
	}

	Config.PeeringMode = "promiscuous"
	if mode := os.Getenv("OWNWORLD_PEERING_MODE"); mode == "strict" {
		Config.PeeringMode = "strict"
	}
}

// FIX F: Bootstrapping - Fetch Genesis BEFORE DB Init
func fetchGenesisFromSeed(seeds string) string {
	nodeList := strings.Split(seeds, ",")
	for _, seed := range nodeList {
		seed = strings.TrimSpace(seed)
		if seed == "" {
			continue
		}

		statusURL := seed + "/api/status"
		if !strings.HasPrefix(seed, "http") {
			statusURL = "http://" + seed + "/api/status"
		}

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(statusURL)
		if err == nil && resp.StatusCode == 200 {
			var status struct {
				UUID    string `json:"uuid"`
				Genesis string `json:"genesis"`
			}
			json.NewDecoder(resp.Body).Decode(&status)
			resp.Body.Close()

			if status.Genesis != "" {
				InfoLog.Printf("🌍 Found Universe Genesis via Seed: %s", status.Genesis)
				return status.Genesis
			}
		}
	}
	return ""
}

func bootstrapFederation() {
	seeds := os.Getenv("SEED_NODES")
	if seeds == "" {
		InfoLog.Println("No SEED_NODES found. Starting as Lonely/Genesis Node.")
		// Note: We handled ServerLoc safety in main(), so we don't strictly need it here,
		// but it's safe to leave the logic flow as is.
		return
	}

	nodeList := strings.Split(seeds, ",")
	for _, seed := range nodeList {
		seed = strings.TrimSpace(seed)
		if seed == "" {
			continue
		}

		var myGenHash string
		err := db.QueryRow("SELECT value FROM system_meta WHERE key='genesis_hash'").Scan(&myGenHash)
		if err != nil {
			continue
		}

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
	var b [8]byte
	crand.Read(b[:])
	mrand.Seed(int64(binary.LittleEndian.Uint64(b[:])))

	setupLogging()
	initConfig()

	TargetGenesisHash = fetchGenesisFromSeed(os.Getenv("SEED_NODES"))

	initDB()

	// --- RACE CONDITION FIX START ---
	// We explicitly initialize ServerLoc here, synchronously.
	// This ensures that even if bootstrapFederation() runs slowly in the background,
	// HTTP handlers (like /register) will never see a nil ServerLoc.
	if ServerLoc == nil {
		ServerLoc = []int{0, 0, 0}
	}
	// --- RACE CONDITION FIX END ---

	InfoLog.Println("OWNWORLD BOOT SEQUENCE (V3.1)")
	InfoLog.Printf("Mode: %v | Control: %v", Config.PeeringMode, Config.CommandControl)

	go processImmigration()
	go startHeartbeatLoop()
	go bootstrapFederation()
	go runGameLoop()

	mux := http.NewServeMux()

	mux.HandleFunc("/federation/handshake", handleHandshake)
	mux.HandleFunc("/federation/sync", handleSyncLedger)
	mux.HandleFunc("/federation/map", handleMap)
	mux.HandleFunc("/federation/transaction", handleFederationTransaction)
	mux.HandleFunc("/federation/heartbeat", handleHeartbeat)
	mux.HandleFunc("/federation/reputation", handleReputationQuery)

    // User endpoints (Should be gated in future if needed, but logic remains same)
	mux.HandleFunc("/api/register", func(w http.ResponseWriter, r *http.Request) {
        if !Config.CommandControl {
            http.Error(w, "Registration Disabled on this Node", 403)
            return
        }
        handleRegister(w, r)
    })
	mux.HandleFunc("/api/deploy", handleDeploy)
	mux.HandleFunc("/api/build", handleBuild)
	mux.HandleFunc("/api/construct", handleConstruct)
	mux.HandleFunc("/api/bank/burn", handleBankBurn)
	mux.HandleFunc("/api/fleet/launch", handleFleetLaunch)
	mux.HandleFunc("/api/state", handleState)
	mux.HandleFunc("/api/scan", handleScan)
	mux.HandleFunc("/api/fleet/transfer", handleCargoTransfer)
	mux.HandleFunc("/api/colony/policy", handleSetPolicy)
    
    // Fix 4: Alliance Endpoint
    mux.HandleFunc("/api/federation/ally", handleAlly)

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"uuid": ServerUUID, "tick": CurrentTick, "leader": LeaderUUID,
			"location": ServerLoc, "genesis": GenesisHash,
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
