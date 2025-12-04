package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

    // Assuming you generated the proto code in this package:
    pb "ownworld/pkg/federation" 
    "google.golang.org/protobuf/proto"
)

// --- Registration (The Alice Patch) ---

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	json.NewDecoder(r.Body).Decode(&req)
	
    // 1. Generate Global Identity (The Fix)
    pubKey, _, _ := ed25519.GenerateKey(rand.Reader)
    userUUID := hashBLAKE3(pubKey) // Hash(PubKey) is the Global ID
    pubKeyStr := hex.EncodeToString(pubKey)
    passHash := hashBLAKE3([]byte(req.Password))

	var count int
	db.QueryRow("SELECT count(*) FROM users WHERE username=?", req.Username).Scan(&count)
	if count > 0 {
		http.Error(w, "Username Taken", 400)
		return
	}

    // 2. Insert User
	res, _ := db.Exec(`INSERT INTO users (global_uuid, username, password_hash, is_local, ed25519_pubkey) 
                       VALUES (?, ?, ?, 1, ?)`, userUUID, req.Username, passHash, pubKeyStr)
	uid, _ := res.LastInsertId()

	// 3. Goldilocks Search (Simplified)
	sysID := fmt.Sprintf("sys-%s-%d", userUUID[:6], rand.Intn(999))
	db.Exec("INSERT OR IGNORE INTO solar_systems (id, x, y, z, star_type, owner_uuid) VALUES (?, 0, 0, 0, 'G2V', ?)", sysID, ServerUUID)

	// 4. Spawn Homestead + Ark
	startBuilds := `{"farm": 5, "well": 5, "urban_housing": 10}`
	db.Exec(`INSERT INTO colonies (system_id, owner_uuid, name, pop_laborers, food, iron, buildings_json) 
	         VALUES (?, ?, ?, 100, 5000, 1000, ?)`, sysID, userUUID, req.Username+" Prime", startBuilds)

	db.Exec(`INSERT INTO fleets (owner_uuid, status, origin_system, ark_ship, fuel) 
			 VALUES (?, 'ORBIT', ?, 1, 5000)`, userUUID, sysID)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "registered", 
		"user_id": uid, 
        "user_uuid": userUUID, // Client needs this
		"system_id": sysID,
        "message": "Identity Secured. Ark Ship Ready.",
	})
}

// --- Dual-Mode Federation Handler ---

func handleFederationMessage(w http.ResponseWriter, r *http.Request) {
    // 1. Check Content-Type
    if r.Header.Get("Content-Type") == "application/x-protobuf" {
        // --- ROBOT PATH (Protobuf) ---
        body, _ := io.ReadAll(r.Body)
        
        // Decompress LZ4 (if you stick to LZ4 over wire)
        // rawProto := decompressLZ4(body) 
        
        var packet pb.Packet
        if err := proto.Unmarshal(body, &packet); err != nil {
            http.Error(w, "Bad Proto", 400)
            return
        }

        // Verify Signature
        // pubKey := lookupPeerKey(packet.SenderUuid)
        // if !ed25519.Verify(pubKey, packet.Payload, packet.Signature) { ... }

        switch inner := packet.Content.(type) {
        case *pb.Packet_Heartbeat:
            // Process Heartbeat
            InfoLog.Printf("Proto Heartbeat from %s: Tick %d", packet.SenderUuid, inner.Heartbeat.Tick)
        case *pb.Packet_FleetMove:
            // Process Fleet Arrival
            InfoLog.Printf("Incoming Fleet from %s", packet.SenderUuid)
        }
        w.Write([]byte("ACK_PROTO"))
        return
    }

    // --- HUMAN PATH (JSON / Debug) ---
    // Fallback to existing JSON logic for debugging or web clients
    handleFederationTransaction(w, r)
}
