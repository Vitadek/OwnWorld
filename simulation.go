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

// --- World Gen (Deterministic) ---

func GetEfficiency(planetID int, resource string) float64 {
	input := fmt.Sprintf("%d-%s-%s", planetID, resource, ServerUUID)
	hashStr := hashBLAKE3([]byte(input))
	hashBytes, _ := hex.DecodeString(hashStr)
	val := binary.BigEndian.Uint16(hashBytes[:2])
	return (float64(val)/65535.0)*2.4 + 0.1
}

// --- Procedural Generation (Universal Truth) ---

// SectorPotential defines what exists at a coordinate purely based on math.
type SectorPotential struct {
    HasSystem   bool               `json:"has_system"`
    SystemType  string             `json:"system_type"` // "G2V", "M-Dwarf", "BlackHole"
    Resources   map[string]float64 `json:"resources"`   // "iron": 2.5 (High), "water": 0.1 (Low)
    Hazards     float64            `json:"hazards"`     // 0.0 to 1.0 (Pirate/Environmental Risk)
}

func GetSectorData(x, y, z int) SectorPotential {
    // 1. INPUT: Target Coordinates + Genesis Hash (The "Universal Constant")
    // We concatenate them to form a unique key for this point in space.
    input := fmt.Sprintf("%s-%d-%d-%d", GenesisHash, x, y, z)
    
    // 2. PURE RNG (Deterministic)
    // BLAKE3 is excellent here because it's fast and avalanche-prone (small change = huge diff)
    hash := hashBLAKE3([]byte(input))
    hashBytes, _ := hex.DecodeString(hash)
    
    // 3. PARSE THE HASH (The "DNA" of the Sector)
    // Byte 0: Existence (Is there a star here?)
    // In a 4X game, stars are rare. Let's say 5% chance.
    exists := hashBytes[0] < 13 // 13 is ~5% of 255
    
    if !exists {
        return SectorPotential{HasSystem: false}
    }

    // Byte 1: Star Type
    starByte := hashBytes[1]
    sType := "M-Dwarf" // Common
    if starByte > 200 { 
        sType = "O-Type" // Rare giant
    } else if starByte > 150 { 
        sType = "G2V" // Sun-like
    } else if starByte < 10 { 
        sType = "BlackHole" // Extremely Rare
    }
    
    // Bytes 2-10: Resource Efficiency (0.1 to 2.5)
    // We normalize 0-255 to 0.1-2.5
    res := make(map[string]float64)
    res["iron"] = (float64(hashBytes[2]) / 255.0) * 2.4 + 0.1
    res["gold"] = (float64(hashBytes[3]) / 255.0) * 2.4 + 0.1
    res["vegetation"] = (float64(hashBytes[4]) / 255.0) * 2.4 + 0.1
    res["water"] = (float64(hashBytes[5]) / 255.0) * 2.4 + 0.1
    res["fuel"] = (float64(hashBytes[6]) / 255.0) * 2.4 + 0.1

    // Hazard Level based on another byte
    hazards := float64(hashBytes[7]) / 255.0
    
    return SectorPotential{
        HasSystem:  true,
        SystemType: sType,
        Resources:  res,
        Hazards:    hazards,
    }
}

// --- Physics Helpers (Needed by Handlers) ---

func GetSystemCoords(sysID string) []int {
	var x, y, z int
	err := db.QueryRow("SELECT x, y, z FROM solar_systems WHERE id=?", sysID).Scan(&x, &y, &z)
	if err == nil {
		return []int{x, y, z}
	}
	return []int{0, 0, 0}
}

func CalculateFuelCost(origin, target []int, mass int, targetUUID string) int {
	dist := 0.0
	for i := 0; i < 3; i++ {
		dist += math.Pow(float64(origin[i]-target[i]), 2)
	}
	distance := math.Sqrt(dist)

	multiplier := 10.0 

	if targetUUID == ServerUUID {
		multiplier = 1.0 
	} else {
		peerLock.RLock()
		peer, known := Peers[targetUUID]
		peerLock.RUnlock()
		
		if known {
			if peer.Relation == 1 { multiplier = 2.5 } 
		}
	}

	return int(distance * float64(mass) * multiplier)
}

