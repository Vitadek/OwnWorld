package main

import (
	"crypto/ed25519"
	"bytes"
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
	MinTickDuration = 4000
	MaxTickDuration = 6000
)

var (
	// Infrastructure
	db       *sql.DB
	InfoLog  *log.Logger
	ErrorLog *log.Logger

	// Identity
	ServerUUID  string
	GenesisHash string
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
	TickDuration  int64 = 5000
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
	stateLock sync.Mutex // Added this (Fixes simulation.go error)
	
	// Rate Limiting
	ipLimiters = make(map[string]*rate.Limiter)
	ipLock     sync.Mutex

	bufferPool = sync.Pool{
        New: func() interface{} {
            return new(bytes.Buffer)
        },
    }
)

// --- Game Constants ---
var UnitCosts = map[string]map[string]int{
	"ark_ship": {"iron": 5000, "food": 5000, "fuel": 500, "pop_laborers": 100},
	"fighter":  {"iron": 500, "fuel": 50, "pop_laborers": 1},
	"frigate":  {"iron": 2000, "carbon": 500, "gold": 50, "pop_specialists": 5},
	"scout":    {"iron": 100, "fuel": 50},
}

var BuildingCosts = map[string]map[string]int{
	"farm":            {"iron": 10},
	"well":            {"iron": 10},
	"iron_mine":       {"food": 500},
	"shipyard":        {"iron": 2000, "carbon": 500},
	"urban_housing":   {"iron": 50},
	"pilot_academy":   {"iron": 1000, "gold": 100},
	"financial_center": {"iron": 5000, "gold": 1000},
}
