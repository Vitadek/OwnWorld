func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	json.NewDecoder(r.Body).Decode(&req)
	
	// Use BLAKE3 for password hashing in this example (or Argon2 in production)
	hash := hashBLAKE3([]byte(req.Password))

	var count int
	db.QueryRow("SELECT count(*) FROM users WHERE username=?", req.Username).Scan(&count)
	if count > 0 {
		http.Error(w, "Taken", 400)
		return
	}

	// 1. Generate Identity (User UUID)
	pubKey, _, _ := ed25519.GenerateKey(nil) // rand.Reader implied if nil
	userUUID := hashBLAKE3(pubKey) 

	res, _ := db.Exec("INSERT INTO users (global_uuid, username, password_hash, is_local) VALUES (?, ?, ?, 1)", userUUID, req.Username, hash)
	uid, _ := res.LastInsertId()

	// 2. Goldilocks Search (Find a good planet)
	var sysID string
	// var bestX, bestY, bestZ int 
	found := false
	
	// Assume ServerLoc is 0,0,0 if not defined globally
	serverX, serverY, serverZ := 0, 0, 0 

	rand.Seed(time.Now().UnixNano())
	for i := 0; i < 50; i++ {
		// Search near the Server Location
		x := serverX + rand.Intn(100) - 50
		y := serverY + rand.Intn(100) - 50
		z := serverZ + rand.Intn(100) - 50
		
		// Deterministic ID
		tempID := fmt.Sprintf("sys-%d-%d-%d", x, y, z)
		
		// Check Efficiency (Food & Iron)
		// Note: GetEfficiency is in simulation.go
		if GetEfficiency(x*1000+y, "food") > 0.9 && GetEfficiency(x*1000+y, "iron") > 0.8 {
			sysID = tempID
			// bestX, bestY, bestZ = x, y, z
			found = true
			break
		}
	}
	if !found {
		sysID = fmt.Sprintf("sys-%s-fallback", req.Username)
	}

	// Create System if not exists (Lazy generation)
	db.Exec("INSERT OR IGNORE INTO solar_systems (id, x, y, z, star_type, owner_uuid) VALUES (?,?,?,?, 'G2V', ?)", 
		sysID, rand.Intn(100), rand.Intn(100), rand.Intn(100), ServerUUID)

	// 3. Create The Homestead (Colony)
	// Give them starting buildings so they don't die immediately
	startBuilds := `{"farm": 5, "iron_mine": 5, "urban_housing": 10}`
	
	db.Exec(`INSERT INTO colonies 
		(system_id, owner_uuid, name, pop_laborers, food, iron, buildings_json) 
		VALUES (?, ?, ?, 100, 5000, 1000, ?)`, 
		sysID, userUUID, req.Username+" Prime", startBuilds)

	// 4. Create The Ark Ship (Fleet)
	// It spawns in ORBIT of the new colony with fuel
	db.Exec(`INSERT INTO fleets 
		(owner_uuid, status, origin_system, ark_ship, fuel) 
		VALUES (?, 'ORBIT', ?, 1, 5000)`, 
		userUUID, sysID)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "registered", 
		"user_id": uid, 
		"message": "Welcome Commander. Colony established. Ark Ship awaiting orders.",
		"system_id": sysID,
		"uuid": userUUID,
	})
}
