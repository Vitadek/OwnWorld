package types

import "time"

// --- Geographic & Political ---

type SolarSystem struct {
	ID          string   `json:"id"`           // UUID of System
	StarType    string   `json:"star_type"`    // e.g. "RedDwarf", "BlueGiant"
	OwnerUUID   string   `json:"owner_uuid"`   // Controlling Server
	PlanetIDs   []int    `json:"planet_ids"`
	IsFederated bool     `json:"is_federated"`
	TaxRate     float64  `json:"tax_rate"`     // 0.0 to 1.0
}

type Colony struct {
	ID            int            `json:"id"`
	SystemID      string         `json:"system_id"`
	OwnerID       int            `json:"owner_id"`
	Name          string         `json:"name"`
	Location      []int          `json:"location"`
	Buildings     map[string]int `json:"buildings"`
	Population    int            `json:"population"`
	
	// Resources (Tier 1 & 2)
	Food, Water             int `json:"food,water"`
	Iron, Carbon, Gold      int `json:"iron,carbon,gold"`
	Platinum, Uranium, Dia  int `json:"platinum,uranium,diamond"`
	Vegetation, Oxygen      int `json:"vegetation,oxygen"`
	
	// Stats
	Efficiency    float64 `json:"efficiency_mult"` // Evening Factor
	Health        float64 `json:"health"`
	DefenseRating int     `json:"defense_rating"`
}

type Fleet struct {
	ID           int    `json:"id"`
	OriginColony int    `json:"origin_colony"`
	Status       string `json:"status"` // IDLE, TRANSIT, ORBIT
	
	// Composition
	ArkShip    int `json:"ark_ship"`
	Fighters   int `json:"fighters"`
	Frigates   int `json:"frigates"`
	Haulers    int `json:"haulers"`
	
	// Cargo (For Trading)
	Cargo      map[string]int `json:"cargo"`
	Fuel       int            `json:"fuel"`
}

// --- Networking ---

type Peer struct {
	UUID        string
	Url         string
	GenesisHash string
	PeerCount   int
	CurrentTick int64
	LastSeen    time.Time
	Reputation  int // -1 = Banned
}

type MarketOrder struct {
	ID         string `json:"id"`
	SellerUUID string `json:"seller_uuid"`
	Item       string `json:"item"`
	Price      int    `json:"price"` // Credits
	Quantity   int    `json:"quantity"`
	IsBuy      bool   `json:"is_buy"`
}
