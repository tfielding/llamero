# Llamero

[![ci](https://github.com/tfielding/llamero/actions/workflows/ci.yml/badge.svg)](https://github.com/tfielding/llamero/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Llamero (Spanish for a llama herder, pronounced yah-MEH-roh) is your own Ollama assistant in the
messaging apps you already use: it answers when you message, uses tools from MCP servers
(cameras, Home Assistant, ...), and keeps watch with checks and alerts, messaging you only when
something matters.

It's a small Go service. Each messaging app is a **transport**; use one or several at once:

- **Signal**, through [bbernhard/signal-cli-rest-api](https://github.com/bbernhard/signal-cli-rest-api).
  End-to-end encrypted. Needs a phone number for the bot.
- **Telegram**, through Telegram's Bot API directly: no extra container, no phone number, no
  public URL. Bot chats aren't end-to-end encrypted, so Telegram's servers can see them.

```
Signal app   ⇄ signal-cli-rest-api ⇄ ┐
                                      ├ llamero ⇄ Ollama
Telegram app ⇄ Telegram Bot API    ⇄ ┘    ⇅
                                      MCP servers (optional)
```

## Features

- Conversation memory per chat, saved to disk: recent messages word for word, plus a running
  summary of older ones (see [Long chats](#long-chats))
- Typing indicator and a 👀 reaction on your message while it's being answered
- Replying to a message works: the model sees which message you replied to
- Markdown from the model becomes the app's formatting (bold, italic, code); headings become bold
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
| `/hooks stats` | Totals per hook since the bot started: checked, sent, nothing to report, sent unchecked and why, skipped, check times, and the busiest keys |
| `/snooze 2h` | Mute every [hook](#hooks) for you for a while (`30m`, `1h30m`, `1d`, up to `7d`); `/snooze <hook> 2h` mutes one, `/snooze <hook> <key> 2h` one hook's events for one key (e.g. a camera) |
| `/snooze` | What you've snoozed; `/snooze off` (or `/snooze <hook> off`) unmutes |
| `/settings` | The settings in use, with tokens and phone numbers hidden |
| `/wake` | Wake the Ollama machine (Wake-on-LAN) |
| `/help` | This list |

`/status`, `/stats`, `/stop`, `/summary`, `/knowledge`, `/schedules`, `/hooks`, `/snooze` and `/settings` answer
right away, even while a reply is being written.

## What it answers

| Message | Behavior |
|---|---|
| Direct message from an allowed sender | Answered |
| Messages in group chats (and Telegram channels) | Ignored |
| Direct message from anyone else | Ignored (logged, with their id) |
| Messages the bot's own account sends (alerts, sync messages), and other bots | Ignored |

Each transport has its own allowlist. The same person on two apps counts as two people, each with
their own chat and memory.

## Requirements

- Ollama reachable from the container. If Ollama runs on another machine, make it listen on the
  network: `OLLAMA_HOST=0.0.0.0` (macOS app: `launchctl setenv OLLAMA_HOST 0.0.0.0`, then restart
  Ollama).
- At least one messaging app, set up as below.

### Signal

- signal-cli-rest-api with `MODE=json-rpc` and a registered number for the bot.
- Llamero only reads from the receive websocket and sends direct replies, so other services that
  use the same signal-cli-rest-api account to *send* (alerts etc.) are unaffected.
- The typing indicator only shows if "Typing indicators" is on in your Signal app
  (Settings → Privacy).

```yaml
transports:
  signal:
    url: http://signal-cli-rest-api:8080      # the default
    number: ${SIGNAL_NUMBER}
    allowed_senders: ${SIGNAL_ALLOWED_SENDERS}   # phone numbers
```

### Telegram

1. In Telegram, message [@BotFather](https://t.me/BotFather), send `/newbot` and pick a name. It
   gives you a token like `123456789:AAH...`.
2. Open your new bot and press **Start**: Telegram bots can only message people who have.
3. Find your numeric user id: start Llamero with the token and any id, message the bot, and the log
   says `ignoring message from sender not on allowlist from=telegram:123456789`; the number is
   yours. (Or ask [@userinfobot](https://t.me/userinfobot).)

```yaml
transports:
  telegram:
    token: ${TELEGRAM_TOKEN}
    allowed_senders: [123456789]               # Telegram user ids
```

- No public URL or open port: Llamero asks Telegram for new messages (long polling).
- A bot token can only be used by one program at a time. If another program polls with it, or a
  webhook is set for it, Llamero says so in the log.
- Inside Llamero, Telegram people are `telegram:<id>`, e.g. in a hook's `to:`. Signal ids can be
  written the same way, `signal:+15551234567`, or without the prefix: both mean the same.

## Run

Like Frigate, llamero uses two folders: `config` for what you write (mounted read-only) and
`data` for what the bot writes.

```
/opt/llamero/
├── config/                 yours; the bot only reads it
│   ├── config.yml          settings (see Configuration)
│   └── knowledge/          notes for the model (optional, see Knowledge)
└── data/                   chat history, one file per chat; owned by the bot
```

The image runs as a non-root user (uid 65532) that needs to write to `data`:

```bash
sudo mkdir -p /opt/llamero/config/knowledge /opt/llamero/data
sudo chown 65532:65532 /opt/llamero/data
```

Add the service to your compose file (see also
[`docker-compose.example.yml`](docker-compose.example.yml)). With Signal, put it next to your
`signal-cli-rest-api` service so they share a Docker network:

```yaml
  llamero:
    image: ghcr.io/tfielding/llamero:latest
    restart: always
    environment:                # values for the ${...} in config.yml, from your .env
      - TZ=America/New_York     # your time zone, for schedules and times
      - SIGNAL_NUMBER=${SIGNAL_NUMBER}
      - SIGNAL_ALLOWED_SENDERS=${SIGNAL_ALLOWED_SENDERS}
      - TELEGRAM_TOKEN=${TELEGRAM_TOKEN}
    volumes:
      - /opt/llamero/config:/config:ro
      - /opt/llamero/data:/data
    healthcheck:                # optional: see Health
      test: ["CMD", "/llamero", "-healthcheck"]
      interval: 60s
      timeout: 10s
      start_period: 30s
    depends_on:                 # with Signal
      - signal-cli-rest-api
```

```bash
docker compose up -d llamero
```

Then send `/status` from your phone. If it doesn't start, `docker logs llamero` names the
setting that's wrong.

- Use `http://signal-cli-rest-api:8080` (the service name) for Signal, not `localhost`: inside a
  container, `localhost` is the container itself.

Locally, without Docker:

```bash
TELEGRAM_TOKEN=123456789:AAH... TELEGRAM_ALLOWED_SENDERS=123456789 \
OLLAMA_URL=http://localhost:11434 DATA_DIR=./data go run .
```

## Configuration

Settings come from an optional config file, environment variables, or both.

- **Config file:** `/config/config.yml`, or the path in `CONFIG_FILE`. Copy
  [`config.example.yml`](config.example.yml) to start:

  ```yaml
  transports:
    signal:
      number: ${SIGNAL_NUMBER}
      allowed_senders: ${SIGNAL_ALLOWED_SENDERS}
    telegram:                     # either app, or both
      token: ${TELEGRAM_TOKEN}
      allowed_senders: [123456789]
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
  typo), an unset `${VAR}` or a missing knowledge file stops llamero with a message naming
  the setting and its line. The file is read once; restart to apply changes.

| Variable | config.yml | Default | |
|---|---|---|---|
| `SIGNAL_NUMBER` | `transports.signal.number` | none | [Signal](#signal): the number registered in signal-cli-rest-api; sets up Signal |
| `SIGNAL_ALLOWED_SENDERS` | `transports.signal.allowed_senders` | required with Signal | Phone numbers (or Signal account ids) allowed to use the bot through Signal, with or without `signal:` in front; comma-separated, or a list in the file |
| `SIGNAL_URL` | `transports.signal.url` | `http://signal-cli-rest-api:8080` | |
| `TELEGRAM_TOKEN` | `transports.telegram.token` | none | [Telegram](#telegram): the bot token from @BotFather; sets up Telegram |
| `TELEGRAM_ALLOWED_SENDERS` | `transports.telegram.allowed_senders` | required with Telegram | Telegram user ids allowed to use the bot; comma-separated, or a list in the file |
| `TELEGRAM_URL` | `transports.telegram.url` | `https://api.telegram.org` | Only for a self-hosted Bot API server |
| `OLLAMA_URL` | `ollama.url` | `http://localhost:11434` | |
| `OLLAMA_BACKUP_URL` | `ollama.backup_url` | none | Another address for Ollama, used when `OLLAMA_URL` can't be reached: e.g. a laptop's VPN address while it's away from home (see [Ollama on a laptop](#ollama-on-a-laptop)) |
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
| | `mcp.servers.<name>.read_only` | none | Tools to treat as read-only although the server doesn't mark them: offered without asking, and given to hooks and schedules. Only list tools that really just look things up |
| | `mcp.servers.<name>.knowledge` | `<name>.md` if present | The server's notes file in the [knowledge folder](#knowledge); must exist |
| `MCP_TOOLS` | `mcp.tools` | all | Only these tools, across all servers |
| `MCP_MAX_RESULT_CHARS` | `mcp.max_result_chars` | `32000` | Max characters of tool output given to the model (about 8k tokens); Ollama's context window must hold it |
| `HEALTH_ADDR` | `health.addr` | `:8092` | Where [`GET /healthz`](#health) listens; `off` to disable |
| `SCHEDULES` | `schedules.enabled` | `true` | [Scheduled checks](#schedules): the `create_schedule` tool and `/schedules` |
| `WEBHOOK_TOKEN` | `webhooks.token` | none | Token [hook](#hooks) callers send as `Authorization: Bearer <token>`; at least 16 characters, required with hooks |
| | `webhooks.hooks.<name>` | none | A [hook](#hooks) at `POST /hooks/<name>`: `task`, `only_if`, `to`, `wake`, `group`, `cooldown`, `model`, `think`, `tools`, `title` |
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

llamero can use tools from any [MCP](https://modelcontextprotocol.io) server that speaks
Streamable HTTP, such as [frigate-mcp](https://github.com/tfielding/frigate-mcp),
[go2rtc-mcp](https://github.com/tfielding/go2rtc-mcp) or Home Assistant's. It has no
tool-specific code: list a server and its tools are offered to the model.

- Only tools marked **read-only** are offered, unless the server's `allow_write` is `ask` or
  `true`. Anything that can change things (locks, alarms, settings) stays hidden by default.
  Hooks and schedules only ever get read-only tools. If a server doesn't mark a tool that only
  looks things up, vouch for it with the server's `read_only`, using the name `/status` lists
  (newer Home Assistant versions prefix theirs: `read_only: [homeassistant__GetLiveContext]`).
  Names in `read_only` or a hook's `tools` that don't match a usable tool are logged at startup,
  with a close match if there is one, and a check is told which of its tools are missing.
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
  -d '{"text": "Front door opened"}' http://llamero:8092/hooks/front_door
```

- **`task`:** what the model does with the event, using read-only tools, like a
  [scheduled check](#schedules). Put the details the tools need in the event text (an id, a
  camera, a sensor). Without a task, the event is sent to you unchanged and the model isn't used.
- **`only_if`:** report only when this is true; otherwise the bot stays silent. Without it, every
  event gets a message.
- **`to`:** who gets the messages, from the transports' `allowed_senders` (Telegram ones as
  `telegram:<id>`); default everyone in them. The model
  runs once, with the first one's model (unless `model` is set) but without their chat history;
  the result goes to all of them and joins each one's conversation, so "show me that" works
  afterwards.
  `to` can also name group chats, alone or with people: a Signal group as `signal:group.<id>`, a
  Telegram group as `telegram:-<id>`. Groups only receive: the bot doesn't answer messages in
  them, so nobody can chat with it or run commands there, and follow-ups (and `/snooze`) happen
  in private chats. A hook with only groups in `to` sends to no one else. To find a group's id,
  add the bot to the group and send a message there: the log says
  `ignoring messages in a group chat … group=<id>`, once per group. With Signal, the bot's number
  has to be a member (signal-cli-rest-api's `GET /v1/groups/<number>` also lists the ids, without
  `signal:`, which is optional).
  With Telegram, bots in groups only see commands unless privacy mode is off in @BotFather, so
  send something like `/hello@<your bot>`.
- **`group`:** events within this long of the first are checked together in one run (default
  `30s`, at most `10m`, `0` for each on its own), so one person walking past three cameras is one
  message. Up to 10 events are listed per run; more are counted.
- **`cooldown`:** after a message, skip the hook's events with the same `key` for this long (e.g.
  `5m`, at most `24h`; default off), so someone lingering in front of one camera is one message,
  not five. Skipped events aren't checked, so they cost no model time; events with another key
  (another camera) still are. Only a sent message starts a cooldown, not a check that found nothing.
  The catch: a second, different arrival on the same key within the cooldown isn't reported.
- **`model`:** the model for this hook's checks, e.g. a smaller, faster vision model, while chat
  keeps the bigger one. Default: the recipient's chat model.
- **`think`:** thinking for this hook's checks: `off`, `on`, `low`, `medium` or `high` (what the
  model supports). Default: the chat's `/think` setting. `off` saves time on simple rules.
- **`tools`:** the only tools this hook's checks get, by name as in the logs, e.g.
  `[review_alert, get_camera_frame]`. Default: every read-only tool. Every model call reads every
  tool's description, so a short list makes each round quicker and keeps the model from wandering
  off. Tools that aren't read-only are never given to hooks, even if listed; names that aren't
  available are logged at each check.
- **`title`:** the first line of the hook's messages, e.g. `📷 Camera` (at most 60 characters).
  Default: 🔔 and the hook's name. `none` leaves it out, so a message is just the answer.
- **`wake`:** wake the Ollama machine if it's asleep (needs [Wake-on-LAN](#wake-on-lan)). Without
  it, or if Ollama can't be reached, you get the events unchecked, marked "not checked", rather
  than nothing.
- Hooks are served on the health port, `HEALTH_ADDR` (default 8092). Publish it, or put the caller
  on the same Docker network. Responses: **202** queued, **401** wrong token, **404** no such hook,
  **503** still starting up.
- Runs wait their turn behind your own messages, and `/stop` cancels one that's running.
- `/hooks` shows the latest checks and what each decided, including the model's reason for
  "nothing to report", which helps when tuning `task` and `only_if`.
- `/hooks stats` shows the totals per hook since the bot started: how many checks sent a
  message and how many found nothing, how often events went out unchecked (and why, e.g. Ollama
  unreachable), how many were skipped while cooling down or snoozed, typical and slowest check
  times, and the busiest keys with how many of each were sent. Like `/hooks`, it's kept in
  memory, so a restart starts it over.
- `/snooze` mutes hooks for a while, for the person who sends it: during yard work, or when
  guests are expected. `/snooze 2h` mutes them all, `/snooze camera_alert 30m` one, and
  `/snooze camera_alert front_door 1h` only that hook's events sent with that `key`. Muted events
  aren't checked when everyone who'd get them has snoozed, so they cost no model time; `/hooks`
  lists them as skipped. Snoozes are kept in memory, so a restart ends them.

**From Home Assistant:** a `rest_command` that any automation can call:

```yaml
# configuration.yaml
rest_command:
  llamero_hook:
    url: "http://<llamero-host>:8092/hooks/{{ hook }}"
    method: POST
    headers:
      Authorization: !secret llamero_hook_auth   # "Bearer <token>" in secrets.yaml
    content_type: application/json
    payload: '{"text": {{ text | tojson }}, "key": {{ key | default("") | tojson }}}'
```

```yaml
# an automation's action
- action: rest_command.llamero_hook
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
- With Signal, signal-cli-rest-api keeps received files in its data folder; llamero doesn't delete
  them.

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

## Ollama on a laptop

If Ollama runs on a laptop you take with you, give Llamero both of its addresses:

```yaml
ollama:
  url: http://192.168.1.50:11434          # at home, on the LAN
  backup_url: http://10.6.0.2:11434       # away, over your VPN (WireGuard, Tailscale, ...)
```

- Each request goes to `url` first. If nothing answers within 3 seconds, it goes to `backup_url`,
  and `url` is skipped for a minute before being tried first again. The log says when it switches.
- The VPN has to let your home server reach the laptop, not just the laptop reach home: check with
  `curl http://<laptop-vpn-ip>:11434/api/version` from the server while the laptop is away. Ollama
  must listen on all interfaces (`OLLAMA_HOST=0.0.0.0`), and the laptop's firewall must allow it.
- A laptop that's asleep can't answer at either address; Wake-on-LAN doesn't work over a VPN.
  Then chats get "Ollama is offline", and [hooks](#hooks) send their events marked "not checked"
  rather than dropping them.

## Health

`GET /healthz` on port 8092 says whether the bot is working, for Docker, Uptime Kuma or Home
Assistant:

```json
{
  "status": "ok",
  "version": "0.1.0",
  "uptime": "26h4m10s",
  "transports": {
    "signal": { "connected": true, "since": "2026-11-04T09:00:00Z" },
    "telegram": { "connected": true, "since": "2026-11-04T09:00:02Z" }
  },
  "ollama": { "reachable": false, "error": "connection refused" },
  "mcp": { "frigate": "ok", "ha": "unavailable (timed out)" }
}
```

- **200** when the bot can receive messages on every transport it uses, **503** when one can't
  (for example signal-cli-rest-api is down, or Telegram can't be reached). That's the one to alert on.
- Ollama and MCP servers are reported but don't make it unhealthy: with Wake-on-LAN a sleeping
  Ollama is normal. To be alerted about Ollama too, have your monitor look for
  `"reachable": true` in the body.
- The connection to signal-cli-rest-api is checked with a ping every 30 seconds, so one that died
  quietly is noticed (and reconnected) within a minute. Telegram is polled continuously, so trouble
  there shows up within a minute too.
- **Docker:** the image has no curl, so `llamero -healthcheck` checks its own endpoint and
  exits 0 or 1; see the `healthcheck:` in [Run](#run). `docker ps` then shows the container as
  healthy or unhealthy.
- **Uptime Kuma** on the same Docker network: an HTTP monitor on
  `http://llamero:8092/healthz`. No need to publish the port otherwise.
- The report has no phone numbers, tokens, URLs or addresses; errors are cut to their cause
  ("connection refused", "timed out"), with the details in the bot's log.

## Safety

- **Allowlist only.** Only the transports' `allowed_senders` get answers; group chats are ignored.
- **Read-only tools by default.** Tools that can change things are hidden unless a server allows
  them, and with `ask` each call needs your approval. Scheduled checks and hooks only ever get
  read-only tools, and creating or deleting a schedule needs your approval.
- **Hooks need a token.** `POST /hooks/<name>` only accepts requests with `WEBHOOK_TOKEN`, and only
  for hooks in the config file. Event text is shown to the model as an event, never as a message
  from you, and can't unlock actions.
- **No secrets in replies.** `/settings` shows tokens only as "set", masks phone numbers and
  removes credentials from URLs.
- **Your models.** Chats, photos and knowledge notes only go to your Ollama server, never to a
  cloud AI. Chat history is stored unencrypted in `data`; photos aren't stored by llamero.
- **Pick the transport for your privacy needs.** Signal is end-to-end encrypted. Telegram bot chats
  aren't: Telegram's servers can read the messages and pictures (including camera snapshots).
- **Keep tokens secret.** Anyone with the Telegram bot token can read and send as the bot. Put it
  in your `.env`, not in a file you share.
- Runs as a non-root user in a minimal image with no shell.
- signal-cli-rest-api has no authentication. Keep its port 8080 off the internet; ideally don't
  publish it at all and let containers talk over the Docker network.
- Signal messages that arrive while llamero is down are not replayed (json-rpc mode). Telegram keeps
  them for up to 24 hours and delivers them when llamero is back.

## Install without Docker

```bash
go install github.com/tfielding/llamero@latest
llamero -version
```

## Develop

```bash
go test -race ./...
```

The code has two sides:

- **`internal/core`**: the assistant itself (the model loop, tools, hooks, schedules, knowledge,
  commands). It knows nothing about any one messaging app.
- **`internal/transport`**: what the core and the messaging apps share: an incoming message, files,
  and the `Messenger` interface. Each app is a package under it; **`internal/transport/signal`**
  is Signal (signal-cli-rest-api, envelope filtering, Markdown to Signal formatting), and
  **`internal/transport/telegram`** is Telegram (Bot API long polling, Markdown to HTML). A new
  messaging app is a new package there that turns its events into `transport.Message` and
  implements `Messenger`. The `transport.Router` lets the core talk on several apps at once: each
  app's people get its prefix (`telegram:12345`), except Signal's, so the same person on two apps
  is two people with their own chats.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full set of checks CI runs, and
[SECURITY.md](SECURITY.md) for reporting vulnerabilities. Changes are listed in
[CHANGELOG.md](CHANGELOG.md).

## License

MIT, see [LICENSE](LICENSE).

llamero is an independent project, not affiliated with or endorsed by the Signal Technology
Foundation, Telegram or Ollama Inc. Signal, Telegram and Ollama are trademarks of their respective
owners.
