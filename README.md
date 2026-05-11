# trilium-mcp

An [MCP](https://modelcontextprotocol.io) server that lets AI agents (Claude Desktop, Claude Code, any MCP-compatible client) read and write a self-hosted [TriliumNext](https://github.com/TriliumNext/Notes) knowledge base over its [ETAPI](https://github.com/TriliumNext/Notes/wiki/ETAPI).

Single static Go binary. No runtime dependencies. Talks to your local Trilium over HTTP(S) and to the client over stdio.

## Why

TriliumNext is a strong personal KB: tree-of-notes with attributes (labels, relations) that double as table columns / board lanes / calendar events. This MCP exposes the right slice of ETAPI so an agent can:

- Capture stuff into your notes (reading lists, decisions, research dumps).
- Maintain structured "tables" by creating notes-as-rows under a parent and tagging them with labels-as-columns.
- Search your existing KB and feed snippets back into a conversation.

It is intentionally minimal: ten tools, ~600 lines of Go, zero clever abstractions.

## Tools

| Tool | Purpose |
| --- | --- |
| `create_note` | Create a note (optionally under a parent, with labels in one shot). |
| `get_note` | Fetch note metadata; optionally include body content. |
| `update_note` | Replace title, type, or body. Omit a field to leave it; pass empty string to clear. |
| `append_content` | Append text to the body with a configurable separator. |
| `delete_note` | Delete a note and its subtree. |
| `search_notes` | Full-power Trilium search (`#label`, `~relation`, `note.title %= "regex"`, ancestor scoping, etc.). |
| `add_label` | Attach a label (`#key=value`) — acts as a "column" in collection views. |
| `add_relation` | Attach a relation (`~name → noteId`) — like a foreign key between notes. |
| `remove_attribute` | Remove a label or relation by its attribute id. |
| `list_attributes` | List all labels and relations on a note. |

## Quick start

### 1. Run TriliumNext

If you don't already have one:

```yaml
# docker-compose.yml
services:
  trilium:
    image: triliumnext/notes:latest
    ports:
      - "8092:8080"
    volumes:
      - ./data:/home/node/trilium-data
```

```bash
docker compose up -d
```

Open `http://localhost:8092/`, finish the setup wizard, then **Options → ETAPI → Create new ETAPI token**. Copy the token (shown only once).

### 2. Install trilium-mcp

**Pre-built binary** (recommended) — grab the right archive from [Releases](https://github.com/OVDEN13/trilium-mcp/releases).

**From source** with Go 1.23+:

```bash
go install github.com/OVDEN13/trilium-mcp@latest
```

**With Docker** (no Go on host):

```bash
git clone https://github.com/OVDEN13/trilium-mcp && cd trilium-mcp
docker build -t trilium-mcp .
```

### 3. Configure

Copy `.env.example` to `.env` next to the binary:

```env
TRILIUM_URL=http://localhost:8092
TRILIUM_TOKEN=your-etapi-token-here
# Optional:
# TRILIUM_HTTP_TIMEOUT_SECONDS=30
```

Or pass the same as real environment variables — the server reads either.

### 4. Register with your MCP client

**Claude Code** (CLI):

```bash
claude mcp add --scope user trilium /path/to/trilium-mcp \
  --env TRILIUM_URL=http://localhost:8092 \
  --env TRILIUM_TOKEN=your-token
```

**Claude Desktop** — add to `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or the equivalent on your OS:

```json
{
  "mcpServers": {
    "trilium": {
      "command": "/absolute/path/to/trilium-mcp",
      "env": {
        "TRILIUM_URL": "http://localhost:8092",
        "TRILIUM_TOKEN": "your-token"
      }
    }
  }
}
```

Restart the client. The ten tools should show up as `trilium__*`.

## Usage patterns

### "Database" of notes (the killer feature)

Trilium's collection views (Table / Board / Calendar) render any note's children based on shared labels. So a "table" is just a parent note + child notes + a consistent label schema:

```
Books (parent)
├── "Atomic Habits"   #status=read    #rating=9   #author=Clear
├── "Antifragile"     #status=read    #rating=8   #author=Taleb
└── "Деньги"          #status=reading             #author=Жонсон
```

An agent populates it like this:

```jsonc
// 1. Create the row
create_note({
  parent_note_id: "<id of Books>",
  title: "Atomic Habits",
  labels: { "status": "read", "rating": "9", "author": "Clear" }
})

// 2. Query rows later
search_notes({ query: "#status=read #rating>=8", ancestor_note_id: "<id of Books>" })
```

Flip the parent's view to **Table** (or **Board** by `status`, or **Calendar** by a date label) in the Trilium UI and you have a database without ever leaving notes.

### Append-only log

```jsonc
append_content({ note_id: "<journal id>", content: "Decided to ship v0.2 on Monday." })
```

`append_content` is non-destructive — handy for daily journals, decision logs, ideation dumps.

## Trilium search cheat sheet

- `#tag` — note has label `tag`.
- `#status=active` — label equals.
- `#rating>=8` — numeric comparison.
- `~author.title *= "Clear"` — follow a relation, match relation target's title.
- `note.title %= "^Re:"` — regex on title.
- `note.content *= "kubernetes"` — substring in body.
- `#status=active OR #status=pending` — boolean.
- Combine with `ancestor_note_id` to scope to a subtree.

Full reference: [Trilium search docs](https://github.com/TriliumNext/Notes/wiki/Search).

## Environment variables

| Var | Default | Notes |
| --- | --- | --- |
| `TRILIUM_URL` | *required* | e.g. `http://localhost:8092` or `https://memo.example.com` |
| `TRILIUM_TOKEN` | *required* | ETAPI token from Trilium settings |
| `TRILIUM_HTTP_TIMEOUT_SECONDS` | `30` | Per-request timeout |

## Building from source

```bash
git clone https://github.com/OVDEN13/trilium-mcp
cd trilium-mcp
go build -ldflags="-s -w" -o trilium-mcp .
```

Cross-compile (e.g. for macOS from Linux):

```bash
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o trilium-mcp-darwin-arm64 .
```

## Security notes

- The server reads `TRILIUM_TOKEN` from env. Treat it like a password — anyone with it can read and write your entire KB. Keep `.env` out of git (it is in `.gitignore`).
- The binary speaks **only** to your configured Trilium URL. It does not phone home, log to disk, or open any listening ports.
- HTTPS works automatically (the binary ships with system CAs when run from the host; the Docker image includes `ca-certificates`).

## Contributing

PRs welcome. Useful directions:

- Stream large note bodies instead of buffering.
- `move_note` / `clone_note` tools.
- Bulk operations (`add_label_to_many`).
- ETAPI v2 features as TriliumNext adds them.
- Tests against an ephemeral TriliumNext container.

For substantive changes, please open an issue first to discuss the shape.

## License

[MIT](./LICENSE).

`trilium-mcp` is an independent project; it is not endorsed by or affiliated with the TriliumNext project. TriliumNext itself is AGPL-3.0; this MCP server talks to it only over its public ETAPI.
