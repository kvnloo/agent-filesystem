# AFS SDKs

This directory contains first-pass agent SDKs for the AFS control plane:

- `typescript/` publishes the `redis-afs` package.
- `python/` publishes the `redis-afs` package with the `redis_afs` import.

Both SDKs use the hosted MCP endpoint as the stable agent-facing transport:

- control-plane tokens call workspace lifecycle tools such as `workspace_create`
  and `mcp_token_issue`;
- mounted filesystem objects use workspace-scoped MCP tokens for file reads,
  writes, searches, and checkpoints.

## Authentication

Set `AFS_API_KEY` in the process environment or pass the key directly to the
client constructor. Set `AFS_API_BASE_URL` only when targeting a local or
Self-managed control plane; otherwise the SDKs use `https://afs.cloud`.

## TypeScript

Full API reference: [`typescript/api-docs.md`](typescript/api-docs.md).

```bash
npm install redis-afs
```

```ts
import { AFS } from "redis-afs";

const afs = new AFS({ apiKey: process.env.AFS_API_KEY });
const workspace = await afs.workspace.create({ name: "foobar" });

const fs = await afs.fs.mount({
  workspace: workspace.name,
  mode: "rw",
});

await fs.writeFile("/src/README.md", "hello world");
const result = await fs.bash().exec("cat src/README.md");
console.log(result.stdout);
await fs.close();
```

## Python

Full API reference: [`python/api-docs.md`](python/api-docs.md).

```bash
pip install redis-afs
```

```python
import os
from redis_afs import AFS

afs = AFS(api_key=os.environ["AFS_API_KEY"])
workspace = afs.workspace.create(name="foobar")

fs = afs.fs.mount(
    workspace=workspace["name"],
    mode="rw",
)

fs.write_file("/src/README.md", "hello world")
result = fs.bash().exec("cat src/README.md")
print(result.stdout)
fs.close()
```

## Mount Semantics

`fs.mount()` opens one workspace through MCP. File API paths such as
`/path/to/file` resolve directly within that workspace. It is not a kernel
FUSE/NFS mount.

`bash().exec()` materializes the workspace tree into one temporary directory,
runs the shell command there, then writes created and modified files back
through MCP. Use relative shell paths such as `src/README.md`. The current alpha sync path supports file create/update;
use `delete()` for explicit remote deletion. Local deletion is not automatically
propagated by the sync helper.

## Test Locally

```bash
npm --prefix sdk/typescript run check
npm --prefix sdk/typescript test
```

```bash
PYTHONPATH=sdk/python/src python3 -m unittest discover -s sdk/python/tests
```

## Publish

Those install commands work from a clean machine only after publishing the
packages to their registries:

```bash
cd sdk/typescript
npm publish --access public
```

```bash
cd sdk/python
python3 -m build
python3 -m twine upload dist/*
```

Before publishing, install from the local checkout:

```bash
npm install ./sdk/typescript
python3 -m pip install ./sdk/python
```
