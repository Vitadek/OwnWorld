package main

import (
	"crypto/ed25519"
	"time"
)

// --- Domain Models (pkg/types) ---

type User struct {
	ID                         int    `json:"id"`
	GlobalUUID                 string `json:"global_uuid"` // Unique across Federation
	Username                   string `json:"username"`
	PasswordHash               string `json:"password_hash"`
	Credits                    int    `json:"credits"`
	IsLocal                    bool   `json:"is_local"`
	Ed25519EncryptedPrivateKey []byte `json:"ed25519_encrypted_private_key"`
}

type SolarSystem struct {
	ID          string  `json:"id"`
	X           int     `json:"x"`
	Y           int     `json:"y"`
	Z           int     `json:"z"`
	StarType    string  `json:"star_type"`
	OwnerUUID   string  `json:"owner_uuid"`
	TaxRate     float64 `json:"tax_rate"`
	IsFederated bool    `json:"is_federated"`
}

type Planet struct {
	ID             int    `json:"id"`
	SystemID       string `json:"system_id"`
	EfficiencySeed string `json:"efficiency_seed"`
	Type           string `json:"type"`
}

type Colony struct {
	ID            int            `json:"id"`
	SystemID      string         `json:"system_id"`
	OwnerUUID     string         `json:"owner_uuid"`
	Name          string         `json:"name"`
	BuildingsJSON string         `json:"buildings_json"`
	Buildings     map[string]int `json:"buildings,omitempty"`

	// Tier 1 Resources (Basic Construction)
	Iron     int `json:"iron"`
	Carbon   int `json:"carbon"`
	Gold     int `json:"gold"`
	Uranium  int `json:"uranium"`
	Platinum int `json:"platinum"`
	Diamond  int `json:"diamond"`

	// Tier 2 Resources (Sustenance & Atmosphere)
	Food       int `json:"food"` // Merged Vegetation/Food
	Water      int `json:"water"`
	Oxygen     int `json:"oxygen"`
	Vegetation int `json:"vegetation"` // Raw input for food

	// Society & Strata
	PopLaborers      int     `json:"pop_laborers"`
	PopSpecialists   int     `json:"pop_specialists"`
	PopElites        int     `json:"pop_elites"`
	StabilityCurrent float64 `json:"stability_current"`
	StabilityTarget  float64 `json:"stability_target"`
	CrimeRate        float64 `json:"crime_rate"`
	MartialLaw       bool    `json:"martial_law"`
}

type Fleet struct {
	ID            int                    `json:"id"`
	OwnerUUID     string                 `json:"owner_uuid"`
	Status        string                 `json:"status"` // ORBIT, TRANSIT
	Fuel          int                    `json:"fuel"`
	OriginSystem  string                 `json:"origin_system"`
	DestSystem    string                 `json:"dest_system"`
	DepartureTick int                    `json:"departure_tick"`
	ArrivalTick   int                    `json:"arrival_tick"`
	StartCoords   map[string]interface{} `json:"start_coords"` // JSON
	DestCoords    map[string]interface{} `json:"dest_coords"`  // JSON

	// Composition
	ArkShip  int `json:"ark_ship"`
	Fighters int `json:"fighters"`
	Frigates int `json:"frigates"`
	Haulers  int `json:"haulers"`
}

// --- Event Sourcing (pkg/core) ---

type TransactionLog struct {
	ID         int    `json:"id"`
	Tick       int    `json:"tick"`
	ActionType string `json:"action_type"`
	Payload    []byte `json:"payload"`
}

// --- Network Models (pkg/federation) ---

type HandshakeRequest struct {
	UUID        string `json:"uuid"`
	GenesisHash string `json:"genesis_hash"`
	PublicKey   string `json:"public_key"`
	Address     string `json:"address"`
}

// TransactionRequest now requires strict Signature enforcement
type TransactionRequest struct {
	UUID      string `json:"uuid"`
	Tick      int    `json:"tick"`
	Type      string `json:"type"` // FLEET_ARRIVAL, MARKET_TRADE
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

type Peer struct {
	UUID      string            `json:"uuid"`
	Address   string            `json:"address"`
	LastTick  int               `json:"last_tick"`
	LastHash  string            `json:"last_hash"`
	LastSeen  time.Time         `json:"last_seen"`
	PublicKey ed25519.PublicKey `json:"-"`
	Status    string            `json:"status"`
}

type LedgerEntry struct {
	Tick      int    `json:"tick"`
	Timestamp int64  `json:"timestamp"`
	PrevHash  string `json:"prev_hash"`
	FinalHash string `json:"final_hash"`
}

type LedgerPayload struct {
	UUID      string      `json:"uuid"`
	Tick      int         `json:"tick"`
	StampHash string      `json:"stamp_hash"`
	PeerCount int         `json:"peer_count"`
	Entry     LedgerEntry `json:"entry"`
}

type GenesisState struct {
	Seed      string `json:"seed"`
	Timestamp int64  `json:"timestamp"`
	PubKey    string `json:"pub_key"`
}
