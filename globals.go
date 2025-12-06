package main

import (
	"bytes"
	"crypto/ed25519"
	"database/sql"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// --- Configuration ---
const (
	DBPath          = "./data/ownworld.db"
    // Slowed down ticks to 1 minute
	MinTickDuration = 60000 
	MaxTickDuration = 65000 
)

var (
	// Infrastructure
	db       *sql.DB
	InfoLog  *log.Logger
	ErrorLog *log.Logger
	DebugLog *log.Logger

	// Identity
	ServerUUID  string
	ServerLoc   []int // [x, y, z]
	GenesisHash string
	TargetGenesisHash string 

	PrivateKey  ed25519.PrivateKey
	PublicKey   ed25519.PublicKey

	// Config
	Config struct {
		CommandControl bool
		PeeringMode    string
	}

	// Consensus State
	Peers         = make(map[string]*Peer)
	peerLock      sync.RWMutex
	CurrentTick   int64 = 0
	PreviousHash  string = "GENESIS"
    // Default tick duration
	TickDuration  int64 = 60000 
	MyRank        int   = 0
	TotalPeers    int   = 1
	PhaseOffset   time.Duration = 0
	
	// Leader
	IsLeader   bool = true
	LeaderUUID string

	// Caches & Queues
	mapSnapshot      atomic.Value 
	immigrationQueue = make(chan HandshakeRequest, 50)
	
	// Locking
	stateLock sync.Mutex
	
	// Buffers
	bufferPool = sync.Pool{
		New: func() interface{} { return new(bytes.Buffer) },
	}
	
	// Rate Limiting
	ipLimiters = make(map[string]*rate.Limiter)
	ipLock     sync.Mutex

	// Replay Protection
	SeenCurrent  = make(map[string]bool)
	SeenPrevious = make(map[string]bool)
	SeenTxLock   sync.Mutex
)

func pruneTransactionCache() {
	SeenTxLock.Lock()
	defer SeenTxLock.Unlock()
	SeenPrevious = SeenCurrent
	SeenCurrent = make(map[string]bool)
}

// Game Constants
var UnitCosts = map[string]map[string]int{
	// Legacy Units kept for reference, but construction now uses HullSpecs
	"ark_ship": {"iron": 5000, "food": 5000, "fuel": 500, "pop_laborers": 100},
}

var BuildingCosts = map[string]map[string]int{
    // Basics
	"farm":            {"iron": 10},
	"well":            {"iron": 10},
	"urban_housing":   {"iron": 50},
    
    // Mines (Raw Material Extraction)
	"iron_mine":       {"food": 500},
    "carbon_extractor": {"iron": 100, "food": 200},
    "platinum_mine":   {"iron": 1000, "food": 1000},
    "uranium_mine":    {"iron": 2000, "steel": 500},
    "diamond_mine":    {"iron": 1500, "food": 1000},

    // Processing & Refining (The Scaffolding)
	"shipyard":           {"iron": 2000, "carbon": 500},
    "steel_mill":         {"iron": 500, "carbon": 500},
    "fuel_synthesizer":   {"iron": 1000, "steel": 200}, // Refines Carbon -> Fuel
    "platinum_refinery":  {"steel": 1000, "carbon": 1000},
    "uranium_enricher":   {"steel": 2000, "platinum": 100},
    "diamond_cutter":     {"steel": 500, "iron": 500},
    "breeder_reactor":    {"steel": 5000, "uranium": 500, "platinum": 200}, // Creates Plutonium

    // Advanced
    "winery":             {"iron": 100, "gold": 50},
	"pilot_academy":      {"iron": 1000, "gold": 100},
	"financial_center":   {"iron": 5000, "gold": 1000},
}

// HARD-CODED CLASSES (The "Physics" of the Hull)
var HullRegistry = map[string]ShipHull{
    "Fighter":        {Class: "Fighter", EngineSlots: 1, WeaponSlots: 4, SpecialSlots: 0},
    "SpeedyFighter":  {Class: "SpeedyFighter", EngineSlots: 2, WeaponSlots: 2, SpecialSlots: 0},
    "Bomber":         {Class: "Bomber", EngineSlots: 1, WeaponSlots: 0, SpecialSlots: 1}, // Special = BombBay
    "Frigate":        {Class: "Frigate", EngineSlots: 4, WeaponSlots: 0, SpecialSlots: 0}, // Pure Mover
    "Colonizer":      {Class: "Colonizer", EngineSlots: 2, WeaponSlots: 0, SpecialSlots: 1}, // Special = Ark
}

// New: Module Costs
var ModuleCosts = map[string]int{
    "booster":       100,
    "propeller":     50,
    "warp_drive":    1000,
    "laser":         200,
    "railgun":       300,
    "bomb_bay":      500,
    "colony_kit":    5000,
}
