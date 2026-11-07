# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- `ollama.backup_url` (`OLLAMA_BACKUP_URL`): another address for Ollama, used when the main one
  can't be reached, e.g. a laptop's VPN address while it's away from home. The main address is
  tried first again after a minute.

### Changed
- An Ollama address that doesn't answer now fails after 3 seconds instead of waiting for the
  request timeout, so "Ollama is offline" (and Wake-on-LAN, and unchecked hook events) happen
  right away.

## [0.2.0] - 2026-11-07

### Added
- Telegram: talk to Llamero from Telegram as well as, or instead of, Signal (`transports.telegram`
  with a bot token from @BotFather and the user ids allowed). It uses Telegram's Bot API directly,
  with no extra container or public URL, and supports formatting, photos both ways, the typing
  indicator, the 👀 reaction and replies to earlier messages. Telegram people are
  `telegram:<id>` inside Llamero (e.g. in a hook's `to:`), with their own chats.
- Several messaging apps at once: each has its own allowlist, and `/healthz` reports each under
  `transports`.

### Changed
- Signal is now optional: set up Signal, Telegram, or both.
- `/healthz` reports `transports.signal` instead of `signal`, and is unhealthy if any transport
  can't receive messages.
- A sender who isn't allowed is logged with their full id when it isn't a phone number (e.g.
  `telegram:123456789`), so it can be copied into `allowed_senders`.

## [0.1.0] - 2026-11-07

### Added
- Chat with your Ollama models over Signal, through signal-cli-rest-api (json-rpc mode). Direct
  messages from an allowlist only; group chats and the bot's own messages are ignored. Messaging
  apps are transports (`transports.signal` in the config), so others can be added later.
- Per-chat memory saved to disk: recent messages word for word, plus a running summary of older
  ones. Typing indicator, a 👀 reaction while answering, swipe-to-reply passes the quoted text, and
  Markdown from the model becomes Signal formatting.
- Photos: send one and ask about it. A separate vision model can answer when the chat's model can't
  see.
- Tools from MCP servers (Streamable HTTP): read-only by default, with per-server `allow_write`
  (`false`, `ask` for approval in the chat, or `true`), tool allowlists and notes. Pictures tools
  return are sent as attachments.
- Knowledge folder: Markdown notes the model always gets, plus notes per MCP server given with its
  tools.
- Scheduled checks, set up by asking: "every weekday at 7:30 tell me what happened overnight",
  with "only if" checks that stay silent unless something's worth reporting.
- Hooks: other systems (Home Assistant, scripts) send events to `POST /hooks/<name>`. The model
  checks each against the hook's task and only messages you when it matters. Events close together
  are checked together, `cooldown` skips repeats, and each hook can set its own `model`, `think`
  and `tools` for quick checks. Tell the bot a 🔔 message was a false alarm and later checks learn
  from it (`/learned`). `/hooks` shows the latest checks and what each decided.
- Wake-on-LAN for an Ollama machine that sleeps.
- `GET /healthz` and a `-healthcheck` flag for Docker.
- Commands: `/models`, `/think`, `/stop`, `/retry`, `/reset`, `/status`, `/stats`, `/summary`,
  `/settings`, `/knowledge`, `/schedules`, `/hooks`, `/learned`, `/wake`, `/help`.
- Configuration from `/config/config.yml`, environment variables, or both, checked at startup with
  errors naming the setting and line.
