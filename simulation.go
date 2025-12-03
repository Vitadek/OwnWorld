package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Phase 5: Simulation & Mechanics

func GetEfficiency(planetID int, resource string) float64 {
	input := fmt.Sprintf("%d-%s", planetID, resource)
	// Use hashBLAKE3 from utils.go
	hashStr := hashBLAKE3([]byte(input))
	hashBytes, _ := hex.DecodeString(hashStr)
	val := binary.BigEndian.Uint16(hashBytes[:2])
	return (float64(val)/65535.0)*2.4 + 0.1
}

// Phase 2.1: Snapshot with Compression
// In snapshotWorld()
func snapshotWorld() {
    // --- PART 1: KEEP THIS EXISTING CODE ---
    rows, err := db.Query(`SELECT id, buildings_json, pop_laborers, pop_specialists, pop_elites, iron, carbon, water, gold, vegetation, oxygen FROM colonies`)
    if err != nil {
        ErrorLog.Printf("Snapshot Query Error: %v", err)
        return
    }
    defer rows.Close()

    var colonies []Colony
    for rows.Next() {
        var c Colony
        var bJson string
        rows.Scan(&c.ID, &bJson, &c.PopLaborers, &c.PopSpecialists, &c.PopElites, &c.Iron, &c.Carbon, &c.Water, &c.Gold, &c.Vegetation, &c.Oxygen)
        json.Unmarshal([]byte(bJson), &c.Buildings)
        colonies = append(colonies, c)
    }

    // --- PART 2: UPDATE THIS SECTION (The Fix) ---
    // Now 'colonies' is defined and populated
    rawJSON, _ := json.Marshal(colonies)
    compressed := compressLZ4(rawJSON)

    // Hash Chain Logic: Previous Hash + Current Data
    payload := append([]byte(PreviousHash), compressed...)
    currentHash := hashBLAKE3(payload)

    dayID := CurrentTick / 100

    // Save to DB
    _, err = db.Exec("INSERT OR REPLACE INTO daily_snapshots (day_id, state_blob, final_hash) VALUES (?, ?, ?)",
        dayID, compressed, currentHash)

    // Update In-Memory State
    if err == nil {
        PreviousHash = currentHash
        InfoLog.Printf("Snapshot Day %d. Hash: %s", dayID, currentHash)
    }
}

// Phase 5.3: Advanced Pop & Stability
func tickWorld() {
	stateLock.Lock()
	defer stateLock.Unlock()

	CurrentTick++

	// Fetch Full Colony Data (V3 Schema)
	rows, err := db.Query(`SELECT id, buildings_json, pop_laborers, pop_specialists, pop_elites, 
	                       food, water, carbon, gold, fuel, stability_current, stability_target 
	                       FROM colonies`)
	if err != nil {
		if CurrentTick % 10 == 0 { ErrorLog.Printf("Tick DB Error: %v", err) }
		return
	}
	defer rows.Close()
	
	type ColUpdate struct {
		ID int
		Food, Water, Carbon, Gold, Fuel int
		PopLab, PopSpec, PopElite int
		Stability float64
	}
	var updates []ColUpdate

	for rows.Next() {
		var c Colony
		var bJson string
		rows.Scan(&c.ID, &bJson, &c.PopLaborers, &c.PopSpecialists, &c.PopElites, 
			&c.Food, &c.Water, &c.Carbon, &c.Gold, &c.Fuel, &c.StabilityCurrent, &c.StabilityTarget)
		json.Unmarshal([]byte(bJson), &c.Buildings)

		// Simple Production
		c.Food += c.Buildings["farm"] * 5
		c.Water += c.Buildings["well"] * 5
		
		// Stability Drift
		diff := c.StabilityTarget - c.StabilityCurrent
		c.StabilityCurrent += diff * 0.05
		c.StabilityTarget = 100.0

		updates = append(updates, ColUpdate{
			ID: c.ID, Food: c.Food, Water: c.Water, Carbon: c.Carbon, Gold: c.Gold, Fuel: c.Fuel,
			PopLab: c.PopLaborers, PopSpec: c.PopSpecialists, PopElite: c.PopElites,
			Stability: c.StabilityCurrent,
		})
	}

	// Commit Updates
	if len(updates) > 0 {
		tx, _ := db.Begin()
		stmt, _ := tx.Prepare(`UPDATE colonies SET food=?, water=?, carbon=?, gold=?, fuel=?, 
		                       pop_laborers=?, pop_specialists=?, pop_elites=?, stability_current=? WHERE id=?`)
		for _, u := range updates {
			stmt.Exec(u.Food, u.Water, u.Carbon, u.Gold, u.Fuel, u.PopLab, u.PopSpec, u.PopElite, u.Stability, u.ID)
		}
		stmt.Close()
		tx.Commit()
	}

	recalculateLeader()
}

func runGameLoop() {
	InfoLog.Println("Starting Galaxy Engine...")
	ticker := time.NewTicker(5 * time.Second) 
	for {
		<-ticker.C
		if PhaseOffset > 0 {
			time.Sleep(PhaseOffset)
		}
		tickWorld()
	}
}
