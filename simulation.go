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
	rows, err := db.Query(`SELECT id, buildings_json, pop_laborers, pop_specialists, iron, carbon, water, gold, vegetation, oxygen FROM colonies`)
	if err != nil {
		ErrorLog.Printf("Snapshot Query Error: %v", err)
		return
	}
	defer rows.Close()

	var colonies []Colony
	for rows.Next() {
		var c Colony
		var bJson string
		rows.Scan(&c.ID, &bJson, &c.PopLaborers, &c.PopSpecialists, &c.Iron, &c.Carbon, &c.Water, &c.Gold, &c.Vegetation, &c.Oxygen)
		json.Unmarshal([]byte(bJson), &c.Buildings)
		colonies = append(colonies, c)
	}

	rawJSON, _ := json.Marshal(colonies)
	compressed := compressLZ4(rawJSON)
	hash := hashBLAKE3(compressed)

	dayID := CurrentTick / 100
	db.Exec("INSERT OR REPLACE INTO daily_snapshots (day_id, state_blob, final_hash) VALUES (?, ?, ?)", 
		dayID, compressed, hash)
	
	InfoLog.Printf("Snapshot Day %d. Size: %d bytes. Hash: %s", dayID, len(compressed), hash[:8])
}

// Phase 5.3: Advanced Population & Stability Logic
func tickWorld() {
	stateLock.Lock()
	defer stateLock.Unlock()

	CurrentTick++

	rows, err := db.Query(`SELECT id, buildings_json, pop_laborers, pop_specialists, iron, carbon, water, gold, vegetation, stability_current FROM colonies`)
	if err != nil {
		// Log error but don't panic. This allows the server to keep running even if DB is briefly locked or invalid.
		// Common cause: Tables not created yet on first boot tick.
		if CurrentTick % 10 == 0 { // Don't spam logs every tick
			ErrorLog.Printf("Tick Simulation Error (DB Query): %v", err)
		}
		return
	}
	
	type ColUpdate struct {
		ID int
		Iron, Carbon, Water, Gold, Veg int
		PopLab, PopSpec int
		Stability float64
	}
	var updates []ColUpdate

	for rows.Next() {
		var c Colony
		var bJson string
		rows.Scan(&c.ID, &bJson, &c.PopLaborers, &c.PopSpecialists, &c.Iron, &c.Carbon, &c.Water, &c.Gold, &c.Vegetation, &c.StabilityCurrent)
		json.Unmarshal([]byte(bJson), &c.Buildings)
		
		pid := c.ID // Proxy for planet ID
		effIron := GetEfficiency(pid, "iron")
		effWater := GetEfficiency(pid, "water")
		
		// 1. Production
		// Stability Penalty: If stability < 50, production is halved.
		prodMult := 1.0
		if c.StabilityCurrent < 50.0 {
			prodMult = 0.5
		}
		if c.StabilityCurrent < 25.0 {
			prodMult = 0.0 // Riots stop production
		}

		prodIron := int(float64(c.Buildings["iron_mine"]*10) * effIron * prodMult)
		prodWater := int(float64(c.Buildings["well"]*20) * effWater * prodMult)
		prodFood := int(float64(c.Buildings["farm"]*20) * prodMult) // Veg = Food

		// 2. Consumption (Classes)
		// Laborers: 1 Food
		// Specialists: 1 Food + 1 Carbon (Consumer Goods)
		
		foodDemand := c.PopLaborers + c.PopSpecialists
		goodsDemand := c.PopSpecialists

		eatenFood := 0
		eatenGoods := 0

		// Consume Food
		if c.Vegetation >= foodDemand {
			c.Vegetation -= foodDemand
			eatenFood = foodDemand
		} else {
			eatenFood = c.Vegetation
			c.Vegetation = 0
		}

		// Consume Goods (Carbon)
		if c.Carbon >= goodsDemand {
			c.Carbon -= goodsDemand
			eatenGoods = goodsDemand
		} else {
			eatenGoods = c.Carbon
			c.Carbon = 0
		}

		c.Iron += prodIron
		c.Water += prodWater
		c.Vegetation += prodFood

		// 3. Growth & Death
		// People die if they don't eat.
		if eatenFood < foodDemand {
			starving := foodDemand - eatenFood
			// Kill laborers first (grim reality of simulation)
			c.PopLaborers -= starving
			if c.PopLaborers < 0 { c.PopLaborers = 0 }
		} else {
			// Growth (1%)
			c.PopLaborers += c.PopLaborers / 100
		}

		// Specialists leave if no goods
		if eatenGoods < goodsDemand {
			leaving := goodsDemand - eatenGoods
			c.PopSpecialists -= leaving
			c.PopLaborers += leaving // Demoted to laborers
		}

		// 4. Stability Calculation
		// Target = 100 - Crime
		// Crime = (Pop / 1000) - (Police * 2)
		totalPop := c.PopLaborers + c.PopSpecialists
		police := c.Buildings["police_station"]
		crime := (float64(totalPop) / 1000.0) - (float64(police) * 2.0)
		if crime < 0 { crime = 0 }
		
		targetStability := 100.0 - (crime * 10.0)
		if eatenFood < foodDemand { targetStability -= 50.0 } // Starvation causes unrest

		// Drift towards target (Soft Cap)
		// Stability moves 10% of the distance to target per tick
		c.StabilityCurrent += (targetStability - c.StabilityCurrent) * 0.1

		// Cap Resources
		if c.Water < 0 { c.Water = 0 }

		updates = append(updates, ColUpdate{
			ID: c.ID, Iron: c.Iron, Carbon: c.Carbon, Water: c.Water, Gold: c.Gold, Veg: c.Vegetation, 
			PopLab: c.PopLaborers, PopSpec: c.PopSpecialists, Stability: c.StabilityCurrent,
		})
	}
	rows.Close()

	if len(updates) > 0 {
		tx, err := db.Begin()
		if err != nil {
			ErrorLog.Printf("Tick Error (Tx Begin): %v", err)
			return
		}
		stmt, _ := tx.Prepare(`UPDATE colonies SET iron=?, carbon=?, water=?, gold=?, vegetation=?, pop_laborers=?, pop_specialists=?, stability_current=? WHERE id=?`)
		for _, u := range updates {
			stmt.Exec(u.Iron, u.Carbon, u.Water, u.Gold, u.Veg, u.PopLab, u.PopSpec, u.Stability, u.ID)
		}
		stmt.Close()
		tx.Commit()
	}

	// 5. Fleet Movement
	fRows, err := db.Query("SELECT id, dest_system, arrival_tick FROM fleets WHERE status='TRANSIT'")
	if err == nil {
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
	}

	// 6. Hash Chain
	payload := fmt.Sprintf("%d-%s", CurrentTick, PreviousHash)
	finalBytes := blake3.Sum256([]byte(payload))
	PreviousHash = hex.EncodeToString(finalBytes[:])

	recalculateLeader()

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
