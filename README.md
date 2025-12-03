# OwnWorld: A Self-Hosted Space-Based Simple Game Server

OwnWorld is a **self-hosted, space-based simple game server** designed to be easily configurable and federated.

---

## Configuration

The server is configured primarily via **environment variables**. These can be set directly in your shell or within a `.env` file.

---

## Federation

To participate in an existing game network, or **federation**, your server must be configured to connect to at least one running seed node.

### Environment Variable

* **`SEED_NODES`**: A **comma-separated list** of seed node URLs.
    * *Example:* `SEED_NODES="http://192.168.1.50:8080,http://ownworld.example.com"`
    * *Default:* Empty (If left empty, the server starts as a **standalone/genesis node**).

---

## Server Options

These environment variables control specific server behavior:

* **`OWNWORLD_DB_FILE`**: Path to the **SQLite database file**.
    * *Default:* `./data/ownworld.db`
* **`OWNWORLD_COMMAND_CONTROL`**: Controls user interaction APIs.
    * Set to `true` (default) to enable user APIs (registration, building).
    * Set to `false` for a resource-only node.
* **`OWNWORLD_PEERING_MODE`**: Defines how the server accepts peer connections.
    * `promiscuous` (**default**): Accepts connections from any valid peer.
    * `strict`: Only accepts connections from peers in a whitelist (not yet fully implemented).

---

## Running

### Standalone (Genesis Node)

Run the following command without setting `SEED_NODES`:

```bash
go run .
```

## Joining a Federation

```bash
SEED_NODES="http://primary-node-ip:8080" go run .
```
