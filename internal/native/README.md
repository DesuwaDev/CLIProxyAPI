# Native management modules

This directory contains optional features compiled into CPA. There is no second
server, loopback management client, usage-queue consumer, or CPAMP dependency.
The original proxy/executors remain responsible for model traffic and scheduling.

## Feature boundaries

| Directory | Responsibility |
| --- | --- |
| `event` | Adapt upstream `usage.Record`, canonical token accounting, hash caller keys, classify errors |
| `storage` | SQLite connection and per-module schema versions |
| `history` | Request-attempt persistence, filters, aggregates, cursor export and key aliases |
| `pricing` | Explicit model/provider/tier/context price rules; immutable per-event cost snapshots |
| `accounts` | Runtime observation history and a human-operated credential issue queue |
| `diagnostics` | Bounded downstream POST observation, SSE error detection, correlation and final outcomes |
| `limits` | Persisted per-key/credential admission policies and process-local rolling counters |
| `fingerprint` | Opt-in Codex OAuth account identity seeds and outbound transforms |
| `headers` | Opt-in Codex OAuth proxy-header cleanup and preservation of supplied client identity |
| `wire` | Opt-in Codex OAuth transport that reproduces the official CLI's TLS ClientHello, HTTP/2 preface and header order, zstd request bodies and cookie jar |
| `risk` | Opt-in preflight content audit, reviewer nodes, events and scoped TTL blocks |
| `inventory` | Durable lifetime usage totals, account/key profiles and quota-window estimates |
| `controls.go` | Host identity lookup and execution-policy adapter |
| `web` | Generated single-file original React management panel, compiled into CPA |
| `runtime.go` | Module registry, lifecycle, event fan-out, retention and module settings |

Feature constructors own their migrations and routes. The runtime registry is the
composition root. To add a feature, create a sibling package, register its schema
and routes here, and add its event hook if needed. Feature packages must not import
`internal/api` or `sdk/cliproxy` (the service package). They may consume SDK contracts.

## Upstream integration surface

- `internal/config/config.go`: one `NativeManagement` configuration field, with its
  type in `native_management.go`.
- `internal/api/server_options.go`: generic `ManagementExtension` interface.
- `internal/api/server.go`: hold the optional interface from server options.
- `internal/api/server_management.go`: register behind existing management middleware;
  serve the original React panel extended in place, with no alternate panel URL.
- `sdk/cliproxy/service_native.go`: adapt runtime account snapshots and attach modules.
- `sdk/cliproxy/service.go` / `service_lifecycle.go`: own, start and close the extension.
- `sdk/cliproxy/usage/manager.go`: an additive `Wait(ctx)` drain barrier. Existing
  `Stop()` remains non-blocking. Shutdown closes SQLite only after queued events drain.
- `internal/logging/observation.go` and one context-copy line in
  `sdk/api/handlers/handlers.go`: preserve an opaque observer UUID across the SDK's
  detached execution context. Existing request IDs and cancellation remain unchanged.

The shutdown deadline starts when shutdown begins, not when the service starts.
This is necessary for persistence when CPA has been running for over 30 seconds.

## Data contract

- An event is one provider attempt, including failures and retries. Request IDs are
  correlation keys, **not** deduplication keys. Each event has an independent UUID.
- Canonical upstream token buckets are used without re-deriving provider semantics.
  Cache is a subset of total input; reasoning is a subset of total output in the
  normalized contract. Do not add either twice when estimating costs.
- Unclassified/inconsistent accounting, missing price rules and non-generation
  requests remain unpriced (`null`). The UI exposes unpriced counts separately.
- Pricing uses actual model names. Provider and tier wildcards must be explicit.
  More specific provider/tier rules win; at equal specificity the greatest
  applicable `min_context` wins. Currency is USD, rates are per million tokens.
- No API keys, OAuth metadata, source credentials, prompts, arbitrary upstream
  bodies or arbitrary response headers are written to the event database. An
  allowlisted upstream request ID, observed service tiers and reasoning effort may
  be retained; missing metadata is never invented. Errors retain only
  allowlisted classifications; API key identifiers use SHA-256.
- Costs are local estimates, not an authoritative billing ledger. Price changes
  do not reprice historical events.
- Account inspection records CPA runtime observations every 15 minutes and on
  demand. It does not actively call provider quota endpoints or modify credentials.
  Unknown is not interpreted as healthy. Existing CPA cooldowns remain authoritative.

## Operational behavior

