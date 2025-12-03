package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

var ServerURL = "http://localhost:8080"
var CurrentUser string
var CurrentUserID int
var HomeSystemID string

// --- Models ---
type RegisterResponse struct {
	UserID   int    `json:"user_id"`
	SystemID string `json:"system_id"`
	Status   string `json:"status"`
}

type StatusResponse struct {
	UUID   string `json:"uuid"`
	Tick   int    `json:"tick"`
	Leader string `json:"leader"`
}

func main() {
	if url := os.Getenv("OWNWORLD_SERVER"); url != "" {
		ServerURL = url
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Println("OwnWorld Federation Client v2.3")
	fmt.Printf("Target Server: %s\n", ServerURL)

	// --- Login Loop ---
	// We stay in this loop until we get a successful auth
	for {
		if !loginLoop(reader) {
			// User chose to exit during login
			return
		}

		// --- Main Command Loop ---
		fmt.Println("\n--- COMMAND LINK ESTABLISHED ---")
		fmt.Printf("Welcome, Commander %s.\n", CurrentUser)
		fmt.Println("Commands: status, build, burn, launch, help, logout, quit")

		logout := false
		for !logout {
			fmt.Printf("[%s]> ", CurrentUser)
			text, _ := reader.ReadString('\n')
			text = strings.TrimSpace(text)
			parts := strings.Fields(text)

			if len(parts) == 0 {
				continue
			}

			cmd := parts[0]

			switch cmd {
			case "status":
				doStatus()
			case "build":
				if len(parts) < 4 {
					fmt.Println("Usage: build <colony_id> <structure> <amount>")
					continue
				}
				amt, _ := strconv.Atoi(parts[3])
				colID, _ := strconv.Atoi(parts[1])
				doBuild(colID, parts[2], amt)
			case "burn":
				if len(parts) < 4 {
					fmt.Println("Usage: burn <colony_id> <item> <amount>")
					continue
				}
				amt, _ := strconv.Atoi(parts[3])
				colID, _ := strconv.Atoi(parts[1])
				doBurn(colID, parts[2], amt)
			case "launch":
				if len(parts) < 4 {
					fmt.Println("Usage: launch <fleet_id> <dest_system_uuid> <distance>")
					continue
				}
				fleetID, _ := strconv.Atoi(parts[1])
				dist, _ := strconv.Atoi(parts[3])
				doLaunch(fleetID, parts[2], dist)
			case "help":
				fmt.Println("Available Commands:")
				fmt.Println("  status                         - Check server tick and leader")
				fmt.Println("  build <colID> <struct> <amt>   - Construct buildings")
				fmt.Println("  burn <colID> <item> <amt>      - Sell resources to the bank")
				fmt.Println("  launch <fid> <dest> <dist>     - Send fleet to another system")
				fmt.Println("  logout                         - Return to login screen")
				fmt.Println("  quit                           - Disconnect")
			case "logout":
				fmt.Println("Logging out...")
				logout = true
				CurrentUser = ""
				CurrentUserID = 0
			case "quit", "exit":
				fmt.Println("Disconnecting...")
				os.Exit(0)
			default:
				fmt.Println("Unknown command. Type 'help' for options.")
			}
		}
	}
}

func loginLoop(reader *bufio.Reader) bool {
	for {
		fmt.Println("\n--- AUTHENTICATION REQUIRED ---")
		fmt.Print("Username: ")
		user, _ := reader.ReadString('\n')
		user = strings.TrimSpace(user)

		if user == "quit" || user == "exit" {
			return false
		}

		fmt.Print("Password: ")
		pass, _ := reader.ReadString('\n')
		pass = strings.TrimSpace(pass)

		if user == "" || pass == "" {
			continue
		}

		fmt.Print("Connecting... ")
		if doRegister(user, pass) {
			CurrentUser = user
			return true
		} else {
			fmt.Println("Login Failed: Invalid credentials or username taken.")
			fmt.Println("Try again or type 'quit' to exit.")
		}
	}
}

func doStatus() {
	resp, err := http.Get(ServerURL + "/api/status")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var s StatusResponse
	json.Unmarshal(body, &s)
	
	if s.Leader == "" { s.Leader = "Unknown" }
	if s.UUID == "" { s.UUID = "Unknown" }

	leaderDisp := s.Leader
	if len(s.Leader) > 8 { leaderDisp = s.Leader[:8] }
	uuidDisp := s.UUID
	if len(s.UUID) > 8 { uuidDisp = s.UUID[:8] }

	fmt.Printf("Tick: %d | Leader: %s | UUID: %s\n", s.Tick, leaderDisp, uuidDisp)
}

func doRegister(user, pass string) bool {
	payload := map[string]string{"username": user, "password": pass}
	data, _ := json.Marshal(payload)

	resp, err := http.Post(ServerURL+"/api/register", "application/json", bytes.NewBuffer(data))
	if err != nil {
		fmt.Printf("Connection Error: %v\n", err)
		return false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	
	// Server returns 400 if user exists/taken/wrong pass (in this MVP logic)
	if resp.StatusCode != 200 {
		// Log internal error for debugging if verbose, but return false to loop
		return false
	}

	var r RegisterResponse
	if err := json.Unmarshal(body, &r); err != nil {
		fmt.Printf("Protocol Error: %v\n", err)
		return false
	}
	
	CurrentUserID = r.UserID
	HomeSystemID = r.SystemID
	fmt.Printf("Success! User ID: %d, System: %s\n", r.UserID, r.SystemID)
	return true
}

func doBuild(colID int, structure string, amount int) {
	payload := map[string]interface{}{
		"colony_id": colID,
		"structure": structure,
		"amount":    amount,
	}
	data, _ := json.Marshal(payload)

	resp, err := http.Post(ServerURL+"/api/build", "application/json", bytes.NewBuffer(data))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("Response: %s\n", string(body))
}

func doBurn(colID int, item string, amount int) {
	payload := map[string]interface{}{
		"colony_id": colID,
		"item":      item,
		"amount":    amount,
	}
	data, _ := json.Marshal(payload)

	resp, err := http.Post(ServerURL+"/api/bank/burn", "application/json", bytes.NewBuffer(data))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("Bank Receipt: %s\n", string(body))
}

func doLaunch(fleetID int, dest string, distance int) {
	payload := map[string]interface{}{
		"fleet_id":    fleetID,
		"dest_system": dest,
		"distance":    distance,
	}
	data, _ := json.Marshal(payload)

	resp, err := http.Post(ServerURL+"/api/fleet/launch", "application/json", bytes.NewBuffer(data))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("Mission Status: %s\n", string(body))
}
