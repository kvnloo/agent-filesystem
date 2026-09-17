# Single-tree workspaces

Status: completed
Owner: Codex (root, storage_safety, ui, cli_sdk)
Created: 2026-09-17
Updated: 2026-09-17

## Goal

One workspace owns one existing file tree and its checkpoints. Preserve tree IDs,
contents, permissions, metadata, and useful product features while retiring the
separate composition model. Cooperate safely with current AFS clients.

## Scope

Control-plane routes/auth/lifecycle, storage generation and revision fencing,
explicit migration reports, UI, CLI, SDKs, current documentation, isolated QA.
The reference checkout at `~/git/afs` is read-only. No existing user databases,
catalogs, configuration, services, or installed binaries may be changed.

## Checklist

- [x] Adapt storage publication and restore safety from current AFS.
- [x] Retire composition HTTP/MCP/auth workflows without widening permissions.
- [x] Provide dry-run migration reports and explicit controlled adoption.
- [x] Adapt existing tree UI and background providers to Workspaces.
- [x] Simplify CLI, SDK, generated examples, and documentation.
- [x] Run appropriate Go/race/real-Redis/frontend checks.
- [x] Verify rebuilt UI against isolated backend and visually check main flows.
- [x] Record limitations and migration instructions; archive with evidence.

## In Flight / Remaining

None. The isolated preview remains running for the user. Real database/catalog
migration is intentionally a separate operator action, following the migration
guide and a reviewed dry-run report.

## Decisions / Blockers

- Existing `/v1/workspaces` tree contracts remain canonical.
- Composition `/v2/workspaces` routes retire explicitly; composition IDs must
  never silently resolve as tree IDs.
- Existing compositions remain preserved until their disposition is established.
- Server checkpoint creation captures published Redis state only.
- Independent safety review found dirty-marker throttling, disabled Pub/Sub also
  disabling the durable journal, root replacement lease-loss races, and stale
  metadata writes that could overwrite checkpoint heads. All were fixed with
  regression tests, including injected lease takeover and concurrent checkpoints.
- The reference assessment's suggestions to remove database/auth/search/history
  are superseded by the user's explicit preservation requirements.

## Verification

- Targeted migration/auth/retired-route/HTTP regressions passed against temporary
  miniredis and catalogs; catalog read-only and scope/grant regressions also pass.
- UI production build and 71 tests passed (17 files); browser lifecycle and visual QA passed.
- User preview: `http://127.0.0.1:18791/workspaces`; disposable Redis on 16391,
  catalog/binary/config under `/tmp/afs-single-tree-preview`. User explicitly
  requested it running; leave this preview up. Existing services unchanged.
- Python SDK 39 tests, TypeScript check and 9 tests, full CLI tests, and targeted CLI race tests pass.
- Actual current AFS binary interoperability passed on disposable Redis: mount,
  local edit/sync, stale revision rejection, restore fences, checkpoint metadata,
  fork discovery and deletion. Evidence: `/tmp/afs-storage-interop.log`.
- Full `go test ./cmd/... ./deploy/... ./internal/...` passed after final changes:
  `/tmp/afs-single-tree-go-tests-final.log`.
- Full mount `go test -race ./...` passed; backend/query/native publication races
  and current-client integration passed:
  `/tmp/afs-storage-mount-race-final.log`,
  `/tmp/afs-storage-backend-race-final.log`,
  `/tmp/afs-storage-settings-race.log`.
- Final migration/auth race tests passed:
  `/tmp/afs-single-tree-migration-race-final.log`. CLI read/grep races passed:
  `/tmp/afs-cli-read-safety-race.log`.
- Migration executable passed against real disposable Redis and SQLite. Dry run
  made no changes; apply preserved every primary Redis key's DUMP and the SQLite
  file bytes. Existing generations remained unchanged; missing markers and
  compatibility metadata archives were created, richer archives merged safely.
  Tested single/multi-tree compositions, read-only grants, bookmark references,
  name/ID collisions, missing attachments, stale reports (including changed
  archives), stopped-writer enforcement, idempotence, and explicit Redis-only
  mode. Harness/log: `/tmp/afs-migration-e2e/verify.py` and `validation.log`.
- Browser QA through the actual disposable backend covered creation, browsing,
  content display, checkpoint creation, edits and comparison, restore with a
  safety checkpoint, fork, rename preserving ID, deletion, settings, search,
  Monitor/API Keys and error states. Existing component styling was preserved.
- Final preview binary was rebuilt and only the task-owned preview restarted.
  Its served HTML exactly matches `internal/uistatic/dist/index.html`; all 26
  referenced assets return HTTP 200. Final browser list/detail renders correctly
  with no console warnings or errors.
- `make commands`, UI embedded build, SDK validation, and LiveSkills tests passed.
  `git diff --check` passed.

## Compatibility / Validation Limits

- Current direct clients do not emit control-plane session heartbeats or rich
  per-file version history. Keyword chunks catch up through the durable journal;
  exact grep scans published files. Embedding refresh remains explicit.
- Restoring a checkpoint fences old mounts, which must reconnect/remount. Server
  checkpoints capture published Redis state, not every client's pending files.
- Migration catalog inspection supports SQLite; PostgreSQL ownership/credential
  inventory needs separate review. Legacy composition credentials fail closed
  and require reviewed per-tree reissuance; no automatic permission widening.
- Redis Array integration remains conditional because no Array-enabled Redis
  was configured. Standard real-Redis publication and full mount race tests ran.
- The UI's separate strict TypeScript check has existing baseline errors (137
  before, 129 after; no new distinct errors). Production Vite build and all 71
  UI tests pass. SDK TypeScript check passes.

## Result

Implemented the single-tree model across backend, UI, CLI, MCP and SDKs. Removed
composition lifecycle/editor plumbing, explicitly retired v2 routes, preserved
independent tree IDs and access boundaries, and added publication/restore safety
compatible with current AFS. Migration remains explicit and offline; no user
database, catalog, configuration, reference checkout or installed service was
migrated or restarted.

Operator instructions: `docs/guides/single-tree-migration.md`.
Architecture decision: `docs/internals/decisions/0002-single-tree-workspaces.md`.

## Follow-up: topology presentation

Removed the obsolete shared Workspaces bounding box, duplicate heading and
count. Workspace nodes now render directly in their existing column, retaining
individual navigation, hover states, animation and connection refs. The host
grouping remains. Embedded UI build and all 71 UI tests passed; desktop and
narrow browser layouts were visually checked with no console errors. The
disposable preview at `http://127.0.0.1:18791/` was refreshed.
