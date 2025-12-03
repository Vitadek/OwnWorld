package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"lukechampine.com/blake3"
)

func GetEfficiency(planetID int, resource string) float64 {
	input := fmt.Sprintf("%d-%s", planetID, resource)
	hash := blake3.Sum256([]byte(input))
	val := binary.BigEndian.Uint16(hash[:2])
	return (float64(val)/65535.0)*2.4 + 0.1
}

// Phase 2.1: Snapshot Logic
func snapshotWorld() {
	// 1. Serialize all Colonies
	// Note: In a real massive MMO, we would stream this or do partial snapshots.
	// For this Phase, we select all and dump.
	rows, _ := db.Query(`SELECT id, buildings_json, pop_laborers, iron, carbon, water, gold, vegetation, oxygen FROM colonies`)
	defer rows.Close()

	var colonies []Colony
	for rows.Next() {
		var c Colony
		var bJson string
		rows.Scan(&c.ID, &bJson, &c.PopLaborers, &c.Iron, &c.Carbon, &c.Water, &c.Gold, &c.Vegetation, &c.Oxygen)
		json.Unmarshal([]byte(bJson), &c.Buildings)
		colonies = append(colonies, c)
	}

	// 2. Serialize & Compress
	rawJSON, _ := json.Marshal(colonies)
	compressed := compressLZ4(rawJSON)

	// 3. Hash
	hash := hashBLAKE3(compressed)

	// 4. Persist
	// We use CurrentTick / 100 as a rough "DayID" for this prototype
	dayID := CurrentTick / 100
	db.Exec("INSERT OR REPLACE INTO daily_snapshots (day_id, state_blob, final_hash) VALUES (?, ?, ?)", 
		dayID, compressed, hash)
	
	InfoLog.Printf("Snapshot Day %d. Size: %d bytes. Hash: %s", dayID, len(compressed), hash[:8])
}

func tickWorld() {
	stateLock.Lock()
	defer stateLock.Unlock()

	CurrentTick++

	// 1. Colony Simulation
	rows, _ := db.Query(`SELECT id, buildings_json, pop_laborers, iron, carbon, water, gold, vegetation FROM colonies`)
	type ColUpdate struct {
		ID int; Iron, Carbon, Water, Gold, Veg int
		PopLab int
	}
	var updates []ColUpdate

	for rows.Next() {
		var c Colony
		var bJson string
		rows.Scan(&c.ID, &bJson, &c.PopLaborers, &c.Iron, &c.Carbon, &c.Water, &c.Gold, &c.Vegetation)
		json.Unmarshal([]byte(bJson), &c.Buildings)
		pid := c.ID
		effIron := GetEfficiency(pid, "iron")
		effWater := GetEfficiency(pid, "water")
		prodIron := int(float64(c.Buildings["iron_mine"]*10) * effIron)
		prodWater := int(float64(c.Buildings["well"]*20) * effWater)
		prodFood := int(float64(c.Buildings["farm"]*20))
		consFood := c.PopLaborers / 10
		consWater := c.PopLaborers / 10
		c.Iron += prodIron
		c.Water += prodWater - consWater
		c.Vegetation += prodFood - consFood
		if c.Water < 0 {
			c.Water = 0
		}
		if c.Vegetation < 0 {
			c.Vegetation = 0
		}
		if c.Water > 0 && c.Vegetation > 0 {
			c.PopLaborers += c.PopLaborers / 100
		} else {
			c.PopLaborers -= c.PopLaborers / 50
		}
		updates = append(updates, ColUpdate{
			ID: c.ID, Iron: c.Iron, Carbon: c.Carbon, Water: c.Water, Gold: c.Gold, Veg: c.Vegetation, PopLab: c.PopLaborers,
		})
	}
	rows.Close()
	tx, _ := db.Begin()
	stmt, _ := tx.Prepare(`UPDATE colonies SET iron=?, carbon=?, water=?, gold=?, vegetation=?, pop_laborers=? WHERE id=?`)
	for _, u := range updates {
		stmt.Exec(u.Iron, u.Carbon, u.Water, u.Gold, u.Veg, u.PopLab, u.ID)
	}
	stmt.Close()
	tx.Commit()

	// 2. Fleet Movement
	fRows, _ := db.Query("SELECT id, dest_system, arrival_tick FROM fleets WHERE status='TRANSIT'")
	for fRows.Next() {
		var fid, arrival int
		var dest string
		fRows.Scan(&fid, &dest, &arrival)
		if CurrentTick >= arrival {
			db.Exec("UPDATE fleets SET status='ORBIT', origin_system=? WHERE id=?", dest, fid)
			InfoLog.Printf("Fleet %d arrived at %s", fid, dest)
		}
	}
	fRows.Close()

	// 3. Hash Chain
	payload := fmt.Sprintf("%d-%s", CurrentTick, PreviousHash)
	finalBytes := blake3.Sum256([]byte(payload))
	PreviousHash = hex.EncodeToString(finalBytes[:])

	recalculateLeader()

	// Phase 2.1: Periodic Snapshots
	if CurrentTick % 100 == 0 {
		go snapshotWorld()
	}
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
