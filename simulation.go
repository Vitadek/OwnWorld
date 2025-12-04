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
func snapshotWorld() {
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

	// 1. Arrival Logic (Fleets)
	db.Exec("UPDATE fleets SET status='ORBIT', origin_system=dest_system WHERE status='TRANSIT' AND arrival_tick <= ?", CurrentTick)

	// 2. Colony Logic
	rows, err := db.Query(`SELECT id, buildings_json, pop_laborers, pop_specialists, pop_elites, 
	                       food, water, carbon, gold, fuel, stability_current, stability_target 
	                       FROM colonies`)
	if err != nil {
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

		// --- ECONOMY ENGINE v2 ---
		
		// 1. Consumption
		// Laborers: 1 Food
		// Specialists: 2 Food + 1 Carbon (Consumer Goods)
		// Elites: 5 Food + 1 Gold (Luxury)
		neededFood := (c.PopLaborers * 1) + (c.PopSpecialists * 2) + (c.PopElites * 5)
		neededCarbon := c.PopSpecialists * 1
		neededGold := c.PopElites * 1

		// Apply Consumption
		if c.Food >= neededFood { c.Food -= neededFood } else {
			c.StabilityTarget -= 5.0
			// Starvation Death (5%)
			c.PopLaborers = int(float64(c.PopLaborers) * 0.95)
		}

		if c.Carbon >= neededCarbon { c.Carbon -= neededCarbon } else {
			// Specialists downgrade to Laborers if unhappy
			if c.PopSpecialists > 0 {
				c.PopSpecialists--
				c.PopLaborers++
			}
		}

		if c.Gold >= neededGold { c.Gold -= neededGold } // Elites just complain (Stability hit)

		// 2. Production
		// Multipliers based on Class ratios
		effLab := float64(c.PopLaborers) / 100.0
		// effSpec := float64(c.PopSpecialists) / 10.0

		// Basic Resources
		c.Food += int(float64(c.Buildings["farm"] * 10) * effLab)
		c.Water += int(float64(c.Buildings["well"] * 10) * effLab)
		
		// Mining (Depends on Planet Efficiency)
		sysHash := c.ID // Simplified planet ID usage
		c.Iron += int(float64(c.Buildings["iron_mine"] * 5) * effLab * GetEfficiency(sysHash, "iron"))
		
		// Stability Drift
		diff := c.StabilityTarget - c.StabilityCurrent
		c.StabilityCurrent += diff * 0.1
		c.StabilityTarget = 100.0 // Reset baseline

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

// Phase 4: Time Lord Loop (Updated for V3.1)
func runGameLoop() {
	InfoLog.Println("Starting Galaxy Engine (V3.1 Time Lord Mode)...")
	
	for {
		// 1. Calculate Offset based on Election/Rank
		offset := CalculateOffset()
		
		// 2. Determine Target Time (Global 5s Grid)
		// We align to the nearest 5-second mark (Unix Epoch)
		now := time.Now().UnixMilli()
		target := ((now / 5000) * 5000) + 5000 + offset.Milliseconds()
		
		// 3. Sleep until Target
		sleep := time.Until(time.UnixMilli(target))
		if sleep > 0 {
			time.Sleep(sleep)
		}

		// 4. Execute Tick
		tickWorld()
	}
}
