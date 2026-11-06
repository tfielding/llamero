# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Hooks learn from your feedback: tell the bot a 🔔 message was a false alarm (or right, or that
  something was missed), e.g. by replying to it, and it saves your verdict with the event
  (`hook_feedback` tool, no approval needed). Later checks of that hook get your recent verdicts as
  examples. Kept in `data/learned/<hook>.md`; new `/learned` command lists them, `/learned clear
  <hook>` forgets them.
- Hook `cooldown`: after a message, the hook skips events with the same `key` (e.g. a camera, sent as
  `"key"` in the JSON or `?key=`) for that long, without asking the model. Only a sent message starts
  it.

### Fixed
- When the model uses tools and then writes nothing, it's asked once more for an answer before the
  bot gives up. Hooks and scheduled checks no longer send the chat's "Sorry, the model returned no
  answer… Try asking again": a hook sends its events marked "not checked", and a schedule reports
  a failed run.
- Checks with an "only if" (hooks and schedules) stayed silent only if the model hadn't looked at any
  pictures: one that fetched a snapshot to decide sent it even when the answer was "nothing to
  report". Now its answer decides. An answer that starts with NOTHING_TO_REPORT and gives a reason
  also counts as nothing to report.

## [0.3.0] - 2026-11-06

### Added
- Hooks: other systems send events to `POST /hooks/<name>` (Home Assistant, a script, anything that
  can make an HTTP request), with a token (`WEBHOOK_TOKEN`). Each hook in `webhooks.hooks` has a
  `task` the model does with read-only tools, like a scheduled check, and an optional `only_if`, so
  you only hear about events that matter. Events close together are checked in one run (`group`,
  default 30s). A hook without a task passes events on unchanged. If Ollama can't be reached, the
  events are sent unchecked rather than lost; `wake` wakes the Ollama machine first. Served on the
  health port (8092).
- Photos: send the bot a photo, with or without a question, and it looks at it. If the chat's model
  can't see images, `OLLAMA_VISION_MODEL` (or `ollama.vision_model`) answers that message and the
  chat keeps its model; if neither can, the bot says so. Photos are shrunk to 1600 px and sent as
  JPEG, up to 4 per message; other files are named to the model but not read.
- `GET /healthz` on port 8092 (`HEALTH_ADDR`, `off` to disable): 200 while the bot is connected to
  signal-cli-rest-api, 503 when it isn't, with Ollama and MCP servers reported in the body. No
  addresses or secrets in it. `signal-ollama -healthcheck` for a Docker `HEALTHCHECK` (the image
  has no curl). The Signal connection is now pinged every 30 seconds, so one that died quietly is
  reconnected within a minute instead of never.
- Scheduled checks: ask "every weekday at 7:30 tell me what happened overnight on the cameras" or
  "remind me at 5pm to …", approve, and the result arrives on time. Repeats (`every 30m`, with an
  optional window like `between 22:00 and 06:00`), times of day on chosen days, and one-offs.
  "Only if" checks stay silent unless something's worth reporting. Scheduled runs only get
  read-only tools; creating and deleting need approval. `/schedules` lists them, with `delete`,
  `pause`, `resume` and `run`. A check that can't run (Ollama unreachable, or the model fails)
  tells you which and why, once per run of failures. `SCHEDULES` (default `true`) turns them off;
  `TZ` sets the time zone.
- Knowledge folder (`KNOWLEDGE_DIR`, default `/config/knowledge`): Markdown notes the model always
  gets (people, cars, routines), and per-MCP-server notes (`frigate.md` for the server `frigate`)
  given only with that server's tools. Edits apply from the next message. Capped at
  `KNOWLEDGE_MAX_CHARS` (8000). New `/knowledge` command; `/status` shows the total.
- Optional config file, `/config/config.yml` (or `CONFIG_FILE`), with every setting; see
  `config.example.yml`. Environment variables still work and win over the file. `${VAR}` in the file
  is filled in from the environment. Invalid settings, unknown keys and unset `${VAR}`s stop the bot
  at startup with a message naming the setting.