`native-management.enabled` is opt-in and requires restart. Database path and
retention also require restart. Module defaults in YAML apply to a new database;
subsequent module switches are managed in the UI/SQLite. Turning off a feature does
not delete its data. The native sink is independent of the legacy
`usage-statistics-enabled` HTTP queue switch and does not consume that queue.

The existing asynchronous usage dispatcher serializes writes. SQLite uses WAL,
FULL synchronization, a busy timeout, and one connection; queries/downloads are
bounded so slow clients do not hold the connection during export. The UI reports
write/maintenance failures and per-process counters; disk failure can prevent event
persistence and must not be mistaken for complete history. An abrupt process kill
can lose events still queued in memory. Graceful shutdown drains accepted events.

Diagnostics has a separate 1024-record nonblocking queue and reports overflow/write
failure counters. It observes POSTs under `/v1`, `/v1beta`, `/openai/v1`, and
`/backend-api/codex`, excluding upgrades. A transient SSE scanner keeps at most
64 KiB per line and stores only outcome metadata. Unsupported/oversized data marks
inspection partial. HTTP completion is not proof of model success; WebSockets and
legacy rows are not retroactively correlated. The wrapper preserves response bytes,
status and flushing. An opaque UUID connects the downstream row with usage events.

History schema v2 adds nullable observed TTFT and explicit latency-observation flags.
Positive legacy timings migrate; missing legacy zero timings remain unknown. All
pending migrations for one module are transactional. Back up before upgrading;
restoring an older binary requires its pre-upgrade database, not a schema downgrade.

Pricing's separate catalog schema stores the last successful LiteLLM-format token
catalog. Automatic refresh defaults off and uses the configured interval (1–168h).
Local provider/tier/context rules always win. Catalog fallback uses exact model
names, actual response tiers, OpenAI fast/priority aliases, flex/batch prices, and
dynamic above-N-token thresholds (including 272k). Missing long-context service-tier
prices remain unknown. Claude usage.speed is observed independently of its request;
observed 5m/1h cache writes use separate rates. Ambiguous TTL or missing prices stay
unpriced. Unknown context formats are excluded. Downloads are bounded to 16 MiB; errors,
cancellation or source changes do not replace the previous catalog. Network I/O
does not hold DB/module locks and never changes inference transports. The source
hash identifies the pricing snapshot; it is not an authenticity signature.

Retention defaults to 90 days and deletes old events/snapshots in batches of 10000
every 15 minutes; large expired backlogs may take several cycles to clear. SQLite
reuses freed space; retention does not guarantee immediate file shrinkage.
Pending account actions are retained until handled. Database schema downgrade is
refused. Stop CPA before copying the database for a backup; preserve `-wal`/`-shm`
files if present. This implementation does not migrate an existing CPAMP database.

## API

All data endpoints use CPA's existing management authentication and availability
middleware under `/v0/management/native`:

- `GET /status`, `PUT /modules/:name` (`{"enabled": true}`)
- `GET /history/events`, `/history/summary`, `/history/export`
- `GET/PUT /history/aliases`
- `GET /history/analytics`, `/history/models`, `/history/trace/:id`
- `GET/PUT /pricing` (`{"rules": [...]}`)
- `GET/PUT /pricing/catalog`, `POST /pricing/catalog/sync`, `GET /pricing/catalog/model?model=...`
- `POST /identity` with exactly one of `key` or `credential`; returns a hash/index, not secrets
- `GET/PUT /limits/:scope/:target`, scope `client` or `credential`, policy `{ "rpm": 0, "concurrency": 0 }`
- `GET/PUT /fingerprint/:target`, policy `{ "mode": "off" | "device" | "session" | "full" }`
- `GET/PUT /headers/:target`, policy `{ "mode": "off" | "clean" | "client" }`
- `GET/PUT /headers/client-version`, settings `{ "manual_version": "", "automatic": false }`
- `POST /headers/client-version/sync`, fetch the official stable CLI version without downloading software
- `GET/PUT /wire/:target`, policy `{ "mode": "off" | "codex", "compress": true, "cookies": true, "routing_hint": true }`; GET also returns per-credential connection counters
- `GET /wire/profile`, the compiled-in profile summary (client version, TLS, HTTP/2, body, cookies, WebSocket); read-only
- `GET /inventory`, `GET/PUT /inventory/:scope/:target`, `PUT /inventory/windows/:target`
- `GET/PUT /risk/config`, `GET /risk/status`, `GET /risk/events`, `GET /risk/blocks`
- `DELETE /risk/blocks/:kind/:target`, `POST /risk/test`
- `GET /diagnostics/requests`, `/diagnostics/requests/:id`
- `GET /accounts`, `POST /accounts/inspect`, `GET /accounts/history?account=...`
- `GET/PUT /accounts/actions`

