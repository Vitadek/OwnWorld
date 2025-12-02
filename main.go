package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"

	"./pkg/core"
	"./pkg/game"
	_ "github.com/mattn/go-sqlite3"
)

var (
	db *sql.DB
	ServerUUID string
	GenesisHash string
)

func main() {
	// 1. Initialize DB (WAL Mode)
	var err error
	db, err = sql.Open("sqlite3", "./data/ownworld.db?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil { panic(err) }
	
	// 2. Initialize Identity (Genesis Bind)
	// (Logic from previous discussion goes here: Hash(Genesis) -> UUID)
	
	// 3. Start Game Loop (TDMA Staggering)
	go func() {
		for {
			// Offset logic from Consensus module
			// time.Sleep(PhaseOffset) 
			tickWorld()
			time.Sleep(5 * time.Second)
		}
	}()

	// 4. HTTP Server (Dual Mode + Timeouts)
	mux := http.NewServeMux()
	mux.HandleFunc("/bank/burn", handleBankBurn)
	mux.HandleFunc("/register", handleArkRegister) // The new "Ark" start
	
	server := &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	fmt.Println("🌌 Galaxy Node Online.")
	server.ListenAndServe()
}

// --- Specific Handlers for the New Mechanics ---

// The "Ark" Start
func handleArkRegister(w http.ResponseWriter, r *http.Request) {
	// ... Create User Logic ...
	uid := 1 // Mock ID
	
	// Give 1 Ark Ship (No Colony)
	// Fleet ID 0 usually reserved for 'In Transit' or 'Orbit'
	db.Exec(`INSERT INTO fleets (user_id, status, ark_ship, fuel) VALUES (?, 'ORBIT', 1, 2000)`, uid)
	
	json.NewEncoder(w).Encode(map[string]string{"msg": "Welcome. Ark Ship Deployed in Orbit."})
}

// The Scarcity Bank
func handleBankBurn(w http.ResponseWriter, r *http.Request) {
	var req struct { UserID, ColonyID int; Resource string; Amount int }
	json.NewDecoder(r.Body).Decode(&req)

	// Calculate Value
	payout := game.CalculateBurnPayout(req.Resource, req.ColonyID, req.Amount, ServerUUID)

	// Transaction (Atomic)
	tx, _ := db.Begin()
	// 1. Remove Resource
	res, _ := tx.Exec(fmt.Sprintf("UPDATE colonies SET %s = %s - ? WHERE id=?", req.Resource, req.Resource), req.Amount, req.ColonyID)
	if n, _ := res.RowsAffected(); n == 0 {
		tx.Rollback(); http.Error(w, "Not enough minerals", 400); return
	}
	// 2. Add Credits
	tx.Exec("UPDATE users SET credits = credits + ? WHERE id=?", payout, req.UserID)
	tx.Commit()

	json.NewEncoder(w).Encode(map[string]int{"credits_earned": payout})
}

func tickWorld() {
	// ... Load State ...
	
	// Apply Corruption (Evening Factor)
	// eff := game.CalculateCorruption(UserColonyCount)
	// mining_output = base * eff
	
	// ... Save State ...
}
