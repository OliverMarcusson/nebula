# Nebula: connected Claude accounts and shared sessions

Status (6 October 2026): implemented are the chunked session archive with
retention, account sign-in through `claude auth login` (dashboard, terminal, mod),
live per-profile usage, account switching through the `nebula claude` launcher
and the mod's usage-limit detection, `nebula setup` (service, mod, launcher), and
Docker/systemd deployment, and dashboard sign-in through Claustra (OIDC code
flow with PKCE, Nebula's own signed session). Reset redemption and device
pairing remain proposed. The approach is a Claude Code mod plus a local companion for
Windows and Linux.
This document records the requested behavior and the boundaries found
in Claustra, Claude documentation, and native client inspection on 6 October 2026.

## Target clients

Nebula targets the Claude Code CLI and [T3 Code](#t3-code-as-the-desktop-frontend)
as the desktop frontend, on Windows and Linux. Claude Desktop is out of scope
(decided 6 October 2026): its sign-in runs on cookies inside the app, tokens use OS
secure storage, its signed build refuses automation, and no external
account-selection action was found. The Desktop findings below and in the
investigation are kept as background only; they are not implementation targets.

## Requested behavior

A Nebula user can connect several Claude subscription accounts, see their usage
and available resets, and choose the order in which accounts are used. When the
active account reaches a usage limit, Claude Code (in a terminal or through
T3 Code) should switch to another connected account that has usage remaining.

“Reset” means redeeming an available Claude usage reset. It does not mean clearing
a local usage counter, changing a login, or resetting Nebula's selection.
The user can request a reset from Nebula or ask Claude to request one through
the plugin. Both entry points require a fresh Claustra passkey verification for
that particular reset before it is carried out.

Automatic switching never spends a reset. If every account is exhausted, show
the next known replenishment time and offer an explicit reset request for an
eligible account.

## What the upstream interfaces support

Inspection of the native clients (Claude Code 2.1.291, Desktop 2.19675.1) found
the usage reader, OAuth handling, reset handling, and Desktop's multi-account
machinery. There is no public API documentation for these; treat them as
version-dependent integration candidates.

Claude's [limit reset documentation](https://support.claude.com/en/articles/17007452-what-is-a-limit-reset)
describes redeeming an offer through Settings > Usage on the web or in Desktop.
An offer can restore session or weekly limits, can expire, and cannot be undone
after redemption. The documentation does not expose a public redemption API.
Do not assume a reset always restores both limits or is available to every account.

Claude Code's [authentication documentation](https://code.claude.com/docs/en/authentication)
supports native subscription sign-in and separate credential storage through
`CLAUDE_CONFIG_DIR`, including separate macOS Keychain entries. A live process
does not gain a documented account-switching API from this setting. Environment
credential helpers also do not control Desktop's ordinary subscription login.

The [StopFailure hook](https://code.claude.com/docs/en/hooks#stopfailure) can report
a `rate_limit` error without a successful model response. Its output cannot
force Claude Code to continue a turn. Account selection and any restart/resume
therefore need a companion that manages the native process, rather than relying
only on an MCP tool call after the model has already hit its limit.

[Claude Code plugins](https://code.claude.com/docs/en/plugins) can bundle hooks,
skills, and MCP servers. Desktop can use a local MCP server. A
[remote Desktop connector](https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp)
runs through Anthropic's servers, so it cannot directly control a user's local
application or access a localhost companion.

Code's native usage reader calls `GET /api/oauth/usage` with subscription OAuth
and the `oauth-2025-04-20` beta header; the companion uses the same read. Code
also contains a reset reader and redemption call, the first candidate for a
reset adapter, subject to live validation.

Desktop reads the same usage API and contains native multi-account support,
subject to a feature gate and managed policy. Its account-change IPC validates
an already selected identity against `/api/auth/accounts`; it is not yet proven
to be a command that performs the complete switch. The hosted account-selection
flow still needs tracing. Its signed production build also restricts debugging
and ignores an unauthorized `CLAUDE_USER_DATA_DIR` override, so do not base the
adapter on an assumed debug port or arbitrary profile directory.

The local companion remains necessary to manage native credentials and processes.
Version-check private adapters, validate their responses, and report unavailable
capabilities accurately. A working Desktop switch, usage reader, and reset
executor must be demonstrated before advertising them as implemented.

## Selected local companion architecture

Nebula is a separate service and repository. It owns account associations, device
authorization, preferences, usage summaries, session archives, reset requests, and
audit history. Claustra remains the OIDC login and passkey step-up provider.
Nebula stores approvals after validating a fresh, action-bound Claustra login. A local
companion owns native Claude profiles and performs account operations on the
user's device. Claude Code gets a mod backed by that companion; T3 Code runs
the same Claude Code CLI, with Nebula choosing the account beneath it.

The companion targets Windows and Linux, with Claude Code CLI and T3 Code
integrations on both.

## T3 Code as the desktop frontend

T3 Code drives the Claude Code CLI through its `claudeAgent` provider and stays
unaware of which Claude account is in use (decided 6 October 2026). Nebula picks
the account beneath the CLI rather than registering one T3 Code provider
instance per account. T3 Code's Claude settings include `binaryPath` and
`homePath` (`CLAUDE_CONFIG_DIR`), seen in its installed settings schema, so a
Nebula launcher (`nebula-claude`) in `binaryPath` execs the real `claude` with
the selected profile. The launcher is built and tested with a stand-in CLI;
running it under T3 Code itself, and whether T3 resumes a thread after its
Claude process exits at a limit, are not yet validated.
Transcripts land in the selected profile's `projects` directory, which
`nebula sync` already archives. T3 Code's own thread database is not archived.

## Archive storage

Uploads are content-addressed: the companion splits each transcript at the first
newline after every 256 KiB, asks which chunks the server lacks, uploads those,
and commits a manifest. Append-only JSONL keeps earlier chunks identical, so a
revision costs its new tail. The server reassembles and validates every commit
(revision digest, JSONL, size and path limits), stores chunks gzip-compressed,
keeps the latest ten revisions per session, then hourly for two days, then
daily, and sweeps unreferenced chunks after a one-hour grace period. An index
file serves listing. Earlier whole-bundle archives are migrated on startup.

## Account sign-in

Accounts are connected with Claude Code's own `claude auth login`, run by the
companion with `CLAUDE_CONFIG_DIR` set to a new profile under the companion's
config directory. On the device the browser opens and the OAuth loopback
redirect completes the sign-in. The CLI also prints a manual sign-in URL and
reads a pasted code from stdin; the server relays that URL to the dashboard
and a pasted code back, so a sign-in can be finished from another computer.
The server accepts only https sign-in URLs on Anthropic hosts, keeps sign-ins
in memory for at most 10 minutes, and holds a code only until the device
collects it. The code is single use and bound to the CLI's PKCE verifier,
which never leaves the device. On success the companion names the profile
after the account, reports it, and the server connects the account. Claude
Code's mod can start the same flow with `/nebula-login`, limited to the
browser on the same computer because a mod cannot write to a running process.

Shared accounts use the same relay with the server as the device. The server
runs `claude auth login` into a vault profile, so the sign-in completes by the
pasted code. Renewal can replace the refresh token, so a sign-in copied to
several devices could sign the others out at the first renewal; instead only
the server ever renews, and devices receive access tokens alone. Claude Code
renews only right before it calls the API, so the server marks its stored token
expired and runs one minimal `claude -p` request, keeping the new token only if
it changed. Devices write the token to a profile marked `.nebula-shared`. A
running Claude Code whose in-memory token is past expiry, with no refresh
token, keeps sending it; the API's 401 makes it reread the profile and continue
with the newer token.

## Installation and first use

The user installs the Nebula plugin, then asks Claude to set it up. The agent
can run a bundled, deterministic setup command that detects the OS and CPU,
installs the companion, and checks the plugin connection. The companion does
not require a separately installed Go toolchain, administrator rights, or
manually edited configuration. Installation is part of setup, not something
the user must complete before the agent can help.

Ship pinned companion binaries for supported Windows and Linux architectures
with an authenticated release manifest and checksums. The installer verifies
the expected release and platform before executing a binary. It accepts no
model-selected executable URL or shell fragment. Repeated setup reuses a healthy,
compatible installation; an interrupted download leaves the working version
intact. Report an unsupported platform explicitly.

Install into the user's application data directory and configure startup through
the appropriate per-user Windows or Linux mechanism. Keep persistent device
state outside the plugin directory so plugin updates do not erase account
connections. Multiple Claude sessions share one companion, with separate managed
client instances; they must not create competing account-rotation daemons.

After installation, the agent opens a Nebula pairing page. The user authenticates
through Claustra with a passkey and authorizes the displayed device. The installer and
agent never ask for a passkey secret, browser session cookie, or copied bearer
token. Each Claude account is then connected through its native sign-in flow.
The agent can guide and open those flows, while the user completes sign-in.

For T3 Code, setup will point its Claude `binaryPath` at the Nebula launcher once
that exists, preserving the rest of T3 Code's settings.

The setup command reports installed version, supported client capabilities,
pairing state, and any remaining native sign-in or app-install step. Once paired,
asking “use a reset for my work account” creates the reset request and opens
Nebula verification. The agent can finish setup automatically, but cannot
complete the human passkey ceremony or silently spend an available reset.

Updates use the same authenticated release path and preserve device state.
Uninstall stops the companion's per-user startup registration, removes its
integration entries, and revokes the device identity when Nebula is reachable.
An offline uninstall provides the device identifier for revocation on Nebula.

## Pairing and credentials

The user pairs a companion from their signed-in Nebula page with a fresh
Claustra OIDC authentication backed by a passkey. Pairing grants a revocable device identity for this feature;
it does not give the device administrator rights or general access to Nebula.
The companion signs in through the native Claude flow for each account and
associates the provider account identifier and local profile with the Nebula
user. A typed email address alone is not proof of a connected account.

Claude passwords, cookies, OAuth tokens, refresh tokens, and Keychain contents
stay on the device. Nebula stores provider account identifiers and reported
usage metadata. Usage and identity reports from a paired device are attributed
to that device; they are not represented as independently verified by Anthropic.
The native client continues to own token refresh and sign-in.

The companion uses outbound authenticated HTTPS requests to Nebula. Browser
pages and remote MCP servers do not send arbitrary commands to localhost. The
device credential is scoped to the user and device, stored securely on the
device, and stored only as a hash on Nebula. The local MCP surface exposes
fixed account operations, never shell execution or raw credentials.

Existing Claustra OIDC access tokens are for UserInfo and avatars. Do not extend
their meaning to companion control implicitly. Device pairing and delegation
need their own explicit authorization, expiry, revocation, and rate limits.

## Server-backed Claude Code sessions

Session availability across machines is a core Nebula feature. The companion
collects complete native JSONL records from each configured Claude Code profile's
`projects` directory and the session's subagent JSONL files. It uploads them to
Nebula, which stores immutable, content-addressed revisions scoped to the Nebula
user, source device, and native session UUID. Repeated uploads are idempotent;
independent devices retain separate histories rather than overwriting each other.

Preserve the raw native transcript, including tool results, parent links, compact
summaries, and session metadata. Ignore an incomplete trailing record while Code
is writing. Do not upload OAuth credentials, settings, browser storage, or whole
workspace directories. Transcripts can themselves contain private prompts, code,
and tool output: archival deliberately stores that content on the user's server.

The server and companion list archived revisions and fetch an exact revision or
the newest one for a particular device/session. Restore recreates the native
project/session layout in a selected local Claude Code projects directory. It
verifies the content digest, confines paths, refuses symlinks and differing
existing files, and makes the main transcript visible after the subagent records.
An explicit restore does not run Claude, execute tool calls, copy workspace files,
or rewrite project paths. Cross-machine workspace mapping and automatic native
resume still need client validation.

`nebula sync --watch` periodically uploads changed complete snapshots and retries
after temporary server failures. Offline transcripts remain local. Every source
device has a stable identifier; session IDs and project names are preserved.
Server storage must survive restarts and be included in backups. Archive deletion,
retention policy, a searchable session page, and OIDC-authorized device setup are
subsequent work; the initial archive never silently expires stored revisions.

The initial mod offers `/nebula-sync` and `/nebula-sessions`, plus periodic
companion synchronization. Fetch/restore remain explicit companion commands.
Restoring needs an explicit destination/workspace decision and must
not automatically execute the transcript or replay completed tool effects.

## Claude Code mods

Use a mod as the Code-facing plugin entry point. Anthropic publishes the
[function-hook source and declarations](https://github.com/anthropics/claude-code/tree/main/mods),
including session lifecycle, process execution, timers, commands, and UI hooks.
The initial Nebula mod uses these hooks to run a configured companion without
a shell and to expose archival commands. The independent companion watcher
remains responsible for final writes, crashes, offline retries, and profile-wide
history imports. Function hooks are early access and version-dependent.

Later use custom UI for account/usage state and pending reset approvals. A mod
does not establish a supported subscription-switch or reset protocol, remove the
need for the OS-specific companion, or extend Code hooks to ordinary Desktop chat.
Reset execution still requires a fresh action-bound Claustra login. The short
session-end hook budget means periodic mod sync alone cannot guarantee the last
shutdown records; keep a companion watcher running.

## Usage and switching

Each observation records the account, source device, observation time, relevant
usage windows, exhaustion state, replenishment times, and reset offers with their
provider identifiers, scope, and expiry. Unknown or stale usage is shown as
unknown. Expired observations cannot establish that an account has usage left.

Selection is deterministic: choose the first enabled, authenticated account in
the user's priority order with fresh evidence that the relevant limits have
remaining capacity. An account with an exhausted weekly or model-specific limit
is not eligible merely because its session window has capacity. Recheck the
candidate immediately before use. Accounts need not have identical model access.

On a native usage-limit event, mark the affected account/window exhausted and
select a candidate. Distinguish subscription exhaustion from overload, temporary
request throttling, authentication failure, and billing errors. A generic HTTP
429 is not sufficient evidence to rotate accounts.

Serialize switches for each managed client instance. Pin every running instance
to an account; switching one instance must not overwrite credentials under
another. Limit attempts to one pass through the eligible accounts, then stop
with a visible explanation instead of looping.

For Code, the companion launches the native process with its selected profile.
Native credential-change detection exists, but changing a token alone does not
establish a complete switch: provider account/organization metadata, usage state,
and running sessions also need to agree. Validate this before choosing between
an in-process switch and a managed restart/resume.
Any restart/resume must be proven to retain the intended local session and must
not replay completed tool calls or automatically resubmit a partially executed
user request. A switch is successful only after the native client confirms the
selected account. Under T3 Code the switch happens beneath the CLI it launches;
validate that the T3 thread's session continues on the new account before
claiming it.

For background only (Claude Desktop is out of scope), tracing Desktop's switch
showed that Desktop reinitializes account/organization session storage and may
park eligible running Code sessions under their original identity, restoring
them when switching back. Other sessions can be torn down. Selecting a different
Desktop account therefore does not by itself route the limited task to that
account. Validate the companion's task handoff separately, including ownership
of any provider-hosted conversation and preservation of local tool effects.
Code's sibling-login adoption only runs for a missing/invalid login; an accepted
structured token update also does not confirm that account metadata changed.

## Reset approval and execution

Both the Nebula page and `request_usage_reset(account_id, offer_id)` create the
same server-owned reset request. The MCP result contains a request identifier,
status, and a fixed Nebula approval URL. Claude can request approval and read
status; it cannot approve a request or receive a reusable verification token.

The approval page names the connected account, requesting device, reset offer,
limits affected, and offer expiry. It explains that the reset is consumed and
cannot be undone. Opening the page does not approve the operation.

The user chooses **Verify and use reset**, which starts a new Claustra OIDC
authorization using `prompt=login`, `max_age=0`, PKCE S256, fresh state, and a nonce
bound server-side to the immutable reset request, user, account, organization,
grant revision, device, provider request ID, and approval browser transaction.
Nebula exchanges the code itself and verifies the ID token's signature, issuer,
audience, nonce, expiry, and `auth_time`. Require authentication after the request
was created and approval was initiated, with a short transaction expiry; consume
the transaction once. A preexisting Nebula session or login for another request
cannot approve it. Nebula does not host a second passkey credential database.

Claustra's existing `requireRecent` five-minute session window is insufficient for
a particular reset. Its OIDC `prompt=login` behavior already supplies fresh
authentication; Nebula must validate and bind the result rather than changing
Claustra's account page or WebAuthn challenge schema.

After verifying the action-bound Claustra authentication, atomically transition the request from pending to
approved. The authorized device claims it once with a short execution lease.
Before redeeming, verify the native client's account and the current offer match
the approved binding. Reject an expired, consumed, or changed offer; do not
substitute another offer or account. Read-only eligibility checks can happen
before approval; redemption cannot.

Include the provider organization, the exact offer, and a stable provider request
identifier in the immutable approval binding. Respect the offer's availability
and the windows it actually clears. Keep the request identifier unchanged through
reconciliation, and verify the provider's idempotency before enabling retries.

Track requests as `pending`, `approved`, `executing`, `succeeded`, `failed`,
`cancelled`, `expired`, or `unknown`. Approval and lease expiry are separate from
the provider offer's expiry. A network timeout after redemption produces
`unknown` until reconciled against provider state. Never automatically redeem
again after an ambiguous result. Provider idempotency, when available, uses the
same request identifier across retries.

Browser GETs are read-only. Request, cancellation, and approval POSTs require
CSRF protection and ownership checks. Device endpoints require the paired device
identity and ownership checks. Disconnecting an account, revoking a device,
suspending access, disabling an account, or scheduling account deletion
invalidates pending approvals and prevents new execution claims. An operation
already sent to Claude cannot be retroactively undone.

Audit request, approval, execution claim, provider result, cancellation, and
reconciliation with account/request/device identifiers. Do not log secrets,
browser storage, prompts, or conversation contents.

## Nebula page and plugin behavior

Nebula provides a Claude accounts page with connected accounts, active devices,
per-client active account, priority, observation age, usage windows, next
replenishment time, and available reset offers. It provides connect, disconnect,
manual switch, enable automatic switching, reset request, cancellation, and
device revocation controls. Unsupported operations show their actual capability
state. An offline device cannot complete a server-initiated reset.

The MCP surface provides `list_accounts`, `get_usage`, `switch_account`,
`request_usage_reset`, and `get_reset_status`. Tool results are scoped to the
paired user and contain no native credentials. List and usage tools report
observation age. Switch results identify whether switching completed or a
native restart/sign-in is required. A reset request reports pending verification
until Nebula approves it, and reports success only after confirmed redemption.

## Implementation gates and acceptance criteria

The integration approach and target systems are selected: an agent-installable
local companion for Windows and Linux, targeting the Claude Code CLI and T3 Code.
Validate Code profile login/launch/resume, T3 Code continuation through the
launcher, usage observations, and reset redemption on both operating systems.
Use the native protocols in the investigation as the starting point.

The initial session archive uses per-user bearer credentials provisioned by
`nebula init`, filesystem snapshots, and explicit or watched local synchronization.
It does not yet implement OIDC web login, device pairing, or the account/reset UI.

Then implement account/device storage and pairing, usage reporting, selection,
the reset state machine and operation-bound Claustra step-up, the Nebula page,
and expanded plugin/companion operations. Extend maintenance, recovery, deletion, and restored
backup invalidation to cover the new ephemeral credentials and approvals.

The completed feature must demonstrate:

- From the installed plugin, the agent installs and connects the companion on
  Windows and Linux without administrator rights or a Go toolchain; repeat setup
  preserves existing accounts and credentials. The user completes native sign-in
  and Claustra passkey ceremonies without exposing their secrets to the agent.
- Two native Claude accounts connected to one Nebula user; another Nebula
  user cannot inspect or operate either connection.
- A genuine usage-limit event switches a managed Code instance to an eligible
  account, preserves the intended session, and does not replay tool effects.
- A T3 Code thread continues on another account through the Nebula launcher,
  verified against the supported T3 Code version; unsupported versions do not
  claim to have switched.
- Exhausted, disabled, expired-login, and stale-observation accounts are excluded;
  exhaustion across all accounts stops rotation without spending a reset.
- Page and MCP reset requests each require a new assertion for the exact request;
  a fresh ordinary login alone cannot redeem it.
- Duplicate approval, claim, and redemption attempts cannot spend a second reset;
  ambiguous provider results reconcile without blind retry.
- Wrong-user assertions, altered account/offer bindings, expired challenges,
  device/account revocation, recovery quarantine, and disabled/deleting Nebula
  accounts cannot authorize execution.
- Actual provider redemption updates the displayed state, while denial,
  unavailability, and device disconnection produce accurate visible statuses.

No acceptance item is satisfied merely by updating Nebula's own counters or
by rendering controls without a working native operation behind them.
