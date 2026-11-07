# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

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
