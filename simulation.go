package main

import (
	"database/sql" // Required for sql.NullString
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	mrand "math/rand"
	"sync/atomic"
	"time"
)

// --- Constants ---
const (
	UniverseSize = 1000000 
	MaxResource  = 2000000000 
)

func safeAdd(current, amount int) int {
	if amount > 0 && current > MaxResource-amount {
		return MaxResource
	}
	return current + amount
}

// --- World Gen ---

func GetEfficiency(planetID int, resource string) float64 {
	input := fmt.Sprintf("%d-%s-%s", planetID, resource, ServerUUID)
	hashStr := hashBLAKE3([]byte(input))
	hashBytes, _ := hex.DecodeString(hashStr)
	val := binary.BigEndian.Uint16(hashBytes[:2])
	return (float64(val)/65535.0)*2.4 + 0.1
}

type SectorPotential struct {
	HasSystem  bool               `json:"has_system"`
	SystemType string             `json:"system_type"`
	Resources  map[string]float64 `json:"resources"`
	Hazards    float64            `json:"hazards"`
}

func GetSectorData(x, y, z int) SectorPotential {
	if x > UniverseSize || x < -UniverseSize || y > UniverseSize || y < -UniverseSize || z > UniverseSize || z < -UniverseSize {
		return SectorPotential{HasSystem: false}
	}

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
	
    res["uranium_ore"] = (float64(hashBytes[6]) / 255.0) * 1.5 + 0.05
    res["platinum_ore"] = (float64(hashBytes[8]) / 255.0) * 1.5 + 0.05
    res["diamond_ore"] = (float64(hashBytes[9]) / 255.0) * 1.2 + 0.01

	hazards := float64(hashBytes[7]) / 255.0

	return SectorPotential{
		HasSystem:  true,
		SystemType: sType,
		Resources:  res,
		Hazards:    hazards,
	}
}

// --- Physics & Fleet Logic ---

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
		if known && peer.Relation == 1 {
			multiplier = 2.5
		}
	}
	return int(distance * float64(mass) * multiplier)
}

