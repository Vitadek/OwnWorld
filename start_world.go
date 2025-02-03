package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"math/rand"
	"os"
	"sync"
	"time"
)

type Colony struct {
	Name     string `json:"name"`
	Location []int  `json:"location"`
}

type InfrastructureBlueprint struct {
	Name               string `json:"name"`
	MineralsNeeded     int    `json:"minerals_needed"`
	WaterNeeded        int    `json:"water_needed"`
	FoodNeeded         int    `json:"food_needed"`
	ResearchPoints     int    `json:"research_points"`
	ProductionModifier int    `json:"production_modifier"`
}

type Ship struct {
	Name             string  `json:"name"`
	Class            string  `json:"Class"`
	Health           int     `json:"Health"`
	Description      string  `json:"Description"`
	PersonnelLimit   int     `json:"Personnel_Limit"`
	PersonnelMinimum int     `json:"Personnel_Minimum"`
	CargoLimit       int     `json:"Cargo_Limit"`
	FuelCapacity     int     `json:"Fuel_Capacity"`
	FuelEfficiency   float64 `json:"Fuel_Efficiency"`
	Speed            int     `json:"Speed"`
	Level            int     `json:"Level"`
	Damage           int     `json:"Damage"`
	Price            int     `json:"Price"`
	Amount           int     `json:"Amount"`
}

type FleetShip struct {
	Name   string `json:"name"`
	Amount int    `json:"amount"`
}

type Mission struct {
	MissionNumber       int      `json:"Mission_Number"`
	Fleets              []string `json:"fleets"`
	Location            []int    `json:"Location"`
	DestinationLocation []int    `json:"DestinationLocation"`
	DistanceTraveled    int      `json:"Distance_Traveled"`
	DestinationDistance int      `json:"Destination_Distance"`
	MissionType         string   `json:"Mission_Type"`
	MissionSuccess      bool     `json:"Mission_Success"`
}

type Fleet struct {
	Name     string      `json:"name"`
	Ships    []FleetShip `json:"ships"`
	Mission  []Mission   `json:"Mission"`
	Location []int       `json:"Location"`
}

type GameState struct {
	Colonies        []Colony       `json:"colonies"`
	BuildingLimit   int            `json:"buildingLimit"`
	FoodCount       int            `json:"food"`
	WaterCount      int            `json:"water"`
	MineralCount    int            `json:"minerals"`
	UraniumCount    int            `json:"uranium"`
	Population      int            `json:"population"`
	Happiness       float64        `json:"happiness"`
	ResearchPoints  int            `json:"researchPoints"`
	Infra           map[string]int `json:"infrastructure"`
	TicksPassed     int            `json:"ticksPassed"`
	StarCoins       int            `json:"starCoins"`
	TaxRate         float64        `json:"taxRate"`
	Ships           []Ship         `json:"ships"`
	Fleets          []Fleet        `json:"fleets"`
	Missions        []Mission      `json:"Missions"`
}

var state GameState
var stateLock sync.Mutex
var fileLock sync.Mutex

func loadShips(shipFilename string) ([]Ship, error) {
	data, err := ioutil.ReadFile(shipFilename)
	if err != nil {
		return nil, err
	}

	var ships []Ship
	err = json.Unmarshal(data, &ships)
	return ships, err
}

func saveState(filename string) error {
	fileLock.Lock()
	defer fileLock.Unlock()

	data, err := json.MarshalIndent(state, "", "  ") // Pretty-print JSON
	if err != nil {
		return err
	}

	tempFilename := filename + ".tmp"
	if err := ioutil.WriteFile(tempFilename, data, 0644); err != nil {
		return err
	}

	return os.Rename(tempFilename, filename)
}

func loadState(filename string) error {
	fileLock.Lock()
	defer fileLock.Unlock()

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, &state)
}

func missionDistance(startLocation, endLocation []int) int {
	squareDelta := func(a, b int) int {
		return (a - b) * (a - b)
	}

	return int(math.Sqrt(float64(squareDelta(startLocation[0], endLocation[0]) +
		squareDelta(startLocation[1], endLocation[1]) +
		squareDelta(startLocation[2], endLocation[2]))))
}