- Per-MCP-server settings in the config file: `allow_write` (`false`, `ask` or `true`), a `tools`
  allowlist, and `knowledge` (its notes file in the knowledge folder, checked at startup), e.g.
  approval for Home Assistant actions only. `MCP_ALLOW_WRITE` is the default for
  servers that don't set it.
- Stricter startup checks: `SIGNAL_URL` and `OLLAMA_URL` must be http(s) URLs, `OLLAMA_TIMEOUT` positive.

### Changed
- Clearer startup errors for knowledge files: a missing file lists what's in the folder, and an
  unreadable file or folder says "permission denied" with the fix.
- The compose example follows Frigate's layout: `/opt/signal-ollama/config:/config:ro` for what you
  write, `/opt/signal-ollama/data:/data` for chat history. Existing setups that map a folder to
  `/data` keep working; add a `/config` mount to use the knowledge folder.

### Fixed
- Replies no longer end with "[1 image(s) were sent to the user with this reply.]": the model was
  copying the note kept in the chat history.
- Animated clips from tools (e.g. frigate-mcp's `get_alert_clip` GIFs) are sent to you but no longer
  given to a vision model, which only takes still JPEG or PNG pictures; the model is told a clip was
  sent.

### Removed
- `MCP_HINT_<NAME>`: put per-server notes in the knowledge folder as `<name>.md` instead.

## [0.2.0] - 2026-11-01

### Added
- Long chats are summarized instead of forgotten: messages past `HISTORY_LIMIT` are folded into a
  running summary that is saved with the chat and given to the model. New `/summary` command;
  `HISTORY_SUMMARY` (default `true`) and `SUMMARY_MODEL` settings.
- Wake-on-LAN: with `OLLAMA_WOL_MAC` set, a message that arrives while the Ollama machine is asleep
  wakes it and is answered once it's up (`OLLAMA_WOL_ADDR`, `OLLAMA_WOL_WAIT`). New `/wake` command.
  It also wakes the machine at startup when the bot is waiting for Ollama to pick a model.
- `MCP_ALLOW_WRITE=ask`: tools that can change things are offered, but each call waits for your
  approval in Signal (yes to run, anything else cancels, 2-minute timeout). If an approved call
  fails and the model retries, the next prompt shows the error. After a timeout or a second "no",
  no more actions are tried for that message.
- A single value sent for a tool parameter that takes a list (`"domain": "light"`) is wrapped
  in a list instead of being rejected by the server.
- If the model calls a tool name that doesn't exist, it's told the exact names that do, instead
  of the user being asked to approve it.
- Replies to earlier messages (Signal's swipe-to-reply) pass the quoted text to the model, so
  "explain this part" refers to the right message.
- A 👀 reaction on your message while it's being answered, removed once the reply is sent
  (`REACTION` to change the emoji, `off` to disable).
- `/retry` answers your last message again: the one that failed (error, offline, `/stop`) if
  any, otherwise the last one answered, replacing its answer.
- `/settings` shows the current configuration. MCP tokens are never shown, phone numbers are
  masked, and credentials and query strings are removed from URLs.

### Changed
- `/model` is now `/models` (`/models` lists, `/models <name>` switches). `/model` still works.
- Docker `:latest` now points to the newest release. Builds from `main` are published as `:edge`,
  and releases also get a `major.minor` tag (e.g. `:0.1`).

## [0.1.0] - 2026-10-29

### Added
- Chat with Ollama models over Signal via signal-cli-rest-api (json-rpc mode).
- Allowlist of senders; group chats and the account's own messages are ignored.
- Per-chat memory saved to disk, typing indicator, Markdown converted to Signal formatting.
- Commands: `/model`, `/reset`, `/status`, `/think`, `/stop`, `/stats`, `/help`.
- Tools from MCP servers (Streamable HTTP): read-only by default, optional allowlist, per-server
  tokens and hints, server instructions passed to the model, images sent as attachments.
- Safeguards for tool use: invented/blank arguments removed, read-only calls retried without
  failing optional parameters, failure and round limits, explained empty answers.
- Fast "Ollama is offline" reply; `-version` flag.