func resolveDeepSpaceArrival(fleet Fleet) {
    // 1. Discovery Logic
	var x, y, z int
	n, _ := fmt.Sscanf(fleet.DestSystem, "sys-%d-%d-%d", &x, &y, &z)

	if n != 3 {
		coords := GetSystemCoords(fleet.DestSystem)
		x, y, z = coords[0], coords[1], coords[2]
	}

	if x > UniverseSize || x < -UniverseSize || y > UniverseSize || y < -UniverseSize || z > UniverseSize || z < -UniverseSize {
		return
	}

	var exists int
	db.QueryRow("SELECT count(*) FROM solar_systems WHERE x=? AND y=? AND z=?", x, y, z).Scan(&exists)
	if exists == 0 {
		potential := GetSectorData(x, y, z)
		if potential.HasSystem {
			sysID := fmt.Sprintf("sys-%d-%d-%d", x, y, z)
			db.Exec("INSERT OR IGNORE INTO solar_systems (id, type, x, y, z, owner_uuid) VALUES (?, ?, ?, ?, ?, ?)",
				sysID, potential.SystemType, x, y, z, "")
			InfoLog.Printf("🚀 Fleet %d discovered %s!", fleet.ID, sysID)
		}
	}

    // 2. ATOMIC SWAP LOGIC (Market Fulfillment)
    if fleet.TargetOrderID != "" {
        row := db.QueryRow("SELECT item, quantity, price, is_buy, seller_uuid FROM market_orders WHERE order_id=?", fleet.TargetOrderID)
        
        var item, sellerUUID string
        var qty, price int
        var isBuy bool
        
        if err := row.Scan(&item, &qty, &price, &isBuy, &sellerUUID); err == nil {
            // Fetch Colony at destination (The Trading Partner)
            var colID int
            var colOwner string
            // Dynamic query to find the colony in the system matching the order owner
            errCol := db.QueryRow("SELECT id, owner_uuid FROM colonies WHERE system_id=? AND owner_uuid=?", fleet.DestSystem, sellerUUID).Scan(&colID, &colOwner)
            
            if errCol == nil {
                // Execute Trade
                tx, _ := db.Begin()
                success := false
                
                if !isBuy { 
                    // This was a SELL order (Partner is Selling, Fleet is Buying)
                    // Fleet needs Credits, Partner needs Goods.
                    // WAIT: Fleet sent to BUY means Fleet must carry CREDITS.
                    cost := price * qty
                    
                    if fleet.Payload.Credits >= cost {
                        // Check if colony has goods
                        res, _ := tx.Exec(fmt.Sprintf("UPDATE colonies SET %s = %s - ? WHERE id=? AND %s >= ?", item, item, item), qty, colID, qty)
                        if n, _ := res.RowsAffected(); n > 0 {
                            // Transfer Goods to Fleet
                            if fleet.Payload.Resources == nil { fleet.Payload.Resources = make(map[string]int) }
                            fleet.Payload.Resources[item] += qty
                            fleet.Payload.Credits -= cost
                            
                            // Pay the Colony Owner (User)
                            tx.Exec("UPDATE users SET credits = credits + ? WHERE global_uuid=?", cost, colOwner)
                            
                            // Update Fleet
                            fJson, _ := json.Marshal(fleet.Payload)
                            tx.Exec("UPDATE fleets SET payload_json=? WHERE id=?", string(fJson), fleet.ID)
                            
                            success = true
                            InfoLog.Printf("💰 Trade Executed: Fleet %d bought %d %s from %s", fleet.ID, qty, item, colOwner)
                        }
                    }
                } else {
                    // This was a BUY order (Partner wants to Buy, Fleet is Selling)
                    // Fleet needs Goods, Partner needs Credits (User credits usually, or Colony credits? Let's use User credits for simplicity)
                    // Actually, "Does the colony have credits?" usually refers to the User balance.
                    
                    payout := price * qty
                    // Check if fleet has goods
                    if fleet.Payload.Resources[item] >= qty {
                        // Check if Buyer (Colony Owner) has credits
                        var buyerCreds int
                        tx.QueryRow("SELECT credits FROM users WHERE global_uuid=?", colOwner).Scan(&buyerCreds)
                        
                        if buyerCreds >= payout {
                            // Deduct Item from Fleet
                            fleet.Payload.Resources[item] -= qty
                            
                            // Add Item to Colony
                            tx.Exec(fmt.Sprintf("UPDATE colonies SET %s = %s + ? WHERE id=?", item, item), qty, colID)
                            
                            // Transfer Credits: Buyer -> Fleet Owner
                            tx.Exec("UPDATE users SET credits = credits - ? WHERE global_uuid=?", payout, colOwner)
                            tx.Exec("UPDATE users SET credits = credits + ? WHERE global_uuid=?", payout, fleet.OwnerUUID)
                            
                            // Update Fleet
                            fJson, _ := json.Marshal(fleet.Payload)
                            tx.Exec("UPDATE fleets SET payload_json=? WHERE id=?", string(fJson), fleet.ID)
                            
                            success = true
                            InfoLog.Printf("💰 Trade Executed: Fleet %d sold %d %s to %s", fleet.ID, qty, item, colOwner)
                        }
                    }
                }
                
                if success {
                    // Delete the order as fulfilled
                    tx.Exec("DELETE FROM market_orders WHERE order_id=?", fleet.TargetOrderID)
                    tx.Commit()
                } else {
                    tx.Rollback()
                    InfoLog.Printf("⚠️ Trade Failed for Fleet %d (Funds/Goods missing)", fleet.ID)
                }
            }
        }
    }
}

// --- Industry & Happiness ---

