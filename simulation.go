package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	mrand "math/rand" 
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

type SectorPotential struct {
	HasSystem  bool               `json:"has_system"`
	SystemType string             `json:"system_type"`
	Resources  map[string]float64 `json:"resources"`
	Hazards    float64            `json:"hazards"`
}

func GetSectorData(x, y, z int) SectorPotential {
	input := fmt.Sprintf("%s-%d-%d-%d", GenesisHash, x, y, z)
	hash := hashBLAKE3([]byte(input))
	hashBytes, _ := hex.DecodeString(hash)

	exists := hashBytes[0] < 13

	if !exists {
		return SectorPotential{HasSystem: false}
	}

	starByte := hashBytes[1]
	sType := "M-Dwarf"
	if starByte > 200 {
		sType = "O-Type"
	} else if starByte > 150 {
		sType = "G2V"
	} else if starByte < 10 {
		sType = "BlackHole"
	}

	res := make(map[string]float64)
	res["iron"] = (float64(hashBytes[2]) / 255.0) * 2.4 + 0.1
	res["gold"] = (float64(hashBytes[3]) / 255.0) * 2.4 + 0.1
	res["vegetation"] = (float64(hashBytes[4]) / 255.0) * 2.4 + 0.1
	res["water"] = (float64(hashBytes[5]) / 255.0) * 2.4 + 0.1
	res["fuel"] = (float64(hashBytes[6]) / 255.0) * 2.4 + 0.1

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
			if peer.Relation == 1 {
				multiplier = 2.5
			}
		}
	}

	return int(distance * float64(mass) * multiplier)
}

func resolveDeepSpaceArrival(fleet Fleet) {
	var x, y, z int
	n, _ := fmt.Sscanf(fleet.DestSystem, "sys-%d-%d-%d", &x, &y, &z)

	if n != 3 {
		coords := GetSystemCoords(fleet.DestSystem)
		x, y, z = coords[0], coords[1], coords[2]
		if x == 0 && y == 0 && z == 0 && fleet.DestSystem != "sys-0-0-0" {
			return
		}
	}

	var exists int
	db.QueryRow("SELECT count(*) FROM solar_systems WHERE x=? AND y=? AND z=?", x, y, z).Scan(&exists)
	if exists > 0 {
		return
	}

	potential := GetSectorData(x, y, z)

	if potential.HasSystem {
		sysID := fmt.Sprintf("sys-%d-%d-%d", x, y, z)
		db.Exec("INSERT OR IGNORE INTO solar_systems (id, type, x, y, z, owner_uuid) VALUES (?, ?, ?, ?, ?, ?)",
			sysID, potential.SystemType, x, y, z, "")

		InfoLog.Printf("🚀 Fleet %d confirmed %s system at %s!", fleet.ID, potential.SystemType, sysID)
	} else {
		InfoLog.Printf("🚀 Fleet %d arrived at void sector %d,%d,%d.", fleet.ID, x, y, z)
	}
}

// --- New Mechanics: Refining & Combat ---

func processIndustry(c *Colony) {
	// 1. Steel Production (Tier 2)
	if count, ok := c.Buildings["steel_mill"]; ok && count > 0 {
		efficiency := GetEfficiency(c.ID, "iron")
		inputIron := 2 * count
		inputCarbon := 1 * count
		outputSteel := int(float64(count)*efficiency) + 1

		if c.Iron >= inputIron && c.Carbon >= inputCarbon {
			c.Iron -= inputIron
			c.Carbon -= inputCarbon
			c.Steel += outputSteel
		}
	}

	// 2. Wine Production (Tier 3 - Luxury)
	if count, ok := c.Buildings["winery"]; ok && count > 0 {
		efficiency := GetEfficiency(c.ID, "vegetation")
		inputVeg := 5 * count
		outputWine := int(float64(count)*efficiency) + 1

		if c.Vegetation >= inputVeg {
			c.Vegetation -= inputVeg
			c.Wine += outputWine
		}
	}
}

