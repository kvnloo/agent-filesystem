# One workspace owns one tree

Status: accepted
Date: 2026-09-17

## Context

The independent Redis tree and a separate composition were both exposed as
workspace-related resources. Users had to create a volume, attach it to an
Agent Workspace, and choose paths and attachment permissions before mounting.
Current AFS already operates directly on the independent tree.

## Decision

Use the existing independent tree as the sole Workspace resource. Keep its
storage ID, inode/content namespace, checkpoints, and catalog ownership.
`/v1/workspaces` is canonical. Retire the former v2 volume and composition
routes with HTTP 410, and remove composition lifecycle/editor/MCP routing code.
The CLI, SDKs and UI mount or operate on one workspace directly.

Retain database selection, authentication, deployment, search, history and
participating-client monitoring. Reuse existing UI components and tree lifecycle
features. Server checkpoint creation captures published Redis state, without
claiming to synchronize pending local changes on remote clients.

Writing requires the shared generation and inode revision protocol. Explicit
restore replaces roots and fences old clients. Reads never reconstruct a missing
live root. Legacy adoption is a separate reviewed offline command, not startup
behavior.

## Consequences

Existing tree IDs and data remain in place. Old composition records are retained
as migration evidence; they are not interpreted as trees or silently flattened.
Their credentials fail closed. Administrators review recorded ownership and
attachment restrictions before issuing one replacement key per tree.

CLI volume/attachment/bookmark workflows and SDK virtual multi-workspace roots
are removed. SDK filesystem mounts accept one workspace and root-relative paths.
Old clients must stop before adoption and cannot safely resume unfenced writes.
Current direct Redis clients do not supply this project's session heartbeats or
full file-version history. The SQL catalog and extended workspace metadata
remain necessary for this control plane's ownership and product features.

See [migration instructions](../../guides/single-tree-migration.md).