func processIndustry(c *Colony, efficiencyMult float64) {
    runRefinery := func(building string, inputName string, inputAmt int, outputName string, outputAmt int, inputStock *int, outputStock *int) {
        if count, ok := c.Buildings[building]; ok && count > 0 {
            totalInput := inputAmt * count
            totalOutput := outputAmt * count
            
            eff := GetEfficiency(c.ID, inputName)
            // Apply Stability Bonus
            adjustedOutput := int(float64(totalOutput) * eff * efficiencyMult) + 1

            if *inputStock >= totalInput {
                *inputStock -= totalInput
                *outputStock = safeAdd(*outputStock, adjustedOutput)
            }
        }
    }

    runRefinery("steel_mill", "iron", 2, "steel", 1, &c.Iron, &c.Steel)
    runRefinery("fuel_synthesizer", "carbon", 5, "fuel", 2, &c.Carbon, &c.Fuel)
    runRefinery("winery", "vegetation", 5, "wine", 1, &c.Vegetation, &c.Wine)
    runRefinery("platinum_refinery", "platinum_ore", 3, "platinum", 1, &c.PlatinumOre, &c.Platinum)
    runRefinery("uranium_enricher", "uranium_ore", 5, "uranium", 1, &c.UraniumOre, &c.Uranium)
    runRefinery("diamond_cutter", "diamond_ore", 4, "diamond", 1, &c.DiamondOre, &c.Diamond)
    runRefinery("breeder_reactor", "uranium", 10, "plutonium", 1, &c.Uranium, &c.Plutonium)
}

func calculateSatisfaction(supply, demand int) float64 {
    if demand <= 0 { return 1.0 }
    if supply >= demand { return 1.0 }
    return float64(supply) / float64(demand)
}

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

func reportGrievance(offender, victim string, damage int) {
	db.Exec("INSERT INTO grievances (offender_uuid, victim_uuid, damage_amount, tick) VALUES (?, ?, ?, ?)",
		offender, victim, damage, atomic.LoadInt64(&CurrentTick))
}

