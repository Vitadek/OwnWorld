package main

import (
	"crypto/ed25519"
	"time"
)

type Peer struct {
	UUID        string
	Url         string
	PublicKey   ed25519.PublicKey
	GenesisHash string
	PeerCount   int
	CurrentTick int64
	LastTick    int64
	LastSeen    time.Time
	Reputation  int
	Relation    int // 0:Neutral, 1:Federated, 2:Hostile
        Location    []int // [x, y, z]
}

type HandshakeRequest struct {
	UUID        string `json:"uuid"`
	GenesisHash string `json:"genesis_hash"`
	PublicKey   string `json:"public_key"`
	Address     string `json:"address"`
        Location    []int  `json:"location"` // My Location
}
type HandshakeResponse struct {
	Status   string `json:"status"`
	UUID     string `json:"uuid"`
	Location []int  `json:"location"` // Seed's Location
}

type TransactionRequest struct {
	UUID      string `json:"uuid"`
	Tick      int64  `json:"tick"`
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

type Colony struct {
	ID            int            `json:"id"`
	SystemID      string         `json:"system_id"`
	OwnerUUID     string         `json:"owner_uuid"`
	Name          string         `json:"name"`
	Buildings     map[string]int `json:"buildings"`
	
	// Population
	PopLaborers   int `json:"pop_laborers"`
	PopSpecialists int `json:"pop_specialists"`
	PopElites     int `json:"pop_elites"`

	// Resources
	Food       int `json:"food"`
	Water      int `json:"water"`
	Iron       int `json:"iron"`
	Carbon     int `json:"carbon"`
	Gold       int `json:"gold"`
	Uranium    int `json:"uranium"`
	Platinum   int `json:"platinum"` // Added
	Diamond    int `json:"diamond"`  // Added
	Vegetation int `json:"vegetation"` // Added
	Oxygen     int `json:"oxygen"`     // Added
	Fuel       int `json:"fuel"`
	
	// Stats
	StabilityCurrent float64 `json:"stability_current"`
	StabilityTarget  float64 `json:"stability_target"`
	MartialLaw       bool    `json:"martial_law"`
}

type Fleet struct {
	ID           int    `json:"id"`
	OwnerUUID    string `json:"owner_uuid"`
	Status       string `json:"status"`
	OriginSystem string `json:"origin_system"`
	DestSystem   string `json:"dest_system"`
	ArrivalTick  int64  `json:"arrival_tick"`
	Fuel         int    `json:"fuel"`
	
	ArkShip    int `json:"ark_ship"`
	Fighters   int `json:"fighters"`
	Frigates   int `json:"frigates"`
}
type HeartbeatRequest struct {
	UUID      string `json:"uuid"`
	Tick      int64  `json:"tick"`
	PeerCount int    `json:"peer_count"`
	GenHash   string `json:"gen_hash"`
	Signature string `json:"sig"` // Hex encoded Ed25519 signature
}