func initializeState() {
	rand.Seed(time.Now().UnixNano())
	state = GameState{
		Colonies: []Colony{
			{
				Name:     "New Eden",
				Location: []int{100, 50, 30},
			},
		},
		BuildingLimit:  1000,
		FoodCount:      100,
		WaterCount:     100,
		MineralCount:   100,
		UraniumCount:   0,
		Population:     100,
		Happiness:      50,
		Infra:          map[string]int{},
		StarCoins:      100000,
		TaxRate:        0.1,
		Ships: []Ship{
			{
				Name:             "Explorite",
				Class:            "Explorite",
				Health:           50,
				Description:      "A simple exploring machine, no pilot needed",
				PersonnelLimit:   0,
				PersonnelMinimum: 0,
				CargoLimit:       0,
				FuelCapacity:     100,
				FuelEfficiency:   1,
				Speed:            rand.Intn(20) + 80,
				Level:            1,
				Damage:           0,
				Price:            1000,
				Amount:           5,
			},
			{
				Name:             "Enforcer",
				Class:            "Enforcer",
				Health:           500,
				Description:      "An air combat machine",
				PersonnelLimit:   4,
				PersonnelMinimum: 4,
				CargoLimit:       0,
				FuelCapacity:     25,
				FuelEfficiency:   1,
				Speed:            rand.Intn(20) + 50,
				Level:            1,
				Damage:           100,
				Price:            5000,
				Amount:           5,
			},
			{
				Name:             "Pioneer",
				Class:            "Pioneer",
				Health:           5000,
				Description:      "A ship to create new settlements or to transfer settlers",
				PersonnelLimit:   1000,
				PersonnelMinimum: 4,
				CargoLimit:       0,
				FuelCapacity:     5000,
				FuelEfficiency:   0.5,
				Speed:            rand.Intn(20) + 20,
				Level:            1,
				Damage:           25,
				Price:            10000,
				Amount:           1,
			},
		},
		Fleets: []Fleet{
			{
				Name: "Gondor",
				Ships: []FleetShip{
					{
						Name:   "Enforcer",
						Amount: 1,
					},
					{
						Name:   "Explorite",
						Amount: 1,
					},
				},
				Mission:  []Mission{},
				Location: []int{100, 50, 30},
			},
		},
		Missions: []Mission{
			{
				MissionNumber:       1,
				Fleets:              []string{"Gondor"},
				DestinationLocation: []int{150, 80, 90}, // Target location
				DistanceTraveled:    0,
				DestinationDistance: 0,
				MissionType:         "Exploration",
				MissionSuccess:      false,
			},
		},
	}

	// Assign the first mission to the Gondor fleet
	if len(state.Fleets) > 0 {
		fleet := &state.Fleets[0]
		fleet.Mission = append(fleet.Mission, state.Missions[0])

		// Calculate destination distance
		state.Missions[0].DestinationDistance = missionDistance(fleet.Location, state.Missions[0].DestinationLocation)
	}

	// Set base infrastructure offsets directly; this avoids missing file issues
	for _, infraName := range []string{"farm", "well", "mine", "uranium_mine", "lab"} {
		state.Infra[infraName] = 1
	}
}

func updateDistanceTraveled(mission *Mission, fleet *Fleet) {
	// Find the slowest ship in the fleet
	slowestSpeed := int(^uint(0) >> 1)
	for _, fleetShip := range fleet.Ships {
		for _, ship := range state.Ships {
			if ship.Name == fleetShip.Name {
				speed := ship.Speed
				if speed < slowestSpeed {
					slowestSpeed = speed
				}
			}
		}
	}

	// Update distance traveled if the mission is not yet successful
	if !mission.MissionSuccess {
		mission.DistanceTraveled += slowestSpeed

		// Update fleet location towards the mission's destination
		for i := range fleet.Location {
			if fleet.Location[i] < mission.DestinationLocation[i] {
				distanceRemaining := mission.DestinationLocation[i] - fleet.Location[i]
				fleet.Location[i] += int(math.Min(float64(slowestSpeed), float64(distanceRemaining)))
			} else if fleet.Location[i] > mission.DestinationLocation[i] {
				distanceRemaining := fleet.Location[i] - mission.DestinationLocation[i]
				fleet.Location[i] -= int(math.Min(float64(slowestSpeed), float64(distanceRemaining)))
			}
		}

		// Check if mission is successful
		if mission.DistanceTraveled >= mission.DestinationDistance {
			mission.MissionSuccess = true
			fleet.Location = mission.DestinationLocation
		}
	}
}

func updateState() {
	stateLock.Lock()
	defer stateLock.Unlock()

	if err := loadState("../data/game_state.json"); err != nil {
		fmt.Println("Error loading game state:", err)
		return
	}

	fmt.Printf("Current State Before Update:\n%+v\n", state)

	oldPopulation := state.Population
	oldStarCoins := state.StarCoins
	oldFoodCount := state.FoodCount
	oldWaterCount := state.WaterCount

	updatePopulation()
	consumeResources()

	happinessModifier := 1.0
	if state.Happiness > 50 {
		happinessModifier += (state.Happiness - 50) / 100
	} else {
		happinessModifier -= (50 - state.Happiness) / 100
	}

	taxRevenue := int(float64(state.Population) * state.TaxRate * happinessModifier)
	state.StarCoins += taxRevenue

	if state.StarCoins < 0 {
		fmt.Println("Warning: Negative StarCoins! Ensure fiscal policy is sound.")
	}

	// Update missions
	for _, fleet := range state.Fleets {
		for i := range fleet.Mission {
			updateDistanceTraveled(&fleet.Mission[i], &fleet)
		}
	}

	state.TicksPassed++

	// Pretty-print updated state for readability
	fmt.Println("Updated State:")
	jsonData, _ := json.MarshalIndent(state, "", "  ")
	fmt.Println(string(jsonData))

	// Additional logs
	fmt.Printf("Population Growth this tick: %d\n", state.Population-oldPopulation)
	fmt.Printf("StarCoins Gained this tick: %d\n", state.StarCoins-oldStarCoins)
	fmt.Printf("Food Consumed this tick: %d\n", oldFoodCount-state.FoodCount)
	fmt.Printf("Water Consumed this tick: %d\n", oldWaterCount-state.WaterCount)

	if err := saveState("../data/game_state.json"); err != nil {
		fmt.Println("Could not save game state:", err)
	} else {
		fmt.Println("Game state updated and saved.")
	}
}