func resolveSectorConflict(currentTick int64) {
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

func tickWorld() {
	stateLock.Lock()
	defer stateLock.Unlock()

	pruneTransactionCache()
	current := atomic.AddInt64(&CurrentTick, 1)
	db.Exec("INSERT INTO transaction_log (tick, action_type) VALUES (?, 'TICK')", current)

    // Clean Expired Market Orders
    if current % 100 == 0 {
        db.Exec("DELETE FROM market_orders WHERE expires_tick < ?", current)
    }

    // Process Fleets
	fRows, _ := db.Query("SELECT id, dest_system, status, hull_class, modules_json, owner_uuid, payload_json, target_order_id FROM fleets WHERE arrival_tick <= ? AND status='TRANSIT'", current)
	defer fRows.Close()

	for fRows.Next() {
		var f Fleet
		var modJson, plJson string
        var tOrder sql.NullString
		fRows.Scan(&f.ID, &f.DestSystem, &f.Status, &f.HullClass, &modJson, &f.OwnerUUID, &plJson, &tOrder)
		json.Unmarshal([]byte(modJson), &f.Modules)
        if plJson != "" { json.Unmarshal([]byte(plJson), &f.Payload) }
        if tOrder.Valid { f.TargetOrderID = tOrder.String }

		resolveDeepSpaceArrival(f)
		db.Exec("UPDATE fleets SET status='ORBIT' WHERE id=?", f.ID)
	}

	resolveSectorConflict(current)

    // Process Colonies (Happiness & Industry)
	rows, err := db.Query(`SELECT id, buildings_json, policies_json, pop_laborers, pop_specialists, pop_elites, 
	                       food, water, carbon, gold, fuel, steel, wine, vegetation, stability_current, stability_target, iron,
                           uranium, uranium_ore, platinum, platinum_ore, diamond, diamond_ore, plutonium
	                       FROM colonies`)
	if err != nil { return }
	defer rows.Close()

	type ColUpdate struct {
		ID                                                      int
		Food, Water, Iron, Carbon, Gold, Fuel, Steel, Wine, Veg int
        Uranium, UraniumOre, Platinum, PlatinumOre, Diamond, DiamondOre, Plutonium int
		PopLab, PopSpec, PopElite                               int
		Stability, Target                                       float64
	}
	var updates []ColUpdate

	for rows.Next() {
		var c Colony
		var bJson, pJson string
		rows.Scan(&c.ID, &bJson, &pJson, &c.PopLaborers, &c.PopSpecialists, &c.PopElites,
			&c.Food, &c.Water, &c.Carbon, &c.Gold, &c.Fuel, &c.Steel, &c.Wine, &c.Vegetation,
			&c.StabilityCurrent, &c.StabilityTarget, &c.Iron,
            &c.Uranium, &c.UraniumOre, &c.Platinum, &c.PlatinumOre, &c.Diamond, &c.DiamondOre, &c.Plutonium)
		json.Unmarshal([]byte(bJson), &c.Buildings)
		
		c.Policies = make(map[string]bool)
		if pJson != "" { json.Unmarshal([]byte(pJson), &c.Policies) }

        // --- 1. Base Extraction (Laborers Work) ---
        // Efficiency multiplier based on current stability (High stability = Bonus)
        effMult := 1.0
        if c.StabilityCurrent >= 90.0 { effMult = 1.10 }
        if c.StabilityCurrent <= 20.0 { effMult = 0.0 } // Strikes!

		foodEff := GetEfficiency(c.ID, "food") * effMult
        
		c.Food = safeAdd(c.Food, int(float64(c.Buildings["farm"]*5)*foodEff))
		c.Water = safeAdd(c.Water, int(float64(c.Buildings["well"]*5)*foodEff))
        c.UraniumOre = safeAdd(c.UraniumOre, int(float64(c.Buildings["uranium_mine"]*2)*GetEfficiency(c.ID, "uranium_ore")*effMult))
        c.PlatinumOre = safeAdd(c.PlatinumOre, int(float64(c.Buildings["platinum_mine"]*2)*GetEfficiency(c.ID, "platinum_ore")*effMult))
        c.DiamondOre = safeAdd(c.DiamondOre, int(float64(c.Buildings["diamond_mine"]*2)*GetEfficiency(c.ID, "diamond_ore")*effMult))
        c.Carbon = safeAdd(c.Carbon, int(float64(c.Buildings["carbon_extractor"]*10)*GetEfficiency(c.ID, "carbon")*effMult))
        c.Iron = safeAdd(c.Iron, int(float64(c.Buildings["iron_mine"]*10)*GetEfficiency(c.ID, "iron")*effMult))

        // --- 2. Industry (Specialists Work) ---
        // If specialists are unhappy (Stability < 40), industry slows down
        indMult := 1.0
        if c.StabilityCurrent < 40 { indMult = 0.5 }
		processIndustry(&c, indMult)

        // --- 3. Stratified Consumption & Happiness ---
        
        // Laborers (Survival: Food, Water)
        labNeedFood := c.PopLaborers / 10 // 1 unit per 10 ppl
        labNeedWater := c.PopLaborers / 10
        satLabFood := 1.0
        satLabWater := 1.0
        
        if c.Food >= labNeedFood { c.Food -= labNeedFood } else { satLabFood = calculateSatisfaction(c.Food, labNeedFood); c.Food = 0 }
        if c.Water >= labNeedWater { c.Water -= labNeedWater } else { satLabWater = calculateSatisfaction(c.Water, labNeedWater); c.Water = 0 }
        
        satLabor := (satLabFood + satLabWater) / 2.0

        // Specialists (Comfort: Steel, Fuel)
        specNeedSteel := c.PopSpecialists / 20
        specNeedFuel := c.PopSpecialists / 20
        satSpecSteel := 1.0
        satSpecFuel := 1.0
        
        if c.Steel >= specNeedSteel { c.Steel -= specNeedSteel } else { satSpecSteel = calculateSatisfaction(c.Steel, specNeedSteel); c.Steel = 0 }
        if c.Fuel >= specNeedFuel { c.Fuel -= specNeedFuel } else { satSpecFuel = calculateSatisfaction(c.Fuel, specNeedFuel); c.Fuel = 0 }
        
        satSpec := (satSpecSteel + satSpecFuel) / 2.0
        if c.PopSpecialists == 0 { satSpec = 1.0 }

        // Elites (Luxury: Wine, Platinum)
        eliteNeedWine := c.PopElites / 5
        eliteNeedPlat := c.PopElites / 10
        satEliteWine := 1.0
        satElitePlat := 1.0
        
        if c.Wine >= eliteNeedWine { c.Wine -= eliteNeedWine } else { satEliteWine = calculateSatisfaction(c.Wine, eliteNeedWine); c.Wine = 0 }
        if c.Platinum >= eliteNeedPlat { c.Platinum -= eliteNeedPlat } else { satElitePlat = calculateSatisfaction(c.Platinum, eliteNeedPlat); c.Platinum = 0 }
        
        satElite := (satEliteWine + satElitePlat) / 2.0
        if c.PopElites == 0 { satElite = 1.0 }

        // --- 4. Weighted Stability ---
        // Formula: (Labor * 0.5) + (Specialist * 0.3) + (Elite * 0.2)
        // Result is 0.0 to 1.0, scaled to 100.
        
        weightedSat := (satLabor * 0.5) + (satSpec * 0.3) + (satElite * 0.2)
        c.StabilityTarget = weightedSat * 100.0
        
        // Starvation Penalty (Laborers dying)
        if satLabor < 0.5 {
            deathToll := int(float64(c.PopLaborers) * 0.05) // 5% die
            c.PopLaborers -= deathToll
            c.StabilityTarget -= 20.0 // Riots
        }

		diff := c.StabilityTarget - c.StabilityCurrent
		c.StabilityCurrent += diff * 0.1 // Move 10% towards target per tick
        if c.StabilityCurrent < 0 { c.StabilityCurrent = 0 }
        if c.StabilityCurrent > 100 { c.StabilityCurrent = 100 }

		updates = append(updates, ColUpdate{
			ID: c.ID,
			Food: c.Food, Water: c.Water, Iron: c.Iron, Carbon: c.Carbon,
			Gold: c.Gold, Fuel: c.Fuel,
			Steel: c.Steel, Wine: c.Wine, Veg: c.Vegetation,
            Uranium: c.Uranium, UraniumOre: c.UraniumOre, 
            Platinum: c.Platinum, PlatinumOre: c.PlatinumOre, 
            Diamond: c.Diamond, DiamondOre: c.DiamondOre,
            Plutonium: c.Plutonium,
			PopLab: c.PopLaborers, PopSpec: c.PopSpecialists, PopElite: c.PopElites,
			Stability: c.StabilityCurrent, Target: c.StabilityTarget,
		})
	}

	if len(updates) > 0 {
		tx, _ := db.Begin()
		stmt, _ := tx.Prepare(`UPDATE colonies SET 
			food=?, water=?, iron=?, carbon=?, gold=?, fuel=?, 
			steel=?, wine=?, vegetation=?,
            uranium=?, uranium_ore=?, platinum=?, platinum_ore=?, diamond=?, diamond_ore=?, plutonium=?,
			pop_laborers=?, pop_specialists=?, pop_elites=?, 
			stability_current=?, stability_target=? 
			WHERE id=?`)
		for _, u := range updates {
			stmt.Exec(u.Food, u.Water, u.Iron, u.Carbon, u.Gold, u.Fuel,
				u.Steel, u.Wine, u.Veg,
                u.Uranium, u.UraniumOre, u.Platinum, u.PlatinumOre, u.Diamond, u.DiamondOre, u.Plutonium,
				u.PopLab, u.PopSpec, u.PopElite,
				u.Stability, u.Target, u.ID)
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
	ticker := time.NewTicker(60 * time.Second)
	for {
		<-ticker.C
		offset := CalculateOffset()
		if offset > 0 { time.Sleep(offset) }
		tickWorld()
	}
}
