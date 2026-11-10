# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- `mcp.servers.<name>.read_only`: tools to treat as read-only although their server doesn't mark
  them, so hooks and schedules get them (e.g. Home Assistant's `homeassistant__GetLiveContext`).
- Tool names that don't match: at startup, names in a server's `read_only` it doesn't have, and
  hook `tools` checks can't use, are logged with the reason (needs approval, or a close match
  such as a prefixed name).
- A hook check whose listed tools aren't available is told which, so the model works without
  them instead of guessing other tool names.
- Hooks can send to group chats: put a Signal group (`signal:group.<id>`) or a Telegram group
  (`telegram:-<id>`) in a hook's `to`, alone or with people. Groups only receive messages; the
  bot still only chats one to one, and logs a group's id (once) when it ignores a message there.
- Signal ids in the config can start with `signal:` (`signal:+15551234567`), like Telegram's with
  `telegram:`. Without it works as before.
- `/hooks stats`: totals per hook since the bot started: checked, sent and nothing to report,
  sent unchecked and why, skipped (cooling down, snoozed), check times, and the busiest keys.
- `/snooze`: mute hooks for a while, for the person who asks: all of them (`/snooze 2h`), one
  (`/snooze camera_alert 30m`), or one hook's events for one key such as a camera
  (`/snooze camera_alert front_door 1h`). `/snooze` lists them and `/snooze off` ends them.
- Hook `title`: the first line of a hook's messages, e.g. `📷 Camera` instead of "🔔 camera_alert",
  or `none` for no title line.
- `ollama.backup_url` (`OLLAMA_BACKUP_URL`): a backup Ollama server, used when the main one can't
  be reached. The main server is tried first again after a minute.

### Changed
- An Ollama address that doesn't answer now fails after 3 seconds instead of waiting for the
  request timeout, so "Ollama is offline" (and Wake-on-LAN, and unchecked hook events) happen
  right away.

### Removed
- Learning from feedback on hook messages (the `hook_feedback` tool and `/learned`): saved verdicts
  only described past events in words, which didn't help checks tell new pictures apart. Old
  verdicts in `data/learned/` are no longer read and can be deleted.

### Fixed
- A hook or schedule check whose answer was only a note about pictures sent a title over bare
  pictures; it now counts as no answer (hook events arrive marked not checked).

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
