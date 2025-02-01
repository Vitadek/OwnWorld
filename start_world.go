package main

import (
        "encoding/json"
        "fmt"
        "io/ioutil"
        "os"
        "sync"
        "time"
)

type InfrastructureBlueprint struct {
        Name              string `json:"name"`
        MineralsNeeded    int    `json:"minerals_needed"`
        WaterNeeded       int    `json:"water_needed"`
        FoodNeeded        int    `json:"food_needed"`
        ResearchPoints    int    `json:"research_points"`
        ProductionModifier int   `json:"production_modifier"`
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
        Level            int     `json:"Level"`
        Damage           int     `json:"Damage"`
        Price            int     `json:"Price"`
        Amount           int     `json:"Amount"`
}

type GameState struct {
        FoodCount      int            `json:"food"`
        WaterCount     int            `json:"water"`
        MineralCount   int            `json:"minerals"`
        UraniumCount   int            `json:"uranium"`
        Population     int            `json:"population"`
        Happiness      float64        `json:"happiness"`
        ResearchPoints int            `json:"researchPoints"`
        Infra          map[string]int `json:"infrastructure"`
        TicksPassed    int            `json:"ticksPassed"`
        StarCoins      int            `json:"starCoins"`
        TaxRate        float64        `json:"taxRate"`
        Ships          []Ship         `json:"ships"`
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

func initializeState() {
    state = GameState{
        FoodCount:    100,
        WaterCount:   100,
        MineralCount: 100,
        UraniumCount: 0,
        Population:   100,
        Happiness:    50,
        Infra:        map[string]int{},
        StarCoins:    100000,
        TaxRate:      0.1,
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
                Level:            1,
                Damage:           25,
                Price:            10000,
                Amount:           1,
            },
        },
    }

    // Set base infrastructure offsets directly; this avoids missing file issues
    for _, infraName := range []string{"farm", "well", "mine", "uranium_mine", "lab"} {
        state.Infra[infraName] = 1
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
                }
        }
}