// Updated: Deep Space Arrival now uses the Universal Truth function
func resolveDeepSpaceArrival(fleet Fleet) {
    // Determine Target Coords from "sys-X-Y-Z" format or DB
    // Assuming fleet.DestSystem is "sys-X-Y-Z" for unexplored space
    var x, y, z int
    n, _ := fmt.Sscanf(fleet.DestSystem, "sys-%d-%d-%d", &x, &y, &z)
    
    if n != 3 {
        // Try to fetch from DB if it's a known ID (e.g., named system)
        coords := GetSystemCoords(fleet.DestSystem)
        x, y, z = coords[0], coords[1], coords[2]
        if x == 0 && y == 0 && z == 0 && fleet.DestSystem != "sys-0-0-0" {
             return // Invalid or non-coord system
        }
    }

    // 1. Check if we already know this system
    var exists int
    db.QueryRow("SELECT count(*) FROM solar_systems WHERE x=? AND y=? AND z=?", x, y, z).Scan(&exists)
    if exists > 0 { return } 

    // 2. Query the Universal Truth
    potential := GetSectorData(x, y, z)

    // 3. Persist Discovery
    if potential.HasSystem {
        sysID := fmt.Sprintf("sys-%d-%d-%d", x, y, z)
        // Store basic info. Detailed resources are procedural and don't need full storage unless colonized.
        // We store type to show on map.
        db.Exec("INSERT OR IGNORE INTO solar_systems (id, type, x, y, z, owner_uuid) VALUES (?, ?, ?, ?, ?, ?)", 
            sysID, potential.SystemType, x, y, z, "") // Unclaimed initially
            
        InfoLog.Printf("🚀 Fleet %d confirmed %s system at %s!", fleet.ID, potential.SystemType, sysID)
    } else {
        InfoLog.Printf("🚀 Fleet %d arrived at void sector %d,%d,%d.", fleet.ID, x, y, z)
    }
}

// --- Core Loop (Event Sourced) ---

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
	
	var prevHash string
	err = db.QueryRow("SELECT final_hash FROM daily_snapshots ORDER BY day_id DESC LIMIT 1").Scan(&prevHash)
	if err != nil {
		prevHash = GenesisHash 
	}

	combined := append(compressed, []byte(prevHash)...)
	finalHash := hashBLAKE3(combined)

	dayID := int(CurrentTick / 17280) 
	
	InfoLog.Printf("📸 Snapshot Day %d. Size: %d bytes. Hash: %s", dayID, len(compressed), finalHash)
	
	db.Exec("INSERT OR REPLACE INTO daily_snapshots (day_id, state_blob, final_hash) VALUES (?, ?, ?)", 
		dayID, compressed, finalHash)
}

func tickWorld() {
	stateLock.Lock()
	defer stateLock.Unlock()

	pruneTransactionCache()

	current := atomic.AddInt64(&CurrentTick, 1)
	db.Exec("INSERT INTO transaction_log (tick, action_type) VALUES (?, 'TICK')", current)

	// Process Fleets (Arrivals)
	fRows, _ := db.Query("SELECT id, dest_system, status, hull_class, modules_json FROM fleets WHERE arrival_tick <= ? AND status='TRANSIT'", current)
	defer fRows.Close()
	
	for fRows.Next() {
		var f Fleet
		var modJson string
		fRows.Scan(&f.ID, &f.DestSystem, &f.Status, &f.HullClass, &modJson)
		json.Unmarshal([]byte(modJson), &f.Modules)
		
		// Trigger Discovery Logic
		resolveDeepSpaceArrival(f)
		
		db.Exec("UPDATE fleets SET status='ORBIT' WHERE id=?", f.ID)
	}

	// Process Colonies
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

		foodEff := GetEfficiency(c.ID, "food")

		c.Food += int(float64(c.Buildings["farm"]*5) * foodEff)
		c.Water += int(float64(c.Buildings["well"]*5) * foodEff)

		reqFood := (c.PopLaborers * 1) + (c.PopSpecialists * 2) + (c.PopElites * 5)
		reqCarbon := c.PopSpecialists / 10

		if c.Food >= reqFood {
			c.Food -= reqFood
			c.StabilityTarget += 0.1
		} else {
			c.Food = 0
			c.StabilityTarget -= 5.0
			c.PopLaborers = (c.PopLaborers * 95) / 100
			c.PopSpecialists = (c.PopSpecialists * 95) / 100
			c.PopElites = (c.PopElites * 95) / 100
		}

		if c.Carbon >= reqCarbon {
			c.Carbon -= reqCarbon
		} else {
			c.StabilityTarget -= 0.5
		}

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
	
	if current % 17280 == 0 {
		go snapshotWorld()
	}

	recalculateLeader()
}

func runGameLoop() {
	ticker := time.NewTicker(5 * time.Second) 
	for {
		<-ticker.C
		
		offset := CalculateOffset()
		if offset > 0 {
			time.Sleep(offset)
		}
		
		tickWorld()
	}
}
