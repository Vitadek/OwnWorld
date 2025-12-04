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
}

type HandshakeRequest struct {
	UUID        string `json:"uuid"`
	GenesisHash string `json:"genesis_hash"`
	PublicKey   string `json:"public_key"`
	Address     string `json:"address"`
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
	Fuel       int `json:"fuel"`
	
	// Stats
	StabilityCurrent float64 `json:"stability_current"`
	StabilityTarget  float64 `json:"stability_target"`
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