func updatePopulation() {
	foodRatio := float64(state.FoodCount) / float64(state.Population)
	waterRatio := float64(state.WaterCount) / float64(state.Population)

	baseGrowth := 10
	resourceModifier := 1.0

	if foodRatio >= 1 && waterRatio >= 1 {
		resourceModifier += 0.1

		if foodRatio >= 5 && waterRatio >= 5 {
			resourceModifier += 0.5
			state.Happiness += 5
		}
	} else {
		fmt.Println("Population growth halted due to resource scarcity.")
		baseGrowth = 0
	}

	baseHappinessRecovery := 1.0
	state.Happiness += baseHappinessRecovery

	if state.Happiness > 100 {
		state.Happiness = 100
	}
	if state.Happiness < 0 {
		state.Happiness = 0
	}

	happinessModifier := 1.0
	if state.Happiness > 75 {
		happinessModifier += (state.Happiness / 100)
	}

	state.Population += int(float64(baseGrowth) * resourceModifier * happinessModifier)
}

func consumeResources() {
	// Record the amount of resources consumed
	foodConsumed := state.Population
	waterConsumed := state.Population

	state.FoodCount -= foodConsumed
	if state.FoodCount < 0 {
		state.FoodCount = 0
	}

	state.WaterCount -= waterConsumed
	if state.WaterCount < 0 {
		state.WaterCount = 0
	}
}

func viewGameState() {
	stateLock.Lock()
	defer stateLock.Unlock()

	fmt.Println("Colonies and their Locations:")
	for _, colony := range state.Colonies {
		fmt.Printf("Colony Name: %s, Location (x, y, z): %v\n", colony.Name, colony.Location)
	}
	fmt.Printf("Building Limit: %d units\n", state.BuildingLimit)
	fmt.Printf("Population: %d\n", state.Population)
	fmt.Println("-------------------------")
}

func viewFleets() {
	stateLock.Lock()
	defer stateLock.Unlock()

	fmt.Println("Current Fleets:")
	if len(state.Fleets) == 0 {
		fmt.Println("No fleets available.")
		return
	}

	for _, fleet := range state.Fleets {
		fmt.Printf("Fleet Name: %s\n", fleet.Name)
		fmt.Println("Ships in Fleet:")
		for _, ship := range fleet.Ships {
			fmt.Printf("  - Ship Name: %s, Amount: %d\n", ship.Name, ship.Amount)
		}
		fmt.Println("Missions:")
		for _, mission := range fleet.Mission {
			fmt.Printf("  - Mission Number: %d, Type: %s\n", mission.MissionNumber, mission.MissionType)
			fmt.Printf("    Destination: %v, DistanceTraveled: %d, DestinationDistance: %d\n", 
				mission.DestinationLocation, mission.DistanceTraveled, mission.DestinationDistance)
			fmt.Printf("    Success: %v\n", mission.MissionSuccess)
		}
		fmt.Printf("Current Location: %v\n", fleet.Location)
		fmt.Println("-------------------------")
	}
}

func viewMissions() {
	stateLock.Lock()
	defer stateLock.Unlock()

	fmt.Println("Active Missions:")
	if len(state.Missions) == 0 {
		fmt.Println("No missions available.")
		return
	}

	for _, mission := range state.Missions {
		fmt.Printf("Mission Number: %d\n", mission.MissionNumber)
		fmt.Printf("Mission Type: %s\n", mission.MissionType)
		fmt.Println("Involved Fleets:")
		for _, fleetName := range mission.Fleets {
			fmt.Printf("  - Fleet Name: %s\n", fleetName)
		}
		fmt.Printf("Destination Location: %v\n", mission.DestinationLocation)
		fmt.Printf("Distance Traveled: %d\n", mission.DistanceTraveled)
		fmt.Printf("Destination Distance: %d\n", mission.DestinationDistance)
		fmt.Printf("Mission Success: %v\n", mission.MissionSuccess)
		fmt.Println("-------------------------")
	}
}

func main() {
	fmt.Println("Welcome to the World Simulation Game!")

	filename := "../data/game_state.json"

	if _, err := os.Stat(filename); os.IsNotExist(err) {
		fmt.Println("No existing game state found, initializing new game state.")
		initializeState()
		if err := saveState(filename); err != nil {
			fmt.Println("Could not save initial game state:", err)
		}
	} else {
		fmt.Println("Existing game state found, loading...")
		if err := loadState(filename); err != nil {
			fmt.Println("Could not load game state:", err)
		} else {
			fmt.Println("Loaded existing game state successfully!")
		}
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			updateState()
			viewGameState() // Display game state including colony location and building limit
			viewFleets()    // Display fleets
			viewMissions()  // Display missions
		}
	}
}