func resolveSectorConflict(currentTick int64) {
	// 1. Fleet vs Fleet (Space Combat)
	rows, _ := db.Query("SELECT id, owner_uuid, origin_system, hull_class, modules_json FROM fleets WHERE status='ORBIT'")
	defer rows.Close()

	systemFleets := make(map[string][]Fleet)

	for rows.Next() {
		var f Fleet
		var modJson string
		rows.Scan(&f.ID, &f.OwnerUUID, &f.OriginSystem, &f.HullClass, &modJson)
		json.Unmarshal([]byte(modJson), &f.Modules)
		systemFleets[f.OriginSystem] = append(systemFleets[f.OriginSystem], f)
	}

	for sysID, fleets := range systemFleets {
		if len(fleets) < 2 {
			continue
		}

		var combatants []Fleet
		owners := make(map[string]bool)
		for _, f := range fleets {
			owners[f.OwnerUUID] = true
			combatants = append(combatants, f)
		}

		if len(owners) > 1 {
			InfoLog.Printf("⚔️ Space Combat initiated in %s", sysID)

			for _, attacker := range combatants {
				dmg := 0
				for _, m := range attacker.Modules {
					if m == "laser" {
						dmg += 10
					}
					if m == "railgun" {
						dmg += 20
					}
				}

				if dmg > 0 {
					for _, victim := range combatants {
						if victim.OwnerUUID != attacker.OwnerUUID {
							chance := float64(dmg) / 100.0
							if mrand.Float64() < chance {
								InfoLog.Printf("💥 Fleet %d destroyed by Fleet %d!", victim.ID, attacker.ID)
								db.Exec("DELETE FROM fleets WHERE id=?", victim.ID)
								reportGrievance(attacker.OwnerUUID, victim.OwnerUUID, 100)
							}
						}
					}
				}
			}
		}
	}

	// 2. Orbital Bombardment
	bRows, _ := db.Query(`SELECT f.id, f.owner_uuid, f.origin_system, f.modules_json 
	                      FROM fleets f 
	                      WHERE f.status='ORBIT' AND f.modules_json LIKE '%bomb_bay%'`)
	defer bRows.Close()

	for bRows.Next() {
		var f Fleet
		var modJson string
		bRows.Scan(&f.ID, &f.OwnerUUID, &f.OriginSystem, &modJson)

		var colID int
		var colOwner, bJson string
		err := db.QueryRow("SELECT id, owner_uuid, buildings_json FROM colonies WHERE system_id=?", f.OriginSystem).Scan(&colID, &colOwner, &bJson)

		if err == nil && colOwner != f.OwnerUUID {
			damage := 5
			buildings := make(map[string]int)
			json.Unmarshal([]byte(bJson), &buildings)

			destroyed := 0
			for k, v := range buildings {
				if v > 0 {
					take := v
					if take > damage {
						take = damage
					}
					buildings[k] -= take
					damage -= take
					destroyed += take
					if damage <= 0 {
						break
					}
				}
			}

			if destroyed > 0 {
				newBJson, _ := json.Marshal(buildings)
				db.Exec("UPDATE colonies SET buildings_json=?, stability_target=stability_target-20 WHERE id=?", string(newBJson), colID)
				InfoLog.Printf("🔥 Colony %d bombarded by Fleet %d. %d structures lost.", colID, f.ID, destroyed)

				reportGrievance(f.OwnerUUID, colOwner, destroyed*10)
			}
		}
	}
}

