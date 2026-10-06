# Nebula

Nebula keeps your Claude Code sessions on your own server and lets several Claude
subscription accounts take turns: when one reaches its usage limit, the session
resumes on the next. It targets the Claude Code CLI and T3 Code (which runs the
CLI) on Windows and Linux. Claude Desktop is out of scope.

- **Session archive:** every Claude Code transcript, including subagents, synced
  continuously from each device; browse, download, and restore from the dashboard.
- **Accounts:** connect accounts with Claude's own sign-in; see live usage for each.
- **Switching:** start Claude through Nebula's launcher and it uses the first
  connected account with room; in a terminal it resumes the session on the next
  account when a usage limit is hit.

Not implemented yet: redeeming usage resets (planned with Claustra passkey
approval), Claustra sign-in for the dashboard, and per-device revocation.
See the [design](docs/design.md).

## Install on a device

You need the Claude Code CLI, a Nebula server (below), and its `device.token`.

```sh
nebula setup --server https://nebula.example.com --token-file device.token
```

Setup checks the server, then:

- installs `nebula` and the `nebula-claude` launcher (`~/.local/bin`, or
  `%LOCALAPPDATA%\Programs\Nebula` on Windows) and writes
  `<user config>/Nebula/config.json` with the server and token;
- runs the companion (`nebula sync --watch`) in the background: a systemd user
  service on Linux, a hidden startup entry on Windows; no administrator rights;
- installs the Claude Code mod through `claude plugin` and sets
  `NEBULA_COMPANION_PATH` in Claude Code's `settings.json`.

Then start Claude with `nebula-claude` (or `alias claude=nebula-claude`), and in
T3 Code set Settings > Claude > Binary path to the launcher. `nebula setup
--uninstall` stops the companion; `--no-service` and `--no-mod` skip those steps.

## Connecting accounts

Accounts connect through Claude Code's own `claude auth login`, run into a fresh
profile (its own `CLAUDE_CONFIG_DIR`) under `<user config>/Nebula/profiles/`:

- **Dashboard:** Claude accounts > Add account > Sign in with Claude. It runs on
  a device whose companion is online; the browser opens there and finishes by
  itself, or open the sign-in link elsewhere and paste the code Claude shows.
- **Terminal:** `nebula login`. **Claude Code:** `/nebula-login`.

Credentials stay in that profile on that device. Nebula profiles share sessions,
settings, `CLAUDE.md`, skills, plugins, agents, commands, hooks, and history with
your home profile (`~/.claude`, or `CLAUDE_CONFIG_DIR`) through links, so any
account can resume any session. Profiles you manage yourself can be added with
`NEBULA_PROFILES` (absolute paths, `:`-separated on Linux, `;` on Windows).

The companion reports each profile's signed-in identity and usage: Claude Code's
cached reading, refreshed every five minutes from Claude's usage endpoint with
the profile's own access token while it is valid. The token is never refreshed,
logged, or sent anywhere but Anthropic. In the dashboard, connected accounts can
be reordered, opted out of switching, or disconnected.

## Switching

The launcher picks the first connected, enabled account in priority order that
has a profile on the device and is not at a usage limit, rechecking live usage
just before starting Claude. Without the server it reuses its last choice.

A usage limit is a `rate_limit` failure that the session's own limit readings
confirm (a window at 100%); ordinary throttling never switches. The mod then
records the limit on the server until the window resets. Under the launcher in a
terminal, it also hands the session over and exits Claude Code, and the launcher
resumes it with `--resume` on the next account, keeping flags such as `--model`.
Nothing is resubmitted: you continue the conversation. For T3 Code and `-p` runs
the launcher hands over to Claude directly; the next session it starts uses
another account.

## Session archive

`nebula sync` archives every profile's transcripts; `--watch` repeats every ten
seconds, skipping sessions whose files have not changed. Files are split at line
boundaries into gzip chunks stored once, so a growing transcript uploads and
stores only its new tail. The server keeps each session's latest ten revisions,
then one per hour for two days, then one per day, and removes unreferenced chunks.

```sh
nebula list
nebula restore --device DEVICE_UUID --session SESSION_UUID [--revision SHA256]
```

Restore recreates the transcript in your projects directory, refuses symlinks and
never overwrites a different file. It does not replay tools or copy workspace
files; open the original workspace in Claude Code to resume.

## Server

Docker with automatic HTTPS (see [deploy/compose.yaml](deploy/compose.yaml)):

```sh
cd deploy
echo NEBULA_DOMAIN=nebula.example.com > .env
docker compose run --rm nebula init --user you --dir /data/credentials
docker compose up -d
```

Copy `credentials/device.token` out of the volume to your devices. Without Docker,
run `nebula serve` behind a TLS reverse proxy; [deploy/nebula-server.service](deploy/nebula-server.service)
is a hardened systemd unit. Back up the data directory.

Transcripts contain prompts, code, and tool output; the server can read them (no
end-to-end encryption). The dashboard signs in with a device token, kept in the
tab's session storage. `init` creates one token per user; add owners to
`users.json` with distinct random 32-byte base64url tokens and restart. The
companion refuses plain HTTP outside loopback and refuses redirects.

### API

Bearer authentication determines the owner. `GET /v1/me`.

- Archive: `GET /v1/sessions[?latest=1]`, `GET /v1/sessions/{device}/{session}[?revision=]`,
  `POST /v1/chunks/missing`, `PUT /v1/chunks/{sha256}`, `PUT /v1/sessions/{device}/{session}`
  (a manifest of chunk IDs; the server reassembles and validates it).
- Accounts: `GET /v1/accounts`, `PUT /v1/devices/{device}/accounts`,
  `POST /v1/accounts/{id}/connect|disconnect|limited`, `PATCH /v1/accounts/{id}`,
  `PUT /v1/accounts/order`.
- Sign-in relay: `GET /v1/devices`, `POST /v1/logins`, `GET /v1/logins/{id}`,
  `POST /v1/logins/{id}/code|cancel`; companions use `POST /v1/devices/{device}/poll`
  and `PUT /v1/devices/{device}/logins/{id}`. Sign-ins live in memory for ten minutes.

## Development

Go 1.25+ and Node 22+.

```sh
(cd web && npm ci && npm run build)   # dashboard, embedded at build time
go build -o bin/nebula ./cmd/nebula
go test ./...
claude plugin validate mods/nebula && claude plugin test mods/nebula
scripts/release.sh 0.2.0              # Linux and Windows binaries with SHA256SUMS
```

For dashboard work run `npm run dev` in `web/`; it proxies `/v1` to
`127.0.0.1:13003`. Mod function hooks are an early-access Claude Code API: after
Claude Code updates, run `claude plugin validate mods/nebula` again.

## License

MIT
