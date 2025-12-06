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
	Reputation  float64 // Changed to float64 for EigenTrust precision
	Relation    int // 0:Neutral, 1:Federated, 2:Hostile
    Location    []int // [x, y, z]
}

type HandshakeRequest struct {
	UUID        string `json:"uuid"`
	GenesisHash string `json:"genesis_hash"`
	PublicKey   string `json:"public_key"`
	Address     string `json:"address"`
    Location    []int  `json:"location"` 
}
type HandshakeResponse struct {
    Status   string `json:"status"`
    UUID     string `json:"uuid"`
    Location []int  `json:"location"` 
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
	
	PopLaborers   int `json:"pop_laborers"`
	PopSpecialists int `json:"pop_specialists"`
	PopElites     int `json:"pop_elites"`

	Food       int `json:"food"`
	Water      int `json:"water"`
	Iron       int `json:"iron"`
	Carbon     int `json:"carbon"`
	Gold       int `json:"gold"`
	Uranium    int `json:"uranium"`
	Platinum   int `json:"platinum"`
	Diamond    int `json:"diamond"`
	Vegetation int `json:"vegetation"`
	Oxygen     int `json:"oxygen"`
	Fuel       int `json:"fuel"`
	
	StabilityCurrent float64 `json:"stability_current"`
	StabilityTarget  float64 `json:"stability_target"`
	MartialLaw       bool    `json:"martial_law"`
}

// Updated Fleet Structure for Modules
type Fleet struct {
	ID           int      `json:"id"`
	OwnerUUID    string   `json:"owner_uuid"`
	Status       string   `json:"status"`
	OriginSystem string   `json:"origin_system"`
	DestSystem   string   `json:"dest_system"`
	ArrivalTick  int64    `json:"arrival_tick"`
	Fuel         int      `json:"fuel"`
	
	// New Modular System
	HullClass    string   `json:"hull_class"` // "Fighter", "Bomber", "Frigate", "Colonizer"
	Modules      []string `json:"modules"`    // ["warp_drive", "shield_gen"]
	
	// Legacy fields (for backward compatibility if needed, else 0)
	ArkShip    int `json:"ark_ship"`
	Fighters   int `json:"fighters"`
	Frigates   int `json:"frigates"`
	Haulers    int `json:"haulers"`
}

// New: Ship Hull Definition for Validation
type ShipHull struct {
    Class        string 
    EngineSlots  int
    WeaponSlots  int
    SpecialSlots int // Colonizer/BombBay
}

type HeartbeatRequest struct {
	UUID      string `json:"uuid"`
	Tick      int64  `json:"tick"`
	PeerCount int    `json:"peer_count"`
	GenHash   string `json:"gen_hash"`
	Signature string `json:"sig"` 
}

// Updated Grievance Type for EigenTrust
type GrievanceReport struct {
    OffenderUUID string `json:"offender"`
    Damage       int    `json:"damage"`
    Proof        string `json:"proof"` // Signature of the combat log
}

// For EigenTrust input
type Grievance struct {
    OffenderUUID string
    VictimUUID   string
    DamageDone   int
    Signature    []byte 
}