History filters: `from` (inclusive epoch milliseconds), `to` (exclusive), `model`,
`provider`, `account`, `key_hash`, `request_id`, `failed=true|false`. Default range
is 24 hours, maximum 366 days. Events/export accept `limit=1..500` and `before`
(exclusive legacy sequence cursor), `sort=time|recent|latency`, `min_latency`,
`max_latency` and opaque `cursor`. Events return `next_cursor`; exports return
`X-Next-Cursor`. Reuse the same filters and sort while paging. New cursors carry a
fixed maximum sequence and use duration/sequence pairs for slow sorting, so late
arrivals and tied durations do not duplicate or skip rows. Retention can still
remove records during a long export. Legacy `next_before`/`X-Next-Before` remain
available for legacy ingestion-ordered clients. `recent` uses request timestamp / sequence pairs and requires the opaque cursor for pagination; it is the default in the new UI, while `time` preserves the legacy ingestion order. The UI exports JSONL and actual XLSX with
inline string cells (no formula interpretation), progress and cancellation.
Summary accepts `group=model|provider|account|key|status|hour|day`; no group returns
overall totals. Group results are capped at 1000 with an explicit truncation flag.
UI export is bounded at 50000 events or 64 MiB; use narrower ranges above that.

Analytics uses exact nearest-rank percentiles from successful observed samples;
TTFT excludes nonstreaming/missing measurements. RPM/TPM divide provider-attempt
totals by the full selected window. Cache read share divides cache-read by input
tokens; no input means null. Models are capped at 500 and trace attempts at 500,
with truncation flags. Diagnostics lists 100 rows per page with `next_before`,
accepts `from`, `to`, `request_id` and `outcome`, and counts downstream requests.

## Updating upstream

Keep the existing origin; inspect which fork/branch is the intended upstream before
merging. Merge upstream normally, resolve the small integration surface above,
and keep feature-specific changes in this directory. Run:

```sh
go test ./internal/native/... ./sdk/cliproxy/usage
go test ./internal/api -run TestNativeManagementUsesExistingAuthorization
go test -race ./internal/native/... ./sdk/cliproxy/usage
go build -o bin/cli-proxy-api-native ./cmd/server
./scripts/build-native.ps1
# In management-ui, after frontend changes:
npm exec --yes --package=bun@1.3.14 -- bun run verify
```

Broaden tests when upstream changes usage accounting, management authentication,
request lifecycle, or auth snapshots. Do not regenerate unrelated files.

## Removing features

For operational removal, switch the module off. For source removal, delete its
package and remove its constructor/route/event wiring in `runtime.go` plus its UI
entry. Retain old schema/data unless an explicit data migration removes them.
To remove the entire extension, remove the integration points listed above and
the SQLite dependency. Upstream model execution code does not depend on modules.

## Unified original panel

`management-ui/` vendors the original React frontend at the commit documented in
its UPSTREAM.md. Additions use its login, shared authenticated API client, sidebar,
router, components and theme. Feature code is isolated under
`src/features/nativeManagement/`; there is no separate login or original-panel
switch. Its build produces `web/management.html`, embedded into CPA. The ordinary
Go build uses the checked-in bundle. Regenerate it whenever frontend code changes.

## Admission and identity integration

The host installs an optional per-manager SDK ExecutionPolicy. It runs for every
standalone provider attempt, including refresh retries, token counting and streamed
attempts. A local denial skips credential health/cooldown mutation and custom upstream
error actions. Remaining credentials can be tried; an all-busy pool returns 429.
Caller admission runs after existing access authentication and covers the whole
request (one WebSocket connection occupies one caller slot). Credential stream slots
are released after the producer closes, including cancellation draining. No network
timeout is introduced. Home scheduling is unaffected.

Zero RPM/concurrency means unlimited. Both fields are mandatory on PUT. RPM counts
accepted starts in a rolling 60-second window, with a maximum configured value of
100000; concurrency is capped at 10000. Edits count already-active requests and the
existing minute's starts. Counters are single-process and reset on restart; policy
rows persist. Turning a module off bypasses enforcement for subsequent admissions.
Policies use the caller key hash or CPA auth index. Replacing a key, base URL or file
identity can create a new target; policies are intentionally not transferred between
credentials automatically. Deleted targets' dormant policies remain until explicitly
changed or the module is removed.

