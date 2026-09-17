# Migrating to single-tree workspaces

A workspace is the existing independent Redis tree, formerly called a volume.
Its ID, file contents, live changes, checkpoints, and catalog ownership remain
in place. No tree contents are copied. `/v1/workspaces` is the canonical HTTP
contract. The former `/v2/workspaces` composition routes and `/v2/volumes`
aliases return `410 Gone`; composition IDs are never redirected to tree IDs.

The Workspaces UI retains browsing, content viewing, search, History,
checkpoints, comparison, restore, fork, settings, database selection, and
access tokens. `afs ws mount <workspace> <directory>` connects one workspace
to one local directory. SDK filesystem mounts likewise take one workspace.

## Offline migration

Migration is a separate executable. Normal startup does not adopt old trees
or delete composition records. Build it into a temporary or chosen binary path:

```sh
go build -o /tmp/afs-migrate-workspaces ./cmd/afs-migrate-workspaces
```

Stop old and current writers, including sync daemons, live mounts, and servers.
Back up Redis and its SQL catalog together. Run the dry run against a disposable
copy first. Supply the exact Redis database, existing SQLite catalog, and catalog
database ID; the tool never reads saved CLI configuration or environment catalog
settings:

```sh
/tmp/afs-migrate-workspaces \
  --redis-url redis://127.0.0.1:16391/0 \
  --catalog /tmp/migration-copy/afs.catalog.sqlite \
  --database-id example-database > /tmp/workspace-migration.json
```

This command reads only. The catalog is opened with SQLite `mode=ro`, without
schema migration. The report contains original tree and composition metadata,
attachment paths and read-only restrictions, bookmark checkpoint references,
catalog ownership, and token IDs/grants. It omits credential secrets and hashes.
Protect the report as administrative metadata.

Review the report before applying:

- Keep each existing tree as an independent workspace, with its original ID and
  name. A composition with several attachments becomes several independent
  workspace connections; it does not become a concatenated tree.
- Resolve missing/incomplete roots, dangling attachments, duplicate tree names,
  inactive generations, and resource ID collisions. These block adoption.
- A composition name matching a tree is reported but never becomes an alias.
- Composition records and bookmarks remain stored verbatim. Retaining them is
  their explicit disposition; the migration executable never deletes them.
- Composition credentials fail closed. Reissue one key per independent workspace
  only after reviewing its owner and the former attachment grants. A read-only
  attachment must receive a read-only key. The tool does not automatically grant
  a composition owner ownership of an attached tree or convert old credentials.

Apply the reviewed report while all writers remain stopped:

```sh
/tmp/afs-migrate-workspaces \
  --redis-url redis://127.0.0.1:16391/0 \
  --catalog /tmp/migration-copy/afs.catalog.sqlite \
  --database-id example-database \
  --apply-report /tmp/workspace-migration.json --writers-stopped
```

For a Redis-only installation that has never used a SQL catalog, explicitly use
`--no-catalog` instead of `--catalog` and `--database-id` in both commands. This
mode cannot report SQL ownership or token grants; do not use it for an
installation that has a catalog.

The tool re-reads the inventory and rejects a stale report. Its fingerprint
covers metadata, relationships and catalog grants, not a checksum of every file;
the stopped-writer requirement and independent backups still apply. Adoption initializes
missing generation markers and preserves extended metadata in a compatibility
record, including trees that already have an active generation. Existing
compatibility fields survive when the live metadata comes from a smaller client
schema. It does not modify the catalog, primary metadata, file contents, modes,
checkpoints, IDs, or composition records. Existing active generations are retained. A failed
multi-workspace adoption can leave earlier trees adopted; inspect again and
review a fresh report before retrying. There is no cross-workspace transaction.

The executable currently supports SQLite catalog inspection and explicit Redis-only adoption. PostgreSQL
operators must export and review the equivalent ownership and credential
inventory separately; do not point this executable at a production PostgreSQL
catalog or use `--no-catalog` to bypass ownership review.

## Shared storage with current AFS

New workspaces use the same `afs:` namespace and generation protocol as current
AFS. Updated writers stage content and validate generation, inode revision, and
parent linkage before publication. Restore fences the old generation before
replacing a live root. Existing mounts must reconnect or remount after a restore; their previous generation is deliberately rejected. Legacy trees without a generation remain readable;
writing requires explicit adoption. A missing live root is reported instead of
being silently materialized by browsing.

These protections require the updated server/client. An old binary that ignores
publication checks can still corrupt shared state, so stop old writers before
adoption. Do not reconnect old clients afterward.

The smaller metadata schema in current AFS omits this project's database, cloud,
region, source and tag fields. The updated control plane retains those fields
in a compatibility record and merges them when reading metadata written by the
current client. Preserve the SQL catalog: it contains ownership, routing,
credentials, and sessions that cannot be reconstructed from Redis metadata.

Server-side checkpoints capture **published Redis state**. They do not flush
unsynchronized local files on every connected machine. Save or flush pending
local changes using the owning client before taking a checkpoint when those
changes must be included.

Current direct Redis clients do not emit this control plane's session heartbeats
or its richer file-version history hooks. The Monitor and History UI
remain useful for participating clients, but an empty active-agent list is not
proof that no direct Redis client is connected. Keyword query indexing catches
up from the durable mutation journal, and exact grep scans published files.
Semantic embeddings for changed content require the explicit
`query index create --embeddings --wait` operation; queries do not backfill
embeddings. Native Array storage still requires a compatible Redis server.
