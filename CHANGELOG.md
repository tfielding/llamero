# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Long chats are summarized instead of forgotten: messages past `HISTORY_LIMIT` are folded into a
  running summary that is saved with the chat and given to the model. New `/summary` command;
  `HISTORY_SUMMARY` (default `true`) and `SUMMARY_MODEL` settings.
- Wake-on-LAN: with `OLLAMA_WOL_MAC` set, a message that arrives while the Ollama machine is asleep
  wakes it and is answered once it's up (`OLLAMA_WOL_ADDR`, `OLLAMA_WOL_WAIT`). New `/wake` command.
- `/settings` shows the current configuration. MCP tokens are never shown, phone numbers are
  masked and credentials are removed from URLs.

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