Codex OAuth fingerprint modes default off per account. Random per-account seeds
persist through refresh, toggling and restarts. Device mode changes installation
identity only; session mode unifies the account session but derives caller-isolated
threads from the original client session/thread. Full mode deliberately shares the
thread. Turn metadata is resolved once per attempt and mirrored into headers/body.
Explicit custom prompt-cache keys and unrelated JSON fields are preserved. A cache
key equal to the original body session is updated with that session. This changes
application request identifiers, not TLS fingerprints. It does not guarantee cache
hits or compatibility with every upstream client/version.

Header normalization defaults off per Codex OAuth account and is managed in the
original credential editor. `clean` removes proxy-chain and hop-by-hop headers,
including Connection-nominated fields, and removes stale body framing/encoding
headers because the executor sends a rebuilt JSON body. HTTP and WebSocket
transports generate their own framing/handshake fields. Upstream authentication,
account ID, configured account cookies, Content-Type and Accept remain executor-owned.
No caller Authorization or Cookie is copied. Other upstream custom headers remain
in place unless they are proxy/connection/body-framing fields covered by cleanup.

The same module owns optional global Codex CLI version settings, shown on the
original Modules page. Manual versions take precedence over the persisted official
stable-release cache; the compiled default is a floor for automatic values, never
for an explicit manual pin. Automatic refresh defaults off and runs every six
hours when enabled. It queries only openai/codex on GitHub, filters rust-v stable
tags, skips drafts/prereleases/unrelated components, and retains previous values
on errors or older releases. A recent persisted check prevents repeated downloads
on restarts. Turning off automatic sync retains its cache; disabling the headers
module suspends overrides and automatic I/O. Disabling either cancels active
automatic downloads. HTTP requests do not wait for background synchronization.

Version declarations are shared through internal/codexidentity. Both UA version
positions and an existing Version header change together; absent Version remains
absent. Unrecognized custom agents are not rewritten. A valid preserved client
identity takes precedence over the global version policy. Account cleanup being
off does not disable a configured global version policy. Existing WebSocket
identity reconciliation observes version changes through its stable header hash.

`client` also restores a valid, unambiguous UA/Originator pair from the caller and
preserves whether Version was supplied. An incomplete pair keeps executor defaults;
the module does not invent a client profile. It only forwards an incoming opaque
X-Codex-Routing-Hint when the incoming and selected upstream Chatgpt-Account-Id
match. It does not synthesize cookies, compress bodies, modify TLS, or guarantee
that the proxy cannot be identified. Policy rows store only target and mode.

Header transforms run before optional fingerprint transforms, after the executor
has built headers. A header-only policy does not disable legacy identity-confuse.
Changes to stable normalized handshake fields, or disabling the header module,
invalidate reused WebSocket connections through the existing replay mechanism.
Per-turn request IDs are excluded from that handshake comparison. Module toggles
apply to subsequent attempts; an already-started attempt keeps its policy snapshot.

The SDK context outbound-transform hook is consumed at the final Codex HTTP,
compact and WebSocket send paths. Opted-in transforms supersede existing global
identity confusion. WebSocket sessions invalidate stale handshake identities on a
mode change and use existing reconnect/replay behavior; connection/session routing
keys are otherwise unchanged. Removing the feature requires removing the optional
host policy and transform call sites, not rewriting scheduler/executor ownership.

Review upstream merges at the execution-policy call sites in conductor_execution,
conductor_stream and the credits fallback; local error handling in conductor_cooldown,
conductor_selection and conductor_request_scoped_errors; authenticated middleware;
Codex HTTP/WS final send sites and session reconciliation. Usage metadata additions
are confined to SDK Detail and the Claude parser/stream merge helper. Generic hooks
are inactive when no native runtime is installed.

Catalog payload semantics are versioned as price_catalog schema v2. The second
migration is a version barrier with no table rewrite; existing standard-only
catalog data remains readable. An older binary refuses this schema instead of
silently flattening service tiers. The host also permits trusted local Retry-After
through the existing SafeResponseHeaders allowlist in home_concurrency.go; no
arbitrary upstream headers are promoted to trusted headers.


## Wire profile integration

The `wire` module changes only how bytes reach `chatgpt.com` for a Codex OAuth
credential that opted in. It does not change the request body, model, prompt,
identifiers or account, and it never applies to API-key credentials or other
providers. Software identity stays with the `headers` module; device and
conversation identifiers stay with the `fingerprint` module. The intended stack
for own-use accounts is `routing.session-affinity: true`, headers mode `client`,
fingerprint mode `device` and wire mode `codex`. Fingerprint modes `session` and
`full` rewrite conversation identifiers and are not part of that stack.

