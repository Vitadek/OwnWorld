OwnWorld Galaxy Engine

OwnWorld is a distributed, federated MMO strategy game engine designed for high-concurrency simulations on low-power hardware (Raspberry Pi/ARM).

It operates as a Layer 3 Application-Specific Blockchain, using event sourcing and cryptographic verification to maintain a shared galaxy state across thousands of independent servers.
Key Features

    Federated Architecture: Every player runs their own server ("World"). Worlds connect via HTTP/2 to form a Galaxy.

    Event Sourcing: All game actions are stored in an immutable transaction_log. State is verifiable and replayable.

    Scarcity Economy: Resource efficiency is procedurally generated based on BLAKE3 hashes of planet IDs. Players must trade to survive.

    Optimized for ARM: Uses WAL Mode SQLite, LZ4 Compression, and TDMA Staggering to run 100+ worlds on a single Raspberry Pi 4.

    Trustless Security: Fleet movements and trades are signed with Ed25519 keys.

Getting Started
Prerequisites

    Docker & Docker Compose

    OR Go 1.22+ (for manual build)

Quick Start (Docker)

Spin up a local 2-node galaxy (1 Seed, 1 Peer):

docker-compose up --build -d

    Seed Node: http://localhost:8080

    Player Node: http://localhost:8081

Manual Build

# Install dependencies
go mod tidy

# Build
go build -o ownworld .

# Run (Standalone)
./ownworld

Configuration

Configure your node using Environment Variables:
Variable	Default	Description
FEDERATION_NAME	(Empty)	Name of the Galaxy. Nodes with matching names/hashes can peer.
SEED_NODES	(Empty)	Comma-separated list of URLs to bootstrap from (e.g. http://seed.net:8080).
OWNWORLD_COMMAND_CONTROL	true	If false, disables User APIs (/register, /login). Runs as a headless "Resource Node".
OWNWORLD_PEERING_MODE	promiscuous	strict = Only accept peers in whitelist. promiscuous = Accept valid Genesis hashes.
FEDERATION_KEY	(Empty)	Optional shared secret for admin sync endpoints.
API Endpoints
Client API (Human)

    POST /api/register: Create a new account and spawn a Colony.

    GET /api/state: Fetch your current colonies, fleets, and credits.

    POST /api/fleet/launch: Send a fleet to another system.

    POST /api/bank/burn: Exchange raw resources for Credits (Scarcity Pricing).

Federation API (Robot)

    POST /federation/handshake: Peer discovery and verification.

    POST /federation/transaction: Protobuf endpoint for high-speed fleet/trade synchronization.

    GET /federation/map: Lightweight, cached JSON map of the known galaxy.

Architecture

    Core: Go (Golang)

    DB: SQLite (WAL Mode) + sync.Pool buffer reuse.

    Crypto: BLAKE3 (Hashing), Ed25519 (Signatures), LZ4 (Compression).

    Consensus: Weighted Election (Tick Height + Peer Count) with TDMA time-slot staggering.

License

MIT
