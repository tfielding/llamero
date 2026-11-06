# signal-ollama

[![ci](https://github.com/tfielding/signal-ollama/actions/workflows/ci.yml/badge.svg)](https://github.com/tfielding/signal-ollama/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Chat with your own Ollama models over Signal. A small Go service that sits next to
[bbernhard/signal-cli-rest-api](https://github.com/bbernhard/signal-cli-rest-api) and answers
direct messages from an allowlist of phone numbers, optionally with tools from MCP servers
(cameras, Home Assistant, ...).

```
Signal app ⇄ signal-cli-rest-api (json-rpc) ⇄ signal-ollama ⇄ Ollama
                 ws /v1/receive                     ⇅     /api/chat
                 POST /v2/send                  MCP servers (optional)
```

## Features

- Conversation memory per chat, saved to disk: recent messages word for word, plus a running
  summary of older ones (see [Long chats](#long-chats))
- Typing indicator and a 👀 reaction on your message while it's being answered
- Swipe-to-reply works: the model sees which message you replied to
- Markdown from the model becomes Signal formatting (bold, italic, monospace); headings become bold
  lines, tables become label/value lines
- [Send it photos](#photos) and ask about them; a vision model answers when the chat's model can't see
- Tools from MCP servers; pictures they return are sent to you as attachments (see [Tools](#tools-mcp))
- A [knowledge folder](#knowledge) of notes the model always has: people, routines, how to use each tool
- [Scheduled checks](#schedules) set up by just asking: "every weekday at 7:30 tell me what happened
  overnight on the cameras", "remind me at 5pm to check the porch"
- [Hooks](#hooks): other systems (Home Assistant, a script) send events to `POST /hooks/<name>`,
  the model looks into them with your tools, and you hear about the ones that matter
- Wakes a sleeping Ollama machine with [Wake-on-LAN](#wake-on-lan), or says right away that it's offline
- Messages from one person are answered in order; different people don't block each other

## Commands

| Command | What it does |
|---|---|
| `/models` | List the installed models, marking the one this chat uses |
| `/models <name>` | Switch this chat to another model |
| `/think [setting]` | Show or change reasoning for this chat: `on`, `off`, `low`, `medium`, `high` or `default` (what the model supports) |
| `/retry` | Answer your last message again, replacing the previous answer (or the one that failed) |
| `/stop` | Cancel the reply in progress, and any messages waiting behind it |
| `/reset` | Forget this conversation, including the summary |
| `/summary` | What it remembers from older messages |
| `/status` | Model, thinking, memory, knowledge, whether Ollama is online, available tools, what's running now |
| `/stats` | Speed of the last reply |
| `/knowledge` | Which notes are loaded from the knowledge folder, and when each is used |
| `/schedules` | Your [scheduled checks](#schedules), with their numbers and next run |
| `/schedules delete\|pause\|resume\|run <n>` | Change one schedule, or run it now |
| `/hooks` | The latest [hook](#hooks) checks and what each decided (sent, nothing to report and why, skipped while cooling down), since the bot started |
| `/learned` | What [hooks](#hooks) learned from your feedback on 🔔 messages |
| `/learned clear <hook>` | Forget what one hook learned |
| `/settings` | The settings in use, with tokens and phone numbers hidden |
| `/wake` | Wake the Ollama machine (Wake-on-LAN) |
| `/help` | This list |

`/status`, `/stats`, `/stop`, `/summary`, `/knowledge`, `/schedules`, `/hooks`, `/learned` and `/settings` answer
right away, even while a reply is being written.

## What it answers

| Message | Behavior |
|---|---|
| Direct message from an allowed sender | Answered |
| Messages in group chats | Ignored |
| Direct message from anyone else | Ignored (logged) |
| Messages the account sends itself (alerts, sync messages) | Ignored |

It only reads from the receive websocket and sends direct replies, so other services that use the
same signal-cli-rest-api account to *send* (alerts etc.) are unaffected.

## Requirements

- signal-cli-rest-api with `MODE=json-rpc` and a registered number
- Ollama reachable from the container. If Ollama runs on another machine, make it listen on the
  network: `OLLAMA_HOST=0.0.0.0` (macOS app: `launchctl setenv OLLAMA_HOST 0.0.0.0`, then restart
  Ollama).

## Run

Like Frigate, signal-ollama uses two folders: `config` for what you write (mounted read-only) and
`data` for what the bot writes.

```
/opt/signal-ollama/
├── config/                 yours; the bot only reads it
│   ├── config.yml          settings (optional, see Configuration)
│   └── knowledge/          notes for the model (optional, see Knowledge)
└── data/                   chat history, one file per chat; owned by the bot
```

The image runs as a non-root user (uid 65532) that needs to write to `data`:

```bash
sudo mkdir -p /opt/signal-ollama/config/knowledge /opt/signal-ollama/data
sudo chown 65532:65532 /opt/signal-ollama/data
```

Add the service next to your existing `signal-cli-rest-api` service, so they share a Docker network
(see also [`docker-compose.example.yml`](docker-compose.example.yml)):

```yaml
  signal-ollama:
    image: ghcr.io/tfielding/signal-ollama:latest
    restart: always
    environment:                # values for the ${...} in config.yml, from your .env
      - TZ=America/New_York     # your time zone, for schedules and times
      - SIGNAL_NUMBER=${SIGNAL_NUMBER}
      - ALLOWED_SENDERS=${ALLOWED_SENDERS}
    volumes:
      - /opt/signal-ollama/config:/config:ro
      - /opt/signal-ollama/data:/data
    healthcheck:                # optional: see Health
      test: ["CMD", "/signal-ollama", "-healthcheck"]
      interval: 60s
      timeout: 10s
      start_period: 30s
    depends_on:
      - signal-cli-rest-api
```

```bash
docker compose up -d signal-ollama
```

Then send `/status` from your phone. If it doesn't start, `docker logs signal-ollama` names the
setting that's wrong.

- Use `http://signal-cli-rest-api:8080` (the service name) for Signal, not `localhost`: inside a
  container, `localhost` is the container itself.
- The typing indicator only shows if "Typing indicators" is on in your Signal app
  (Settings → Privacy).

Locally, without Docker:

```bash
SIGNAL_URL=http://<signal-host>:8080 SIGNAL_NUMBER=+1... ALLOWED_SENDERS=+1... \
OLLAMA_URL=http://localhost:11434 DATA_DIR=./data go run .
```

## Configuration

Settings come from an optional config file, environment variables, or both.

- **Config file:** `/config/config.yml`, or the path in `CONFIG_FILE`. Copy
  [`config.example.yml`](config.example.yml) to start:

  ```yaml
  signal:
    number: ${SIGNAL_NUMBER}
    allowed_senders: ${ALLOWED_SENDERS}
  ollama:
    url: http://192.168.1.50:11434
    model: llama3.2:latest
  mcp:
    servers:
      cams:
        url: http://go2rtc-mcp:8090/mcp
      ha:
        url: http://homeassistant:8123/api/mcp
        token: ${HA_TOKEN}
        allow_write: ask
        knowledge: ha.md
  ```

  `${VAR}` is filled in from the container's environment, so secrets can stay in your `.env`
  (pass them in with `environment:`). Inside `{ }` or `[ ]` on one line, quote it: `"${VAR}"`.
- **Environment variables** win over the file, setting by setting. `MCP_SERVERS` replaces the
  file's whole server list.
- **Checked at startup:** a missing required setting, an invalid value, an unknown key (e.g. a
  typo), an unset `${VAR}` or a missing knowledge file stops signal-ollama with a message naming
  the setting and its line. The file is read once; restart to apply changes.

| Variable | config.yml | Default | |
|---|---|---|---|
| `SIGNAL_NUMBER` | `signal.number` | required | The number registered in signal-cli-rest-api |
| `ALLOWED_SENDERS` | `signal.allowed_senders` | required | Phone numbers (or Signal UUIDs) allowed to use the bot; comma-separated, or a list in the file |
| `SIGNAL_URL` | `signal.url` | `http://signal-cli-rest-api:8080` | |
| `OLLAMA_URL` | `ollama.url` | `http://localhost:11434` | |
| `OLLAMA_MODEL` | `ollama.model` | first installed model | Default model; each chat can switch with `/models` |
| `OLLAMA_VISION_MODEL` | `ollama.vision_model` | none | Answers messages with [photos](#photos) when the chat's model can't see, e.g. `gemma3:4b` |
| `OLLAMA_TIMEOUT` | `ollama.timeout` | `5m` | |
| `OLLAMA_WOL_MAC` | `ollama.wake_on_lan.mac` | none | MAC address of the Ollama machine; turns on [Wake-on-LAN](#wake-on-lan) |
| `OLLAMA_WOL_ADDR` | `ollama.wake_on_lan.addr` | `255.255.255.255:9` | Where to send the wake-up. From Docker, your LAN's broadcast address, e.g. `192.168.1.255:9` |
| `OLLAMA_WOL_WAIT` | `ollama.wake_on_lan.wait` | `60s` | How long to wait for Ollama after a wake-up |
| `HISTORY_LIMIT` | `history.limit` | `40` | Messages kept word for word per chat |
| `HISTORY_SUMMARY` | `history.summary` | `true` | Summarize older messages instead of dropping them |
| `SUMMARY_MODEL` | `history.summary_model` | the chat's model | Model that writes summaries, e.g. a small fast one |
| `SYSTEM_PROMPT` | `system_prompt` | short, phone-friendly prompt | Replaces the built-in prompt, including its formatting rules |
| `REACTION` | `reaction` | `👀` | Reaction on your message while it's answered; `off` to disable |
| `MCP_SERVERS` | `mcp.servers.<name>.url` | none | MCP servers to get tools from. Env: `name=url,...`, e.g. `cams=http://go2rtc-mcp:8090/mcp` |
| `MCP_TOKEN_<NAME>` | `mcp.servers.<name>.token` | none | Bearer token for a server. Env name is uppercased, `-` and `.` become `_`, e.g. `MCP_TOKEN_HA` |
| `MCP_ALLOW_WRITE` | `mcp.allow_write` | `false` | Tools that can change things: `false` hides them, `ask` asks you before each call, `true` runs them. The default for servers without their own |
| | `mcp.servers.<name>.allow_write` | `mcp.allow_write` | The same, for one server |
| | `mcp.servers.<name>.tools` | all | Only these of the server's tools |
| | `mcp.servers.<name>.knowledge` | `<name>.md` if present | The server's notes file in the [knowledge folder](#knowledge); must exist |
| `MCP_TOOLS` | `mcp.tools` | all | Only these tools, across all servers |
| `MCP_MAX_RESULT_CHARS` | `mcp.max_result_chars` | `32000` | Max characters of tool output given to the model (about 8k tokens); Ollama's context window must hold it |
| `HEALTH_ADDR` | `health.addr` | `:8092` | Where [`GET /healthz`](#health) listens; `off` to disable |
| `SCHEDULES` | `schedules.enabled` | `true` | [Scheduled checks](#schedules): the `create_schedule` tool and `/schedules` |
| `WEBHOOK_TOKEN` | `webhooks.token` | none | Token [hook](#hooks) callers send as `Authorization: Bearer <token>`; at least 16 characters, required with hooks |
| | `webhooks.hooks.<name>` | none | A [hook](#hooks) at `POST /hooks/<name>`: `task`, `only_if`, `to`, `wake`, `group`, `cooldown` |
| `TZ` | | UTC | Time zone for schedules, e.g. `America/New_York` |
| `KNOWLEDGE_DIR` | `knowledge.dir` | `/config/knowledge` | Folder of notes for the model |
| `KNOWLEDGE_MAX_CHARS` | `knowledge.max_chars` | `8000` | Max characters the notes add to every message (about 2k tokens) |
| `DATA_DIR` | `data_dir` | `/data` | Chat history |
| `CONFIG_FILE` | | `/config/config.yml` | Config file to read. If set, it must exist; the default may be missing |

## Knowledge

Markdown files in the knowledge folder tell the model things it can't look up:

```
config/knowledge/
  home.md      always given: people, routines, preferences
  ha.md        given with the Home Assistant tools (knowledge: ha.md in config.yml)
  frigate.md   given with the frigate tools
```

- **Per server:** a server's file is named with `knowledge:` in `config.yml`. Without it, the file
  named after the server is used if present (`frigate.md` for `frigate`). It's given only when that
  server's tools are offered. Several servers can share a file.
- **Everything else** in the folder is given with every message, after the system prompt.
- **Edits apply from the next message,** no restart. A missing folder just means no notes.
- **Keep it short:** it's sent with every message, and small models have small context windows.
  The total is capped at `KNOWLEDGE_MAX_CHARS`; anything past it is cut off and logged.

Example `home.md`:

```markdown
- People: Alex and Jordan. Our car: blue hatchback.
- Deliveries usually come to the porch between 10:00 and 16:00.
- The driveway camera faces the street; cars passing on the road aren't visitors.
```

## Tools (MCP)

signal-ollama can use tools from any [MCP](https://modelcontextprotocol.io) server that speaks
Streamable HTTP, such as [frigate-mcp](https://github.com/tfielding/frigate-mcp),
[go2rtc-mcp](https://github.com/tfielding/go2rtc-mcp) or Home Assistant's. It has no
tool-specific code: list a server and its tools are offered to the model.

- Only tools marked **read-only** are offered, unless the server's `allow_write` is `ask` or
  `true`. Anything that can change things (locks, alarms, settings) stays hidden by default.
- Tools are only offered to models that support tool calling (Ollama reports this per model).
- Pictures a tool returns are sent to you with the reply. A vision model sees still ones (JPEG,
  PNG) too; clips such as frigate-mcp's animated GIFs are only for you, and the model is told one
  was sent.
- Each server's notes reach the model with its tools: what the server sends itself (MCP
  "instructions"), plus its [knowledge](#knowledge) file.
- Up to 6 rounds of tool calls per message. A read-only call that fails because of optional
  arguments the model guessed is retried once without them.
- `/status` lists the tools and any unreachable server. Lists refresh every minute, and sessions
  reconnect if a server restarts.

### Approving actions

With `allow_write: ask`, each call to a tool that can change things waits for your OK:

```
Allow switch_off?
• name: kitchen light
Reply yes to run it, anything else cancels (2 min).
```

- The arguments shown are exactly what will be sent; any the tool doesn't take are listed as left
  out.
- Reply **yes** (or ok, sure, 👍) to run it. Any other reply cancels it, and the model sees what you
  wrote, so "no, the bedroom one" works. No answer within 2 minutes cancels it.
- After a timeout or a second "no", the model doesn't try more actions for that message.
- If an approved call fails and the model retries, the next prompt says why the last try failed.
- `/status` shows what's waiting; `/stop` cancels the whole reply.

## Schedules

Ask for a check to run later or regularly, in plain words, and the bot sets it up after you
approve it:

```
You: every weekday at 7:30 tell me what happened overnight on the cameras

Create this schedule?
• when: weekdays 07:30 (first: Wed 4 Nov 07:30)
• task: Summarize camera alerts since 22:00 yesterday
Reply yes to save it, anything else cancels (2 min).
```

At 07:30 the model does the task with your tools and the result arrives as a message starting
with ⏰. It joins the conversation, so you can follow up ("show me that one").

- **When:** `every 30m`, `every 2h between 22:00 and 06:00`, `daily 07:30`, `weekdays 07:30`,
  `weekends 10:00`, `mon,wed,fri 18:00`, or once: `at 17:00`, `tomorrow 9am`, `2026-11-10 08:00`.
  At most every 15 minutes, in the `TZ` time zone. One-offs are deleted after they run.
- **Only when it matters:** "tell me *if* the garage door is open" saves an "only if" condition.
  The check then stays silent unless the condition is true.
- **Look, don't touch:** scheduled checks only get read-only tools. Nobody is there to approve an
  action, so a check can report that the garage door is open, but never close it.
- **When a check can't run** (Ollama unreachable, or the model fails), you get a message saying
  which schedule and why. A repeating check reports only the first failure in a row, so a check
  every 30 minutes doesn't message you all night; `/schedules` shows "last run failed" until one
  works again. A schedule created with `wake` wakes the Ollama machine quietly first.
- A check that's overdue by more than 10 minutes, e.g. because the bot was down, is skipped rather
  than run late. Frequent checks keep the Ollama machine awake.
- **Managing them:** ask ("delete the porch check") or use `/schedules`, `/schedules pause 2`,
  `resume`, `delete`, or `run` (run now, even if paused). Up to 20 per chat; saved in
  `data/schedules`.
- Checks wait their turn behind your own messages, and `/stop` cancels one that's running.
- For instant alerts ("someone's at the door"), schedules are the wrong tool: they look every so
  often. Use a [hook](#hooks), which runs the moment something happens.

## Hooks

Hooks let other systems tell the bot something happened. They send an event to
`POST /hooks/<name>`, the model looks into it with your tools, and you get a message starting
with 🔔, or nothing if it doesn't matter. The bot doesn't know or care what sent the event: Home
Assistant, Node-RED, a cron job or a script.

```yaml
# config.yml
webhooks:
  token: ${WEBHOOK_TOKEN}             # callers send "Authorization: Bearer <token>"
  hooks:
    front_door:
      task: Find out who or what it was, with the cameras, and say so in one sentence.
      only_if: a person came to the door, or anyone is there between 23:00 and 06:00
    leak:                             # no task: the event is sent to you as it is
      group: 0
```

An event is a short text, as JSON `{"text": "..."}` or a plain-text body. It can also have a `key`
saying what it's about, e.g. a camera or a sensor (`"key": "..."` in the JSON, or `?key=` in the URL),
for the cooldown:

```bash
curl -H "Authorization: Bearer $WEBHOOK_TOKEN" -H 'Content-Type: application/json' \
  -d '{"text": "Front door opened"}' http://signal-ollama:8092/hooks/front_door
```

- **`task`:** what the model does with the event, using read-only tools, like a
  [scheduled check](#schedules). Put the details the tools need in the event text (an id, a
  camera, a sensor). Without a task, the event is sent to you unchanged and the model isn't used.
- **`only_if`:** report only when this is true; otherwise the bot stays silent. Without it, every
  event gets a message.
- **`to`:** who gets the messages, from `signal.allowed_senders`; default everyone in it. The model
  runs once, in the first one's chat and with their model; the result goes to all of them and
  joins each one's conversation, so "show me that" works afterwards.
- **`group`:** events within this long of the first are checked together in one run (default
  `30s`, at most `10m`, `0` for each on its own), so one person walking past three cameras is one
  message. Up to 10 events are listed per run; more are counted.
- **`cooldown`:** after a message, skip the hook's events with the same `key` for this long (e.g.
  `5m`, at most `24h`; default off), so someone lingering in front of one camera is one message,
  not five. Skipped events aren't checked, so they cost no model time; events with another key
  (another camera) still are. Only a sent message starts a cooldown, not a check that found nothing.
  The catch: a second, different arrival on the same key within the cooldown isn't reported.
- **`wake`:** wake the Ollama machine if it's asleep (needs [Wake-on-LAN](#wake-on-lan)). Without
  it, or if Ollama can't be reached, you get the events unchecked, marked "not checked", rather
  than nothing.
- Hooks are served on the health port, `HEALTH_ADDR` (default 8092). Publish it, or put the caller
  on the same Docker network. Responses: **202** queued, **401** wrong token, **404** no such hook,
  **503** still starting up.
- Runs wait their turn behind your own messages, and `/stop` cancels one that's running.
- `/hooks` shows the latest checks and what each decided, including the model's reason for
  "nothing to report", which helps when tuning `task` and `only_if`.

**Learning from your feedback:** when a 🔔 message is wrong, say so, e.g. by replying to it with
"false alarm, that's the plant". The model saves your verdict with the event and its own answer, and
later checks of that hook get your recent verdicts as examples, so the same mistake is less likely.
You can also say a message was right, or that something should have been reported. Verdicts are
kept per hook (the latest 30) in `data/learned/<hook>.md`, a Markdown file you can read or edit;
`/learned` lists them and `/learned clear <hook>` forgets them. Nothing about the kind of event is
built in: a camera hook learns about false detections, a sensor hook about whatever you correct.

**From Home Assistant:** a `rest_command` that any automation can call:

```yaml
# configuration.yaml
rest_command:
  signal_ollama_hook:
    url: "http://<signal-ollama-host>:8092/hooks/{{ hook }}"
    method: POST
    headers:
      Authorization: !secret signal_ollama_hook_auth   # "Bearer <token>" in secrets.yaml
    content_type: application/json
    payload: '{"text": {{ text | tojson }}, "key": {{ key | default("") | tojson }}}'
```

```yaml
# an automation's action
- action: rest_command.signal_ollama_hook
  data:
    hook: front_door
    text: "{{ trigger.to_state.name }} opened"
    key: "{{ trigger.entity_id }}"                  # optional, for the cooldown
```

## Photos

Send a photo, with or without a question ("what plant is this?", "what does this error mean?"),
and the bot looks at it.

- **Which model sees it:** the chat's model if it can see images. Otherwise `OLLAMA_VISION_MODEL`
  answers that one message, and the chat stays on its model, so you can keep a strong text model
  like gpt-oss for chat and a small vision model for photos. If neither can see, the bot says so
  instead of answering blind.
- **The photo itself isn't kept:** later messages have the reply describing it, so "is it
  poisonous?" works as a follow-up. Send it again to ask about a detail the reply didn't cover.
- Photos are shrunk to at most 1600 pixels on the long side and sent as JPEG (from JPEG, PNG, WebP
  or GIF); up to 4 per message.
- Other files (PDFs, videos, voice notes) aren't read; the bot says so, and answers any text with them.
- signal-cli-rest-api keeps received files in its data folder; signal-ollama doesn't delete them.

## Long chats

Each chat keeps its last `HISTORY_LIMIT` messages word for word. Past that, the older messages are
summarized by the model and dropped, keeping the newest half. The summary is saved with the chat
and given to the model with every message.

- Summaries are written after a reply is sent, so they never delay one. A message sent meanwhile
  waits; `/status` shows "updating memory" and `/stop` cancels it.
- If a summary fails, the messages are kept and it's tried again after the next reply. If it keeps
  failing, the oldest are dropped at twice `HISTORY_LIMIT`.
- `HISTORY_SUMMARY=false` turns this off: older messages are simply dropped.

## Wake-on-LAN

With `OLLAMA_WOL_MAC` set, a message that arrives while Ollama is unreachable sends a wake-up to
its machine. The bot replies "Ollama is asleep, waking it up…", waits up to `OLLAMA_WOL_WAIT`, then
answers as usual. `/stop` cancels the wait; `/wake` sends a wake-up on demand.

- The machine has to allow it. macOS: System Settings → Energy (or Battery → Options) → "Wake for
  network access". Linux/Windows: enable Wake-on-LAN in the BIOS and on the network card. Wired
  Ethernet is the most reliable.
- From Docker on a bridge network, set `OLLAMA_WOL_ADDR` to your LAN's broadcast address (e.g.
  `192.168.1.255:9`), or use `network_mode: host`.
- Without `OLLAMA_MODEL`, the bot needs Ollama at startup to pick a model; it sends a wake-up then
  too, and every 5 minutes while it waits.

## Health

`GET /healthz` on port 8092 says whether the bot is working, for Docker, Uptime Kuma or Home
Assistant:

```json
{
  "status": "ok",
  "version": "1.3.0",
  "uptime": "26h4m10s",
  "signal": { "connected": true, "since": "2026-11-04T09:00:00Z" },
  "ollama": { "reachable": false, "error": "connection refused" },
  "mcp": { "frigate": "ok", "ha": "unavailable (timed out)" }
}
```

- **200** when the bot can receive messages (connected to signal-cli-rest-api), **503** when it
  can't. That's the one to alert on.
- Ollama and MCP servers are reported but don't make it unhealthy: with Wake-on-LAN a sleeping
  Ollama is normal. To be alerted about Ollama too, have your monitor look for
  `"reachable": true` in the body.
- The connection to signal-cli-rest-api is checked with a ping every 30 seconds, so one that died
  quietly is noticed (and reconnected) within a minute.
- **Docker:** the image has no curl, so `signal-ollama -healthcheck` checks its own endpoint and
  exits 0 or 1; see the `healthcheck:` in [Run](#run). `docker ps` then shows the container as
  healthy or unhealthy.
- **Uptime Kuma** on the same Docker network: an HTTP monitor on
  `http://signal-ollama:8092/healthz`. No need to publish the port otherwise.
- The report has no phone numbers, tokens, URLs or addresses; errors are cut to their cause
  ("connection refused", "timed out"), with the details in the bot's log.

## Safety

- **Allowlist only.** Only `ALLOWED_SENDERS` get answers; group chats are ignored.
- **Read-only tools by default.** Tools that can change things are hidden unless a server allows
  them, and with `ask` each call needs your approval. Scheduled checks and hooks only ever get
  read-only tools, and creating or deleting a schedule needs your approval.
- **Hooks need a token.** `POST /hooks/<name>` only accepts requests with `WEBHOOK_TOKEN`, and only
  for hooks in the config file. Event text is shown to the model as an event, never as a message
  from you, and can't unlock actions.
- **No secrets in replies.** `/settings` shows tokens only as "set", masks phone numbers and
  removes credentials from URLs.
- **Local only.** Chats, photos and knowledge notes only go to your Ollama server. Chat history is
  stored unencrypted in `data`; photos aren't stored by signal-ollama.
- Runs as a non-root user in a minimal image with no shell.
- signal-cli-rest-api has no authentication. Keep its port 8080 off the internet; ideally don't
  publish it at all and let containers talk over the Docker network.
- In json-rpc mode, messages that arrive while signal-ollama is down are not replayed.

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
