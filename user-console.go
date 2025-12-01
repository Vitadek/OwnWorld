package main

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var dbFile = "./data/ownworld.db"
var db *sql.DB // Global DB handle

func main() {
	// 1. Ensure Data Directory Exists
	if _, err := os.Stat("./data"); os.IsNotExist(err) {
		os.MkdirAll("./data", 0700)
	}

	// 2. Connect to DB
	var err error
	db, err = sql.Open("sqlite3", dbFile)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	// 3. Security Lock
	if err := os.Chmod(dbFile, 0600); err != nil {
		// Ignore if file doesn't exist yet
	}

	// Initialize tables if missing
	initSchema()
	os.Chmod(dbFile, 0600) // Re-apply lock

	rand.Seed(time.Now().UnixNano())

	// 4. CLI Argument Mode (Non-Interactive)
	if len(os.Args) > 1 {
		handleCLI()
		return
	}

	// 5. Main Menu Loop (Interactive)
	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Println("\n========================================")
		fmt.Println("   OWNWORLD ADMINISTRATION CONSOLE")
		fmt.Println("========================================")
		fmt.Println("1. List Users")
		fmt.Println("2. Register New User")
		fmt.Println("3. Delete User")
		fmt.Println("4. Exit")
		fmt.Println("========================================")
		fmt.Print("Select Option: ")

		if !scanner.Scan() {
			break
		}
		choice := strings.TrimSpace(scanner.Text())

		switch choice {
		case "1":
			listUsers()
		case "2":
			registerUser(scanner)
		case "3":
			deleteUserInteractive(scanner)
		case "4":
			fmt.Println("Exiting.")
			return
		default:
			fmt.Println("Invalid option.")
		}
	}
}

// --- CLI Logic ---

func handleCLI() {
	command := os.Args[1]

	switch command {
	case "list":
		listUsers()
	case "delete":
		if len(os.Args) < 3 {
			fmt.Println("Usage: delete <id> CONFIRM")
			return
		}
		id, err := strconv.Atoi(os.Args[2])
		if err != nil {
			fmt.Println("Error: Invalid User ID")
			return
		}
		
		confirm := ""
		if len(os.Args) > 3 {
			confirm = os.Args[3]
		}

		if confirm != "CONFIRM" {
			fmt.Printf("Error: To delete User ID %d, you must provide the argument 'CONFIRM' after the ID.\n", id)
			fmt.Printf("Example: %s delete %d CONFIRM\n", os.Args[0], id)
			return
		}
		
		performDelete(id)
	default:
		fmt.Println("Unknown command. Available commands: list, delete")
	}
}

// --- Menu Functions ---

func listUsers() {
	rows, err := db.Query("SELECT id, username, star_coins FROM users ORDER BY id ASC")
	if err != nil {
		fmt.Printf("Error querying users: %v\n", err)
		return
	}
	defer rows.Close()

	fmt.Println("\nID  | Username             | StarCoins | Colonies")
	fmt.Println("----|----------------------|-----------|----------")

	for rows.Next() {
		var id int
		var user string
		var coins int
		
		rows.Scan(&id, &user, &coins)
		
		// Count colonies
		var colCount int
		db.QueryRow("SELECT COUNT(*) FROM colonies WHERE user_id=?", id).Scan(&colCount)

		fmt.Printf("%-3d | %-20s | %-9d | %d\n", id, user, coins, colCount)
	}
}

