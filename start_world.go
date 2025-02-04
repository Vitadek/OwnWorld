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
        Name      string         `json:"name"`
        Location  []int          `json:"location"`
        Buildings map[string]int `json:"buildings"`
        BuildingLimit int        `json:"buildingLimit"`
        FoodCount     int        `json:"food"`
        WaterCount    int        `json:"water"`
        MineralCount  int        `json:"minerals"`
        UraniumCount  int        `json:"uranium"`
        Population    int        `json:"population"`
        Happiness     float64    `json:"happiness"`
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
        Colonies       []Colony `json:"colonies"`
        ResearchPoints int      `json:"researchPoints"`
        TicksPassed    int      `json:"ticksPassed"`
        StarCoins      int      `json:"starCoins"`
        TaxRate        float64  `json:"taxRate"`
        Ships          []Ship   `json:"ships"`
        Fleets         []Fleet  `json:"fleets"`
        Missions       []Mission `json:"Missions"`
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
                                Buildings: map[string]int{
                                        "farm": 1, "well": 1, "mine": 1, "uranium_mine": 1, "lab": 1,
                                },
                                BuildingLimit: 1000,
                                FoodCount:     100,
                                WaterCount:    100,
                                MineralCount:  100,
                                UraniumCount:  0,
                                Population:    100,
                                Happiness:     50,
                        },
                },
                ResearchPoints: 0,
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
                                DestinationLocation: []int{150, 80, 90},
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

                // Calculate and assign the correct destination distance for each mission
                fleet.Mission[0].DestinationDistance = missionDistance(fleet.Location, state.Missions[0].DestinationLocation)
                state.Missions[0].DestinationDistance = fleet.Mission[0].DestinationDistance
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

        // Only update distances and position if the mission is not yet successful
        if !mission.MissionSuccess {
                // Calculate the total distance remaining
                totalDistanceRemaining := missionDistance(fleet.Location, mission.DestinationLocation)

                if totalDistanceRemaining == 0 {
                        mission.MissionSuccess = true
                        fleet.Location = mission.DestinationLocation
                        return
                }

                // Calculate proportional movement for each coordinate
                for i := 0; i < len(fleet.Location); i++ {
                        direction := mission.DestinationLocation[i] - fleet.Location[i]
                        distanceToMove := float64(slowestSpeed) * (float64(direction) / float64(totalDistanceRemaining))

                        // Update the location considering the direction and clamping to the destination
                        newPosition := fleet.Location[i] + int(math.Round(distanceToMove))
                        if direction > 0 && newPosition > mission.DestinationLocation[i] {
                                fleet.Location[i] = mission.DestinationLocation[i]
                        } else if direction < 0 && newPosition < mission.DestinationLocation[i] {
                                fleet.Location[i] = mission.DestinationLocation[i]
                        } else {
                                fleet.Location[i] = newPosition
                        }
                }

                // Update distance traveled
                if slowestSpeed < totalDistanceRemaining {
                        mission.DistanceTraveled += slowestSpeed
                } else {
                        mission.DistanceTraveled += totalDistanceRemaining
                }

                // Check if mission is successful
                if isLocationEqual(fleet.Location, mission.DestinationLocation) {
                        mission.MissionSuccess = true
                }
        }
}

// Helper function to compare two location slices
func isLocationEqual(loc1, loc2 []int) bool {
        if len(loc1) != len(loc2) {
                return false
        }
        for i := range loc1 {
                if loc1[i] != loc2[i] {
                        return false
                }
        }
        return true
}

