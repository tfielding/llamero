# signal-ollama

[![ci](https://github.com/tfielding/signal-ollama/actions/workflows/ci.yml/badge.svg)](https://github.com/tfielding/signal-ollama/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Chat with your own Ollama models over Signal. A small Go service that sits next to
[bbernhard/signal-cli-rest-api](https://github.com/bbernhard/signal-cli-rest-api) and
answers direct messages from an allowlist of phone numbers.

```
Signal app ⇄ signal-cli-rest-api (json-rpc) ⇄ signal-ollama ⇄ Ollama
                 ws /v1/receive                          /api/chat
                 POST /v2/send
```

## What it answers (and what it ignores)

| Message | Behavior |
|---|---|
| Direct message from a number in `ALLOWED_SENDERS` | Answered |
| Messages in group chats | Ignored |
| Direct message from anyone else | Ignored (logged) |
| Messages the account sends itself (alerts, sync messages) | Ignored |

It only reads from the receive websocket and sends direct replies, so other services that
use the same signal-cli-rest-api account to *send* (alerts etc.) are unaffected.

## Features

- Per-chat conversation memory, saved to disk (`DATA_DIR`): the last `HISTORY_LIMIT` messages, plus a
  running summary of older ones (see [Long chats](#long-chats))
- Typing indicator while the model is thinking, and a 👀 reaction on your message until the reply is sent
- Swipe-to-reply works: when you reply to an earlier message, the model sees which one (up to 1000 characters of it)
- Markdown from the model is converted to Signal formatting (bold, italic, monospace); headings become bold lines, tables become label/value lines
- Commands: `/models`, `/models <name>`, `/retry` (answer your last message again, replacing the previous answer), `/reset`, `/summary` (what it remembers from older messages), `/status` (model, thinking, memory, whether Ollama is online, what is running now; answers instantly even mid-reply), `/think` (reasoning on/off or low/medium/high, per chat), `/stop` (cancel the reply in progress and anything waiting), `/stats` (speed of the last reply), `/settings` (current configuration, with tokens and phone numbers hidden), `/knowledge` (which notes are loaded), `/wake` (wake the Ollama machine), `/help`
- Messages from one person are answered in order; different people don't block each other
- Reconnects to Signal automatically
- Optional tools from MCP servers (e.g. camera snapshots); images that tools return are sent to you as attachments
- A knowledge folder of Markdown notes the model always has (people, cars, routines), plus notes per MCP
  server; edits apply from the next message (see [Knowledge](#knowledge))
- If Ollama is unreachable (e.g. the machine is asleep), replies right away saying so instead of waiting for a timeout,
  or wakes the machine first with Wake-on-LAN (see [Wake-on-LAN](#wake-on-lan))

## Requirements

- signal-cli-rest-api with `MODE=json-rpc` and a registered number
- Ollama reachable from the container. If Ollama runs on another machine, make it listen
  on the network: `OLLAMA_HOST=0.0.0.0` (macOS app: `launchctl setenv OLLAMA_HOST 0.0.0.0`,
  then restart Ollama).

## Configuration

Settings come from environment variables, from an optional config file, or both:

- **Config file** (optional): `/config/config.yml`, or the path in `CONFIG_FILE`. Copy
  [`config.example.yml`](config.example.yml) to start. It's the easier way to set up several MCP
  servers, and the only way to give each server its own `allow_write` and `tools`.
  `${VAR}` in it is filled in from the environment, so tokens and phone numbers can stay in your
  `.env`. Inside `{ }` or `[ ]` on one line, put it in quotes: `"${VAR}"`.
- **Environment variables** win over the file, setting by setting. `MCP_SERVERS` replaces the
  file's whole server list; `MCP_TOKEN_<NAME>` replaces one server's token.
- **Checked at startup:** a missing required setting, an invalid value, an unknown key in the file
  (e.g. a typo), a `${VAR}` that isn't set, or a `CONFIG_FILE` that doesn't exist stops
  signal-ollama with a message naming the setting (and its line in the file). The file is read
  once at startup; restart to apply changes. (The [knowledge folder](#knowledge) is different: it's
  read again whenever it changes.)
- `/settings` shows the effective settings and which config file was read.

| Variable | config.yml | Default | |
|---|---|---|---|
| `SIGNAL_NUMBER` | `signal.number` | required | The number registered in signal-cli-rest-api |
| `ALLOWED_SENDERS` | `signal.allowed_senders` | required | Comma-separated phone numbers (or Signal UUIDs) |
| `SIGNAL_URL` | `signal.url` | `http://signal-cli-rest-api:8080` | |
| `OLLAMA_URL` | `ollama.url` | `http://localhost:11434` | |
| `OLLAMA_MODEL` | `ollama.model` | first installed model | Default model; each chat can switch with `/models` |
| `HISTORY_LIMIT` | `history.limit` | `40` | Messages kept per chat |
| `HISTORY_SUMMARY` | `history.summary` | `true` | Summarize messages past `HISTORY_LIMIT` instead of dropping them |
| `SUMMARY_MODEL` | `history.summary_model` | the chat's model | Model that writes summaries, e.g. a small fast one |
| `SYSTEM_PROMPT` | `system_prompt` | short, phone-friendly assistant prompt | Overrides the built-in prompt (including its formatting rules) |
| `OLLAMA_TIMEOUT` | `ollama.timeout` | `5m` | |
| `REACTION` | `reaction` | `👀` | Reaction shown on your message while it's being answered. `off` to disable |
| `OLLAMA_WOL_MAC` | `ollama.wake_on_lan.mac` | none | MAC address of the Ollama machine. Turns on Wake-on-LAN |
| `OLLAMA_WOL_ADDR` | `ollama.wake_on_lan.addr` | `255.255.255.255:9` | Where to send the wake-up. From Docker, use your LAN's broadcast address, e.g. `192.168.1.255:9` |
| `OLLAMA_WOL_WAIT` | `ollama.wake_on_lan.wait` | `60s` | How long to wait for Ollama after a wake-up |
| `CONFIG_FILE` | | `/config/config.yml` | Config file to read. If set, the file must exist; if not set, a missing default file is fine |
| `DATA_DIR` | `data_dir` | `/data` | Chat history (one JSON file per chat) |
| `KNOWLEDGE_DIR` | `knowledge.dir` | `/config/knowledge` | Folder of Markdown notes for the model (see [Knowledge](#knowledge)). Missing folder = no notes |
| `KNOWLEDGE_MAX_CHARS` | `knowledge.max_chars` | `8000` | Max characters the notes add to every request (about 2k tokens) |
| `MCP_SERVERS` | `mcp.servers.&lt;name&gt;.url` | none | MCP servers to get tools from: `name=url,...`, e.g. `cams=http://go2rtc-mcp:8090/mcp` |
| `MCP_TOKEN_<NAME>` | `mcp.servers.&lt;name&gt;.token` | none | Bearer token for server `<name>` (uppercased, `-` and `.` become `_`), e.g. `MCP_TOKEN_CAMS` |
| `MCP_TOOLS` | `mcp.tools` | all | Optional allowlist of tool names, e.g. `list_cameras,get_snapshot` |
| `MCP_ALLOW_WRITE` | `mcp.allow_write` | `false` | Tools not marked read-only: `false` hides them, `ask` offers them but asks you in Signal before each call, `true` runs them without asking. In the file, each server can set its own (`mcp.servers.<name>.allow_write`) |
| | `mcp.servers.<name>.allow_write` | `mcp.allow_write` | Per server: `false`, `ask` or `true` |
| | `mcp.servers.<name>.tools` | all | Per server: only these of its tools (names as the server gives them) |
| | `mcp.servers.<name>.knowledge` | `<name>.md` if it exists | Per server: notes file in the [knowledge folder](#knowledge) for its tools. Must exist |
| `MCP_MAX_RESULT_CHARS` | `mcp.max_result_chars` | `32000` | Max characters of tool output passed to the model (about 8k tokens). Make sure Ollama's context window can hold it |

## Run

With Docker Compose, add the service from `docker-compose.example.yml` next to your
existing `signal-cli-rest-api` service (use `image: ghcr.io/tfielding/signal-ollama:latest`
or `build:`). Like Frigate, it uses two folders: `/config` for what you write (the knowledge
notes; mounted read-only) and `/data` for what the bot writes (chat history). The image runs as a
non-root user (uid 65532), so create them first:

```bash
sudo mkdir -p /opt/signal-ollama/config/knowledge /opt/signal-ollama/data
sudo chown 65532:65532 /opt/signal-ollama/data
docker compose up -d signal-ollama
```

Notes in `config/knowledge` only need to be readable by everyone (the default for files you create).

Use `SIGNAL_URL=http://signal-cli-rest-api:8080` (the service name), not `localhost`:
inside a container, `localhost` is the container itself.

The typing indicator only shows if "Typing indicators" is on in your Signal app
(Settings → Privacy).

Locally:

```bash
SIGNAL_URL=http://<signal-host>:8080 SIGNAL_NUMBER=+1... ALLOWED_SENDERS=+1... \
OLLAMA_URL=http://localhost:11434 DATA_DIR=./data go run .
```

## Knowledge

Put Markdown files in the knowledge folder (`/opt/signal-ollama/config/knowledge` with the compose
example) to tell the model things it can't look up:

```
config/knowledge/
  home.md      always given: people, cars, routines, preferences
  ha.md        given with the Home Assistant tools
  frigate.md   given with the frigate tools
```

- A server's notes file is named in `config.yml` with `knowledge:`, and must exist (checked at
  startup):

  ```yaml
  mcp:
    servers:
      ha:
        url: http://homeassistant:8123/api/mcp
        knowledge: ha.md
  ```

  Without `knowledge:` (or with `MCP_SERVERS`), the file named after the server is used if it's
  there: `frigate.md` for the server `frigate`. Several servers can name the same file.
- A server's notes are added to its tool notes, and only when its tools are offered.
- Every other `.md` file (not used by any server) is always given to the model, in name order,
  after the system prompt.
- Files are read again when they change: edits apply from the next message, no restart.
- Everything here is sent with every message, so keep it short: small models have small context
  windows, and long notes push out chat history. The total is capped at `KNOWLEDGE_MAX_CHARS`
  (general notes first, then per-server ones); anything past it is cut off and logged.
- `/knowledge` lists the files, their size and when each is used. `/status` shows the total.
- The notes only go to your Ollama server.

Example `home.md`:

```markdown
## Home
- People: Alex and Jordan. Our car: blue hatchback (plate XYZ 789).
- Deliveries usually come to the porch between 10:00 and 16:00.

## Cameras
- driveway faces the street; cars passing on the road aren't visitors.
```

## Long chats

Each chat keeps its last `HISTORY_LIMIT` messages word for word. When a chat goes past that, the
older messages are summarized by the model and dropped; the newest half of the limit is kept. The
summary is updated the same way each time, saved in the chat's file in `DATA_DIR`, and given to the
model with every message.

- Summaries are written after the reply is sent, so they never delay an answer. A message sent in
  the meantime waits until the summary is done; `/status` shows "updating memory" and `/stop` cancels it.
- If a summary fails (Ollama offline, error), the messages are kept and it's tried again after the
  next reply. If it keeps failing, the oldest messages are dropped at twice `HISTORY_LIMIT`.
- `/summary` shows what it remembers; `/reset` clears the summary too.
- `HISTORY_SUMMARY=false` turns this off: older messages are simply dropped.

## Approving actions

With `MCP_ALLOW_WRITE=ask`, tools that can change things (lights, locks, alarms) are offered to the
model, but each call waits for your OK:

```
Allow switch_off?
• name: kitchen light
Reply yes to run it, anything else cancels (2 min).
```

- The arguments shown are exactly what will be sent. If the model passed a value the tool doesn't
  take, it's listed as left out, since that can make an action broader than you meant.
- Reply **yes** (or ok, sure, 👍) to run it. Any other reply cancels it, and the model sees what you
  wrote, so "no, the bedroom one" works. No answer within 2 minutes cancels it too. After a
  timeout or a second "no", the model doesn't try any more actions for that message.
- Commands still work meanwhile: `/status` shows what's waiting, `/stop` cancels the whole reply.
- If an approved call fails and the model tries again, the next prompt starts with
  "The last try failed: …" so you can see why.
- Read-only tools never ask. Combine with `MCP_TOOLS` to offer only the actions you want.

## Wake-on-LAN

If the Ollama machine sleeps, set `OLLAMA_WOL_MAC` to its MAC address. When a message arrives and
Ollama is unreachable, the bot sends a wake-up (a Wake-on-LAN magic packet), replies "Ollama is
asleep, waking it up…", waits up to `OLLAMA_WOL_WAIT` for Ollama to answer, then replies as usual.
`/status` shows the wait and `/stop` cancels it. `/wake` sends a wake-up without asking anything.

- The machine has to allow it. macOS: System Settings → Energy (or Battery → Options) → "Wake for
  network access". Linux/Windows: enable Wake-on-LAN in the BIOS and on the network card. Wired
  Ethernet is the most reliable; Wi-Fi often can't wake a sleeping machine.
- Docker: a container on a bridge network can't send to `255.255.255.255`. Set `OLLAMA_WOL_ADDR` to
  your LAN's broadcast address (e.g. `192.168.1.255:9`), or run the container with `network_mode: host`.
- If Ollama doesn't answer in time, you get the usual "can't reach Ollama" reply.
- If `OLLAMA_MODEL` isn't set, the bot needs Ollama at startup to pick a model; it sends a wake-up
  then too (and every 5 minutes while it waits).

## Tools (MCP)

signal-ollama can use tools from any [MCP](https://modelcontextprotocol.io) server that speaks
Streamable HTTP. It has no tool-specific code: point `MCP_SERVERS` at a server and its tools are
offered to the model.

- Only tools marked **read-only** by the server are offered, unless `MCP_ALLOW_WRITE` is `ask` or `true`.
  Anything that can change things (locks, alarms, settings) stays hidden by default.
- Tools are only offered to models that support tool calling (Ollama reports this per model).
- Images returned by a tool are sent to you as Signal attachments with the reply. If the current
  model supports vision, it sees them too; otherwise it's told an image was sent.
- The model can make up to 6 rounds of tool calls per message. `/stop` cancels tool calls too.
- Usage notes are added to the model's instructions per server: whatever the server itself sends
  when the bot connects (MCP "instructions"), plus `<name>.md` from the
  [knowledge folder](#knowledge). Tool-specific advice belongs there, not in the code.
- If a read-only tool call fails because of optional parameters the model guessed, it is retried
  once without them. Tools that can change things are never retried this way.
- `/status` lists the available tools and any server that's unreachable. Tool lists are refreshed
  every minute, and sessions reconnect automatically if a server restarts.

## Notes

- signal-cli-rest-api has no authentication. Keep port 8080 off the internet; ideally don't
  publish it at all and let containers talk over the Docker network.
- In json-rpc mode, messages that arrive while signal-ollama is down are not replayed.
- Chat history is stored unencrypted in `DATA_DIR`.

## Install without Docker

```bash
go install github.com/tfielding/signal-ollama@latest
signal-ollama -version
```

## Develop

```bash
go test -race ./...
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full set of checks CI runs, and
[SECURITY.md](SECURITY.md) for reporting vulnerabilities. Changes are listed in
[CHANGELOG.md](CHANGELOG.md).

## License

MIT, see [LICENSE](LICENSE).

signal-ollama is an independent project, not affiliated with or endorsed by the Signal
Technology Foundation or Ollama Inc. Signal and Ollama are trademarks of their respective owners.
