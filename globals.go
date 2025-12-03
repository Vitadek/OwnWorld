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

var (
	// Database
	dbFile = "./data/ownworld.db"
	db     *sql.DB

	// Identity
	ServerUUID string
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey

	// Networking
	peers            = make(map[string]*Peer)
	peerLock         sync.RWMutex
	immigrationQueue = make(chan HandshakeRequest, 50)
	ipLimiters       = make(map[string]*rate.Limiter)
	ipLock           sync.Mutex
	mapSnapshot      atomic.Value

	// Time & Consensus
	CurrentTick  int           = 0
	PreviousHash string        = "GENESIS"
	IsLeader     bool          = true
	LeaderUUID   string        = ""
	PhaseOffset  time.Duration = 0
	stateLock    sync.Mutex

	// Configuration
	Config struct {
		CommandControl bool   // If false, disable User APIs
		PeeringMode    string // "promiscuous" or "strict"
	}

	// Logging
	InfoLog  *log.Logger
	ErrorLog *log.Logger
)

// Memory Pool
var bufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// Game Configuration
var BuildingCosts = map[string]map[string]int{
	"farm":          {"iron": 10},
	"well":          {"iron": 10},
	"iron_mine":     {"carbon": 50},
	"gold_mine":     {"iron": 500, "carbon": 100},
	"urban_housing": {"iron": 100, "carbon": 100},
}