Mode `codex` reproduces codex-cli 0.154.0 (reqwest 0.12.28 / rustls 0.23.36 /
hyper 1.8.1 / h2 0.4.16): the rustls ClientHello with per-connection randomized
extension order and no session resumption, the hyper SETTINGS and WINDOW_UPDATE
preface, the `/responses` header order, h2-style HPACK indexing, a libzstd-shaped
zstd request body, a per-credential cookie jar and, when the client did not send
one, `x-codex-routing-hint` derived from the requested model. WebSocket dials use
the same TLS profile without ALPN and the CLI's upgrade header order.

The management UI exposes all of this on one page, `/codex-disguise` (sidebar
group "control", shown whenever the native runtime is present). It lists every
Codex OAuth credential with its headers, fingerprint and wire modes, shows the
three module toggles and the `routing.session-affinity` switch, and offers
"apply recommended to all" / "turn off for all accounts". The session-affinity
switch rewrites only that one key in `config.yaml` after re-reading the file.
The per-credential editors in the auth-file sheet remain as the detailed view.

Wiring: `internal/native/controls.go` attaches a `WireTransport` and a header
transform in `BeforeExecute`; `sdk/cliproxy/executor/wire_transport.go` carries
them on the context; `helps.NewUtlsHTTPClient` uses the round tripper for
`chatgpt.com` only, and `codex_websockets_connection.go` uses the TLS dialer. The
header normalization runs again after the fingerprint transform because that
transform rewrites session headers. Mode `off` leaves every existing transport
path untouched, and disabling the module at runtime removes the hook for
subsequent attempts.

The profile is version-pinned. When the official client's network stack changes,
recapture with `cmd/codex_wire_capture` (listener), decode with `chparse`, and
compare with `selftest`; the values in `ProfileSummary()` and the tables in
`internal/native/wire` are the only places to update. Verified on 2026-09-11:
the real edge accepted GET and zstd POST over HTTP/2 through Cloudflare, and the
ClientHello, HTTP/2 preface, header order and zstd frame header matched the CLI
byte for byte across twelve consecutive connections.

## Risk and inventory integration

The optional SDK ExecutionPolicy runs profile checks, risk review and credential
admission before executor calls. Local risk/key rejections stop request failover
without recording credential health failures. Credential profile rejections can
select another credential. ExecutionObserver receives terminal errors and stream
errors using an immutable request-context snapshot; it never reads pooled Gin
contexts from stream goroutines. Only structured cyber_policy error codes can
create caller-scoped session blocks. Reused execution sessions check every turn.

Risk mode defaults off. Blocking reviews text, instructions and tool data before
business generation; observe mode queues up to 64 jobs and never delays forwarding.
Four workers discard obsolete policy revisions. Connected I/O uses cancellation,
not network timeouts. Endpoint failover and chunking are bounded; invalid/partial
reviews fail closed by default. No raw prompts or reviewer responses are persisted.
Reviewer tokens are a separate configuration secret: AES-GCM ciphertext is stored
in SQLite, and the random key in <database>.risk.key. Back up both together; the
API only exposes has_token. Redirects never forward reviewer credentials. A remote
reviewer receives the extracted content, so use a suitable local or trusted node.

Inventory totals are seeded once from retained history and increment in the usage
transaction. Retention never deletes totals/profiles. Collection gaps and external
usage cannot be reconstructed. Quota estimates divide priced window usage by the
observed used percentage, always disclose partial coverage, and become unknown
for zero usage/progress, missing prices, stale or expired snapshots. Reuse quota
observations at their original timestamp; never refresh a stale cache by changing
its timestamp. Budgets are advisory, not a billing-enforced balance. Client names
also update legacy history aliases transactionally.

Frontend integration lives in the native feature registry/provider/API adapters,
CredentialControls, original API key list and AuthFileCard. Remove registry entries,
module construction, usage hooks and these optional slots to remove a module;
remove the generic SDK policy/observer hooks only when no remaining module uses
them. The upstream proxy remains responsible for generation, scheduling and quota.

Reference designs inspected in the adjacent workspace: CPA-Manager-Plus commit
1ae656c82990c480f3f104326a08c6e0001eeb4c (MIT) and sub2api commit
98d86915becae9fe9491a91ffc6defd5235c8d2b (LGPL-3.0). These native packages are an
independent adaptation of the workflows, not copied application source. The
original vendored management UI retains its existing license and upstream pin.