func registerUser(scanner *bufio.Scanner) {
	fmt.Println("\n--- NEW USER REGISTRATION ---")
	fmt.Print("Enter New Username: ")
	scanner.Scan()
	username := strings.TrimSpace(scanner.Text())

	fmt.Print("Enter Password: ")
	scanner.Scan()
	password := strings.TrimSpace(scanner.Text())

	if username == "" || password == "" {
		fmt.Println("Error: Credentials cannot be empty.")
		return
	}

	// Create User
	hash := sha256.Sum256([]byte(password))
	hashStr := hex.EncodeToString(hash[:])

	res, err := db.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", username, hashStr)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			fmt.Println("Error: Username already exists.")
		} else {
			fmt.Printf("Error creating user: %v\n", err)
		}
		return
	}
	uid, _ := res.LastInsertId()
	fmt.Printf("[+] User '%s' created (ID: %d)\n", username, uid)

	// Seed Colony
	bJson, _ := json.Marshal(map[string]int{
		"farm":      100,
		"well":      100,
		"iron_mine": 10,
		"school":    1,
	})

	resCol, err := db.Exec(`INSERT INTO colonies 
		(user_id, name, population, food, water, iron, buildings_json, loc_x, loc_y) 
		VALUES (?, ?, 1000, 20000, 20000, 1000, ?, ?, ?)`,
		uid, username+"'s Prime", string(bJson), rand.Intn(100), rand.Intn(100))

	if err != nil {
		fmt.Printf("Error seeding colony: %v\n", err)
		return
	}
	colID, _ := resCol.LastInsertId()
	fmt.Printf("[+] Colony seeded (ID: %d)\n", colID)

	// Seed Fleet
	_, err = db.Exec(`INSERT INTO fleets 
		(user_id, origin_colony, status, fighters, probes, colonizers, destroyers) 
		VALUES (?, ?, 'IDLE', 0, 1, 0, 0)`, uid, colID)

	if err != nil {
		fmt.Printf("Error seeding fleet: %v\n", err)
		return
	}
	fmt.Println("[+] Fleet deployed. Registration Success.")
}

// Wrapper for interactive deletion
func deleteUserInteractive(scanner *bufio.Scanner) {
	fmt.Println("\n--- DELETE USER ---")
	fmt.Print("Enter User ID to DELETE: ")
	scanner.Scan()
	input := strings.TrimSpace(scanner.Text())
	
	id, err := strconv.Atoi(input)
	if err != nil {
		fmt.Println("Invalid ID.")
		return
	}

	fmt.Printf("WARNING: This will wipe User ID %d and all their colonies/fleets.\n", id)
	fmt.Print("Type 'CONFIRM' to proceed: ")
	scanner.Scan()
	if strings.TrimSpace(scanner.Text()) != "CONFIRM" {
		fmt.Println("Deletion cancelled.")
		return
	}

	performDelete(id)
}

// Shared delete logic
func performDelete(id int) {
	// 1. Delete Fleets
	_, err := db.Exec("DELETE FROM fleets WHERE user_id=?", id)
	if err != nil { fmt.Println("Error deleting fleets:", err) }

	// 2. Delete Colonies
	_, err = db.Exec("DELETE FROM colonies WHERE user_id=?", id)
	if err != nil { fmt.Println("Error deleting colonies:", err) }

	// 3. Delete User
	res, err := db.Exec("DELETE FROM users WHERE id=?", id)
	if err != nil {
		fmt.Println("Error deleting user:", err)
	} else {
		count, _ := res.RowsAffected()
		if count > 0 {
			fmt.Println("User deleted successfully.")
		} else {
			fmt.Println("User ID not found.")
		}
	}
}

func initSchema() {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE, password_hash TEXT, star_coins INTEGER DEFAULT 1000
	);
	CREATE TABLE IF NOT EXISTS colonies (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, name TEXT,
		loc_x INTEGER, loc_y INTEGER, loc_z INTEGER, buildings_json TEXT,
		population INTEGER DEFAULT 1000,
		food INTEGER DEFAULT 5000, water INTEGER DEFAULT 5000,
		iron INTEGER DEFAULT 500, diamond INTEGER DEFAULT 0, 
		platinum INTEGER DEFAULT 0, starfuel INTEGER DEFAULT 0,
		health REAL DEFAULT 100.0, intelligence REAL DEFAULT 50.0, 
		crime REAL DEFAULT 0.0, happiness REAL DEFAULT 100.0
	);
	CREATE TABLE IF NOT EXISTS fleets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, origin_colony INTEGER,
		status TEXT,
		fighters INTEGER, probes INTEGER, colonizers INTEGER, destroyers INTEGER
	);
	CREATE TABLE IF NOT EXISTS ledger (
		tick INTEGER PRIMARY KEY,
		timestamp INTEGER,
		final_hash TEXT
	);
	`
	db.Exec(schema)
}
