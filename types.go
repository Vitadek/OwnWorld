package main

import (
	"crypto/ed25519"
	"time"
)

type User struct {
	ID            int    `json:"id"`
	GlobalUUID    string `json:"global_uuid"`
	Username      string `json:"username"`
	PasswordHash  string `json:"password_hash"`
	Credits       int    `json:"credits"`
	IsLocal       bool   `json:"is_local"`
	Ed25519PubKey string `json:"ed25519_pubkey"`
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

	PopLaborers    int `json:"pop_laborers"`
	PopSpecialists int `json:"pop_specialists"`
	PopElites      int `json:"pop_elites"`

	Food       int `json:"food"`
	Water      int `json:"water"`
	Iron       int `json:"iron"`
	Carbon     int `json:"carbon"`
	Gold       int `json:"gold"`
	Platinum   int `json:"platinum"`
	Uranium    int `json:"uranium"`
	Diamond    int `json:"diamond"`
	Vegetation int `json:"vegetation"`
	Oxygen     int `json:"oxygen"`
	Fuel       int `json:"fuel"`

	StabilityCurrent float64 `json:"stability_current"`
	StabilityTarget  float64 `json:"stability_target"`
	MartialLaw       bool    `json:"martial_law"`
}

type Fleet struct {
	ID            int    `json:"id"`
	OwnerUUID     string `json:"owner_uuid"`
	Status        string `json:"status"`
	OriginSystem  string `json:"origin_system"`
	DestSystem    string `json:"dest_system"`
	DepartureTick int    `json:"departure_tick"`
	ArrivalTick   int    `json:"arrival_tick"`
	ArkShip       int    `json:"ark_ship"`
	Fighters      int    `json:"fighters"`
	Frigates      int    `json:"frigates"`
	Haulers       int    `json:"haulers"`
	Fuel          int    `json:"fuel"`
}

type HandshakeRequest struct {
	UUID        string `json:"uuid"`
	GenesisHash string `json:"genesis_hash"`
	PublicKey   string `json:"public_key"`
	Address     string `json:"address"`
}

type TransactionRequest struct {
	UUID      string `json:"uuid"`
	Tick      int    `json:"tick"`
	Type      string `json:"type"`
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

type LedgerPayload struct {
	UUID      string      `json:"uuid"`
	Tick      int         `json:"tick"`
	PeerCount int         `json:"peer_count"`
	Entry     LedgerEntry `json:"entry"`
}

type LedgerEntry struct {
	Tick      int    `json:"tick"`
	FinalHash string `json:"final_hash"`
	PrevHash  string `json:"prev_hash"`
}

type GenesisState struct {
	Seed      string `json:"seed"`
	Timestamp int64  `json:"timestamp"`
	PubKey    string `json:"pub_key"`
}
