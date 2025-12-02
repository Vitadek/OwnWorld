package game

import (
	"fmt"
	"math"
	"strings"
	"../core"
	"../types"
)

// --- World Generation ---

// Deterministic Efficiency based on Planet ID + Mineral Name
func GetEfficiency(planetID int, resource string, seed string) float64 {
	// Hash Inputs
	hashStr := core.Hash([]byte(fmt.Sprintf("%d-%s-%s", planetID, resource, seed)))
	
	// Convert first byte to 0-255 int
	val := int(hashStr[0]) 

	// Normalize to 0.1 (Poor) to 2.5 (Rich)
	// Bias slightly towards 1.0
	return 0.1 + (float64(val) / 100.0)
}

// --- Physics & Logistics ---

func CalculateFuelCost(origin, target []int, mass int, targetUUID string, localUUID string, isPeer bool) int {
	// 3D Distance
	dist := 0.0
	for i := 0; i < 3; i++ {
		dist += math.Pow(float64(origin[i]-target[i]), 2)
	}
	dist = math.Sqrt(dist)

	// The Geography Multiplier
	multiplier := 10.0 // Default: Unknown Universe (Deep Space)

	if targetUUID == localUUID {
		multiplier = 1.0 // Same Solar System
	} else if isPeer {
		multiplier = 2.5 // Federated (Hyperlane)
	}

	return int(dist * float64(mass) * multiplier)
}

// --- Economy (The Bank) ---

func CalculateBurnPayout(resource string, planetID int, amount int, serverSeed string) int {
	basePrice := map[string]float64{
		"iron": 1.0, "carbon": 1.0, "gold": 100.0,
		"uranium": 50.0, "diamond": 200.0,
	}

	price, ok := basePrice[resource]
	if !ok { return 0 }

	// Scarcity Logic:
	// If efficiency is High (2.0), Payout is Low (0.5)
	// If efficiency is Low (0.5), Payout is High (2.0)
	eff := GetEfficiency(planetID, resource, serverSeed)
	scarcityMod := 1.0 / eff

	return int(float64(amount) * price * scarcityMod)
}

// --- Anti-Snowball (Evening Factor) ---

func CalculateCorruption(totalColonies int) float64 {
	if totalColonies <= 1 { return 1.0 }
	// Logarithmic decay of efficiency
	// 10 Colonies = ~0.3 Efficiency
	return 1.0 / math.Log2(float64(totalColonies)+1)
}
