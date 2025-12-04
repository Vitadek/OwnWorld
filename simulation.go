package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

func GetEfficiency(planetID int, resource string) float64 {
	input := fmt.Sprintf("%d-%s-%s", planetID, resource, ServerUUID)
	hashStr := hashBLAKE3([]byte(input))
	hashBytes, _ := hex.DecodeString(hashStr)
	val := binary.BigEndian.Uint16(hashBytes[:2])
	return (float64(val)/65535.0)*2.4 + 0.1
}

func snapshotWorld() {
	// Simplified snapshot
}

func tickWorld() {
	stateLock.Lock()
	defer stateLock.Unlock()

	// Use atomic correctly inside lock
	CurrentTick++
	current := CurrentTick
	
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
		// Ensure scan order matches Query
		rows.Scan(&c.ID, &bJson, &c.PopLaborers, &c.PopSpecialists, &c.PopElites, 
			&c.Food, &c.Water, &c.Carbon, &c.Gold, &c.Fuel, &c.StabilityCurrent, &c.StabilityTarget)
		json.Unmarshal([]byte(bJson), &c.Buildings)

		c.Food += c.Buildings["farm"] * 5
		c.Water += c.Buildings["well"] * 5
		
		diff := c.StabilityTarget - c.StabilityCurrent
		c.StabilityCurrent += diff * 0.05
		c.StabilityTarget = 100.0

		updates = append(updates, ColUpdate{
			ID: c.ID, Food: c.Food, Water: c.Water, Carbon: c.Carbon, Gold: c.Gold, Fuel: c.Fuel,
			PopLab: c.PopLaborers, PopSpec: c.PopSpecialists, PopElite: c.PopElites,
			Stability: c.StabilityCurrent, Target: c.StabilityTarget,
		})
	}

	if len(updates) > 0 {
		tx, _ := db.Begin()
		stmt, _ := tx.Prepare(`UPDATE colonies SET food=?, water=?, carbon=?, gold=?, fuel=?,
		                       pop_laborers=?, pop_specialists=?, pop_elites=?, stability_current=?, stability_target=? WHERE id=?`)
		for _, u := range updates {
			stmt.Exec(u.Food, u.Water, u.Carbon, u.Gold, u.Fuel, u.PopLab, u.PopSpec, u.PopElite, u.Stability, u.Target, u.ID)
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