func reportGrievance(offender, victim string, damage int) {
	db.Exec("INSERT INTO grievances (offender_uuid, victim_uuid, damage_amount, tick) VALUES (?, ?, ?, ?)",
		offender, victim, damage, atomic.LoadInt64(&CurrentTick))
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

	// 1. Process Fleets
	fRows, _ := db.Query("SELECT id, dest_system, status, hull_class, modules_json FROM fleets WHERE arrival_tick <= ? AND status='TRANSIT'", current)
	defer fRows.Close()

	for fRows.Next() {
		var f Fleet
		var modJson string
		fRows.Scan(&f.ID, &f.DestSystem, &f.Status, &f.HullClass, &modJson)
		json.Unmarshal([]byte(modJson), &f.Modules)

		resolveDeepSpaceArrival(f)
		db.Exec("UPDATE fleets SET status='ORBIT' WHERE id=?", f.ID)
	}

	// 2. Conflict Resolution
	resolveSectorConflict(current)

	// 3. Process Colonies
	rows, err := db.Query(`SELECT id, buildings_json, policies_json, pop_laborers, pop_specialists, pop_elites, 
	                       food, water, carbon, gold, fuel, steel, wine, vegetation, stability_current, stability_target 
	                       FROM colonies`)
	if err != nil {
		return
	}
	defer rows.Close()

	type ColUpdate struct {
		ID                                                int
		Food, Water, Carbon, Gold, Fuel, Steel, Wine, Veg int
		PopLab, PopSpec, PopElite                         int
		Stability, Target                                 float64
	}
	var updates []ColUpdate

	for rows.Next() {
		var c Colony
		var bJson, pJson string
		rows.Scan(&c.ID, &bJson, &pJson, &c.PopLaborers, &c.PopSpecialists, &c.PopElites,
			&c.Food, &c.Water, &c.Carbon, &c.Gold, &c.Fuel, &c.Steel, &c.Wine, &c.Vegetation,
			&c.StabilityCurrent, &c.StabilityTarget)
		json.Unmarshal([]byte(bJson), &c.Buildings)
		
		// --- NEW: POLICY LOGIC ---
		// 1. Parse Policies
		c.Policies = make(map[string]bool)
		if pJson != "" {
			json.Unmarshal([]byte(pJson), &c.Policies)
		}

		foodEff := GetEfficiency(c.ID, "food")
		c.Food += int(float64(c.Buildings["farm"]*5) * foodEff)
		c.Water += int(float64(c.Buildings["well"]*5) * foodEff)

		processIndustry(&c)

		// 2. Calculate Modifiers
		foodConsumptionRate := 1.0
		stabilityMod := 0.0
		
		if c.Policies["rationing"] {
			foodConsumptionRate = 0.5       // Eat half as much
			stabilityMod -= 2.0             // People are unhappy
		}
		if c.Policies["propaganda"] {
			stabilityMod += 1.0             // Artificial happiness
			c.Gold -= 10                    // Costs gold per tick
		}

		// 3. Apply Consumption
		reqFood := int(float64(c.PopLaborers * 1 + c.PopSpecialists * 2 + c.PopElites * 5) * foodConsumptionRate)
		reqCarbon := c.PopSpecialists / 10
		reqWine := c.PopElites / 10

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

		if c.Wine >= reqWine {
			c.Wine -= reqWine
			c.StabilityTarget += 0.5
		} else {
			if c.PopElites > 0 {
				c.StabilityTarget -= 5.0
			}
		}

		// 4. Apply Stability
		c.StabilityTarget += stabilityMod

		diff := c.StabilityTarget - c.StabilityCurrent
		c.StabilityCurrent += diff * 0.05
		if c.StabilityTarget > 100 {
			c.StabilityTarget = 100
		}
		if c.StabilityTarget < 0 {
			c.StabilityTarget = 0
		}

		updates = append(updates, ColUpdate{
			ID: c.ID, Food: c.Food, Water: c.Water, Carbon: c.Carbon,
			PopLab: c.PopLaborers, PopSpec: c.PopSpecialists, PopElite: c.PopElites,
			Steel: c.Steel, Wine: c.Wine, Veg: c.Vegetation,
			Stability: c.StabilityCurrent, Target: c.StabilityTarget,
		})
	}

	if len(updates) > 0 {
		tx, _ := db.Begin()
		stmt, _ := tx.Prepare(`UPDATE colonies SET 
			food=?, water=?, carbon=?, steel=?, wine=?, vegetation=?,
			pop_laborers=?, pop_specialists=?, pop_elites=?, 
			stability_current=?, stability_target=? 
			WHERE id=?`)
		for _, u := range updates {
			stmt.Exec(u.Food, u.Water, u.Carbon, u.Steel, u.Wine, u.Veg,
				u.PopLab, u.PopSpec, u.PopElite, u.Stability, u.Target, u.ID)
		}
		stmt.Close()
		tx.Commit()
	}

	if current%17280 == 0 {
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
