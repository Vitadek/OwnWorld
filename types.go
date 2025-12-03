package main

import (
	"crypto/ed25519"
	"time"
)

// --- Database Models ---

type User struct {
	ID                         int    `json:"id"`
	GlobalUUID                 string `json:"global_uuid"`
	Username                   string `json:"username"`
	PasswordHash               string `json:"password_hash"`
	Credits                    int    `json:"credits"`
	IsLocal                    bool   `json:"is_local"`
	Ed25519EncryptedPrivateKey []byte `json:"ed25519_encrypted_private_key"`
}

type SolarSystem struct {
	ID        string  `json:"id"`
	X         int     `json:"x"`
	Y         int     `json:"y"`
	Z         int     `json:"z"`
	StarType  string  `json:"star_type"`
	OwnerUUID string  `json:"owner_uuid"`
	TaxRate   float64 `json:"tax_rate"`
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

	// Resources (Phase 1.2 Upgrade)
	Iron       int `json:"iron"`
	Carbon     int `json:"carbon"`
	Water      int `json:"water"`
	Gold       int `json:"gold"`
	Platinum   int `json:"platinum"`
	Uranium    int `json:"uranium"`
	Diamond    int `json:"diamond"`
	Vegetation int `json:"vegetation"`
	Oxygen     int `json:"oxygen"` // Added per Protocol

	// Stats
	PopLaborers      int     `json:"pop_laborers"`
	PopSpecialists   int     `json:"pop_specialists"`
	PopElites        int     `json:"pop_elites"`
	StabilityCurrent float64 `json:"stability_current"`
	StabilityTarget  float64 `json:"stability_target"`
	MartialLaw       bool    `json:"martial_law"`
}

type Fleet struct {
	ID            int    `json:"id"`
	OwnerUUID     string `json:"owner_uuid"`
	Status        string `json:"status"` // ORBIT, TRANSIT
	Fuel          int    `json:"fuel"`
	OriginSystem  string `json:"origin_system"`
	DestSystem    string `json:"dest_system"`
	DepartureTick int    `json:"departure_tick"`
	ArrivalTick   int    `json:"arrival_tick"`
	StartCoords   string `json:"start_coords"`
	DestCoords    string `json:"dest_coords"`

	// Composition
	ArkShip  int `json:"ark_ship"`
	Fighters int `json:"fighters"`
	Frigates int `json:"frigates"`
	Haulers  int `json:"haulers"`
}

// --- Network Models ---

type Heartbeat struct {
	UUID      string `json:"uuid"`
	Tick      int    `json:"tick"`
	Timestamp int64  `json:"timestamp"`
	Signature []byte `json:"signature"`
}

type HandshakeRequest struct {
	UUID        string `json:"uuid"`
	GenesisHash string `json:"genesis_hash"`
	PublicKey   string `json:"public_key"`
	Address     string `json:"address"`
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

// --- Consensus Models ---

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
