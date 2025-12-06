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
	Reputation  float64 
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
	ParentID      int            `json:"parent_id"` // Track heritage
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
	
	// New Industry Resources
	Steel      int `json:"steel"`
	Wine       int `json:"wine"`
	
	StabilityCurrent float64 `json:"stability_current"`
	StabilityTarget  float64 `json:"stability_target"`
	MartialLaw       bool    `json:"martial_law"`
}

// Payload for Colonization
type FleetPayload struct {
    PopLaborers   int            `json:"laborers"`
    PopSpecialists int           `json:"specialists"`
    Resources     map[string]int `json:"resources"`   // Food, Iron, etc.
    CultureBonus  float64        `json:"culture"`     // Inherited Stability
}

// Updated Fleet Structure
type Fleet struct {
	ID           int      `json:"id"`
	OwnerUUID    string   `json:"owner_uuid"`
	Status       string   `json:"status"`
	OriginSystem string   `json:"origin_system"`
	DestSystem   string   `json:"dest_system"`
	ArrivalTick  int64    `json:"arrival_tick"`
	Fuel         int      `json:"fuel"`
	
	HullClass    string   `json:"hull_class"`
	Modules      []string `json:"modules"`
	
	// The Seed Payload
	Payload      FleetPayload `json:"payload"`
	
	// Legacy
	ArkShip    int `json:"ark_ship"`
	Fighters   int `json:"fighters"`
	Frigates   int `json:"frigates"`
	Haulers    int `json:"haulers"`
}

type ShipHull struct {
    Class        string 
    EngineSlots  int
    WeaponSlots  int
    SpecialSlots int 
}

type HeartbeatRequest struct {
	UUID      string `json:"uuid"`
	Tick      int64  `json:"tick"`
	PeerCount int    `json:"peer_count"`
	GenHash   string `json:"gen_hash"`
	Signature string `json:"sig"` 
}

type GrievanceReport struct {
    OffenderUUID string `json:"offender"`
    Damage       int    `json:"damage"`
    Proof        string `json:"proof"`
}

type Grievance struct {
    OffenderUUID string
    VictimUUID   string
    DamageDone   int
    Signature    []byte 
}
