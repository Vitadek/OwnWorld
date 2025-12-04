package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sync/atomic"
	"time"
)

// --- World Gen ---

func GetEfficiency(planetID int, resource string) float64 {
	input := fmt.Sprintf("%d-%s-%s", planetID, resource, ServerUUID)
	hashStr := hashBLAKE3([]byte(input))
	hashBytes, _ := hex.DecodeString(hashStr)
	val := binary.BigEndian.Uint16(hashBytes[:2])
	return (float64(val)/65535.0)*2.4 + 0.1
}

// --- Physics Helpers ---

func GetSystemCoords(sysID string) []int {
	// 1. Check Local DB
	var x, y, z int
	err := db.QueryRow("SELECT x, y, z FROM solar_systems WHERE id=?", sysID).Scan(&x, &y, &z)
	if err == nil {
		return []int{x, y, z}
	}
	// 2. If not found (Remote System), returning 0,0,0 or handling error
	// In a real federation, we would query the Peer/Lighthouse.
	// For now, we assume 0,0,0 for unknown remote points (high risk travel)
	return []int{0, 0, 0}
}

func CalculateFuelCost(origin, target []int, mass int, targetUUID string) int {
	dist := 0.0
	for i := 0; i < 3; i++ {
		dist += math.Pow(float64(origin[i]-target[i]), 2)
	}
	distance := math.Sqrt(dist)

	multiplier := 10.0 // Default: Unknown Universe
	if targetUUID == ServerUUID {
		multiplier = 1.0 // Local
	} else {
		peerLock.RLock()
		peer, known := Peers[targetUUID]
		peerLock.RUnlock()
		
		if known {
			if peer.Relation == 1 { multiplier = 2.5 } // Federated
		}
	}

	return int(distance * float64(mass) * multiplier)
}

// --- Core Loop ---

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
	hash := hashBLAKE3(compressed)     

	dayID := int(CurrentTick / 17280) // Approx 1 day if 5s tick
	db.Exec("INSERT OR REPLACE INTO daily_snapshots (day_id, state_blob, final_hash) VALUES (?, ?, ?)", 
		dayID, compressed, hash)
}

func tickWorld() {
	stateLock.Lock()
	defer stateLock.Unlock()

	current := atomic.AddInt64(&CurrentTick, 1)
	db.Exec("INSERT INTO transaction_log (tick, action_type) VALUES (?, 'TICK')", current)

	rows, err := db.Query(`SELECT id, buildings_json, pop_laborers, pop_specialists, pop_elites, 
	                       food, water, carbon, gold, fuel, stability_current, stability_target 
	                       FROM colonies`)
	if err != nil { return }
	defer rows.Close()
	
	type ColUpdate struct {
		ID int
		Food, Water, Carbon, Gold, Fuel int
		PopLab, PopSpec, PopElite int
		Stability, Target float64
	}
	var updates []ColUpdate

	for rows.Next() {
		var c Colony
		var bJson string
		rows.Scan(&c.ID, &bJson, &c.PopLaborers, &c.PopSpecialists, &c.PopElites, 
			&c.Food, &c.Water, &c.Carbon, &c.Gold, &c.Fuel, &c.StabilityCurrent, &c.StabilityTarget)
		json.Unmarshal([]byte(bJson), &c.Buildings)

		// 1. EFFICIENCY & PRODUCTION
		foodEff := GetEfficiency(c.ID, "food")
		// ironEff := GetEfficiency(c.ID, "iron")

		c.Food += int(float64(c.Buildings["farm"]*5) * foodEff)
		c.Water += int(float64(c.Buildings["well"]*5) * foodEff)
		// ... Iron logic would go here

		// 2. CONSUMPTION
		// Laborers: 1 Food
		// Specialists: 2 Food + 0.1 Carbon
		reqFood := (c.PopLaborers * 1) + (c.PopSpecialists * 2)
		reqCarbon := c.PopSpecialists / 10

		if c.Food >= reqFood {
			c.Food -= reqFood
			c.StabilityTarget += 0.1
		} else {
			c.Food = 0
			c.StabilityTarget -= 5.0
			c.PopLaborers = int(float64(c.PopLaborers) * 0.95) // Starvation
		}

		if c.Carbon >= reqCarbon {
			c.Carbon -= reqCarbon
		} else {
			c.StabilityTarget -= 0.5
		}

		// 3. DRIFT
		diff := c.StabilityTarget - c.StabilityCurrent
		c.StabilityCurrent += diff * 0.05
		c.StabilityTarget = 100.0 

		updates = append(updates, ColUpdate{
			ID: c.ID, Food: c.Food, Water: c.Water, Carbon: c.Carbon,
			PopLab: c.PopLaborers, PopSpec: c.PopSpecialists, PopElite: c.PopElites,
			Stability: c.StabilityCurrent, Target: c.StabilityTarget,
		})
	}

	if len(updates) > 0 {
		tx, _ := db.Begin()
		stmt, _ := tx.Prepare(`UPDATE colonies SET food=?, water=?, carbon=?, 
		                       pop_laborers=?, pop_specialists=?, pop_elites=?, stability_current=?, stability_target=? WHERE id=?`)
		for _, u := range updates {
			stmt.Exec(u.Food, u.Water, u.Carbon, u.PopLab, u.PopSpec, u.PopElite, u.Stability, u.Target, u.ID)
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
		if CalculateOffset() > 0 {
			time.Sleep(CalculateOffset())
		}
		tickWorld()
	}
}