func updateState() {
        stateLock.Lock()
        defer stateLock.Unlock()

        if err := loadState("../data/game_state.json"); err != nil {
                fmt.Println("Error loading game state:", err)
                return
        }

        for index, colony := range state.Colonies {
                fmt.Printf("Current State Before Update for Colony %s:\n%+v\n", colony.Name, state.Colonies[index])
                updateColonyState(&state.Colonies[index])
        }

        // Update missions for each fleet
        for _, fleet := range state.Fleets {
                for i := range fleet.Mission {
                        updateDistanceTraveled(&fleet.Mission[i], &fleet)
                }
        }

        // Synchronize mission status back to the global state
        for i := range state.Missions {
                for _, fleet := range state.Fleets {
                        for _, mission := range fleet.Mission {
                                if state.Missions[i].MissionNumber == mission.MissionNumber {
                                        state.Missions[i] = mission
                                }
                        }
                }
        }

        state.TicksPassed++

        // Pretty-print updated state for readability
        fmt.Println("Updated State:")
        jsonData, _ := json.MarshalIndent(state, "", "  ")
        fmt.Println(string(jsonData))

        if err := saveState("../data/game_state.json"); err != nil {
                fmt.Println("Could not save game state:", err)
        } else {
                fmt.Println("Game state updated and saved.")
        }
}

func updateColonyState(colony *Colony) {
        oldPopulation := colony.Population
        oldFoodCount := colony.FoodCount
        oldWaterCount := colony.WaterCount
        
        updatePopulation(colony)
        consumeResources(colony)

        happinessModifier := 1.0
        if colony.Happiness > 50 {
                happinessModifier += (colony.Happiness - 50) / 100
        } else {
                happinessModifier -= (50 - colony.Happiness) / 100
        }

        taxRevenue := int(float64(colony.Population) * state.TaxRate * happinessModifier)
        state.StarCoins += taxRevenue

        // Additional logs for colony
        fmt.Printf("Population Growth for %s this tick: %d\n", colony.Name, colony.Population-oldPopulation)
        fmt.Printf("Food Consumed for %s this tick: %d\n", colony.Name, oldFoodCount-colony.FoodCount)
        fmt.Printf("Water Consumed for %s this tick: %d\n", colony.Name, oldWaterCount-colony.WaterCount)
}

func updatePopulation(colony *Colony) {
        foodRatio := float64(colony.FoodCount) / float64(colony.Population)
        waterRatio := float64(colony.WaterCount) / float64(colony.Population)

        baseGrowth := 10
        resourceModifier := 1.0

        if foodRatio >= 1 && waterRatio >= 1 {
                resourceModifier += 0.1

                if foodRatio >= 5 && waterRatio >= 5 {
                        resourceModifier += 0.5
                        colony.Happiness += 5
                }
        } else {
                fmt.Printf("Population growth halted due to resource scarcity in %s.\n", colony.Name)
                baseGrowth = 0
        }

        baseHappinessRecovery := 1.0
        colony.Happiness += baseHappinessRecovery

        if colony.Happiness > 100 {
                colony.Happiness = 100
        }
        if colony.Happiness < 0 {
                colony.Happiness = 0
        }

        happinessModifier := 1.0
        if colony.Happiness > 75 {
                happinessModifier += (colony.Happiness / 100)
        }

        colony.Population += int(float64(baseGrowth) * resourceModifier * happinessModifier)
}

func consumeResources(colony *Colony) {
        // Record the amount of resources consumed
        foodConsumed := colony.Population
        waterConsumed := colony.Population

        colony.FoodCount -= foodConsumed
        if colony.FoodCount < 0 {
                colony.FoodCount = 0
        }

        colony.WaterCount -= waterConsumed
        if colony.WaterCount < 0 {
                colony.WaterCount = 0
        }
}

func viewGameState() {
        stateLock.Lock()
        defer stateLock.Unlock()

        for _, colony := range state.Colonies {
                fmt.Printf("Colony: %s\n", colony.Name)
                fmt.Printf("Location: %v\n", colony.Location)
                fmt.Printf("Building Limit: %d\n", colony.BuildingLimit)
                fmt.Printf("Population: %d\n", colony.Population)
                fmt.Printf("Happiness: %.2f\n", colony.Happiness)
                fmt.Printf("Food Count: %d\n", colony.FoodCount)
                fmt.Printf("Water Count: %d\n", colony.WaterCount)
                fmt.Println("-------------------------")
        }
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
                        viewGameState() // Display game state including colony information
                        viewFleets()    // Display fleets
                        viewMissions()  // Display missions
                }
        }
} 
