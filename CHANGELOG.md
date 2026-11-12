# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- `llamero -check` (`docker compose run --rm llamero -check`): load the config and try Ollama, its
  models and the MCP servers without starting the bot, so a mistake shows up before a restart. It
  lists each server's tools and any names in `tools`, `read_only` or hooks that match nothing,
  and exits 1 only when the bot couldn't start.
- `/hooks replay`: check hook events again.
  - `/hooks replay 14` (or `12-15`, at most 10) checks runs from `/hooks`, now numbered, with the
    hook's current settings, and shows only you what it decides next to what it decided before.
    Tune a hook's task or model on real events without waiting for new ones.
  - `/hooks replay` alone checks the events that went out unchecked because Ollama was down or the
    model failed (the last 20 runs). Findings are sent as usual, marked "rechecked"; the one who
    asked hears about the rest. `/hooks` says when some are waiting.
- Snoozes, the last 20 hook checks (`/hooks`, with their events) and the runs waiting for
  `/hooks replay` are saved in `data/hooks.json`, so a restart doesn't unmute everything or lose
  them.
- The model is told the current date and time with every message, chats and checks alike, so
  questions about "yesterday" or "this week" don't spend a tool call on the time.
- Names in a server's `tools` that it doesn't have are logged at startup, with a close match
  (e.g. `search` → `search__search`), like names in `read_only` already were. A wrong name there
  used to hide the tool without a word.

### Changed
- A hook's first event has Ollama load the check's model while the events are grouped, so the
  check doesn't wait for it when Ollama unloaded it after being idle (a few seconds to load
  a large model, or more).
- `/stats` after a reply that used tools shows how many model calls it took and splits the time
  reading the prompt into the first call and the rest. Ollama reuses what it read for earlier
  calls but counts those tokens again, so the total count looked like rereading when it wasn't.
- Config file errors list every problem at once, and unknown keys get a hint: the setting it's
  probably a misspelling of (`modle` → `model`), or, for an MCP server or hook indented under
  the wrong section, where it goes.
- Two servers' tools with the same name only get the server name in front when both are offered.
  A tool one server's `tools` leaves out no longer renames the other (`ha_llm__GetDateTime` is
  `llm__GetDateTime` again). `mcp.tools` still accepts the prefixed name.

### Fixed
- A model could say it had set a reminder without creating a schedule. The schedule tool now tells
  models it's the only way to do something later, and not to claim a schedule it didn't create.

## [0.3.0] - 2026-11-10

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
- `mcp.max_tool_rounds` (`MCP_MAX_TOOL_ROUNDS`): how many rounds of tool calls a message can
  take before the model has to answer, 1 to 50 (default 6, as before). Raise it for setups where
  answering takes more lookups, such as a large Home Assistant.
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
