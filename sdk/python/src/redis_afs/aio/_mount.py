"""Async mounted filesystem primitives: _AsyncMountedWorkspace, AsyncMountedFS, AsyncBashRunner."""

from __future__ import annotations

import asyncio
import os
import shutil
import tempfile
from collections.abc import Mapping, MutableMapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .._paths import normalize_remote_path as _normalize_remote_path
from ..errors import AFSError
from ..models import BashResult
from ._sync import _TreeSync


@dataclass(frozen=True)
class _AsyncMountedWorkspace:
    name: str
    token: str
    client: Any


class AsyncMountedFS:
    def __init__(
        self,
        workspace: _AsyncMountedWorkspace,
        *,
        mode: str = "rw",
        concurrency: int = 16,
    ) -> None:
        if not isinstance(workspace, _AsyncMountedWorkspace):
            raise AFSError("mount requires one workspace")
        self._workspace = workspace
        self.mode = mode
        self._local_root: tempfile.TemporaryDirectory[str] | None = None
        self._concurrency = concurrency
        self._semaphore = asyncio.Semaphore(concurrency)

    @property
    def workspace_name(self) -> str:
        return self._workspace.name

    @property
    def local_root(self) -> str | None:
        return self._local_root.name if self._local_root else None

    async def read_file(self, path: str) -> str:
        workspace, remote_path = self._resolve(path)
        response = await workspace.client.call_tool("file_read", {"path": remote_path})
        if response.get("binary"):
            raise AFSError(f"file {remote_path} is binary and cannot be returned as text")
        if response.get("kind") == "dir":
            raise AFSError(f"path {remote_path} is a directory")
        return str(response.get("content", ""))

    async def write_file(self, path: str, content: str | bytes) -> None:
        workspace, remote_path = self._resolve(path)
        text = content.decode("utf-8") if isinstance(content, bytes) else content
        await workspace.client.call_tool("file_write", {"path": remote_path, "content": text})
        if self.local_root:
            local_path = self._local_path_for(remote_path)
            local_path.parent.mkdir(parents=True, exist_ok=True)
            local_path.write_text(text, encoding="utf-8")

    async def list_files(self, path: str = "/", depth: int = 1) -> list[dict[str, Any]]:
        workspace, remote_path = self._resolve(path)
        response = await workspace.client.call_tool("file_list", {"path": remote_path, "depth": depth})
        return list(response.get("entries", []))

    async def glob(
        self,
        pattern: str,
        *,
        path: str = "/",
        kind: str | None = None,
        limit: int | None = None,
    ) -> dict[str, Any]:
        workspace, remote_path = self._resolve(path)
        return await workspace.client.call_tool(
            "file_glob",
            {"path": remote_path, "pattern": pattern, "kind": kind, "limit": limit},
        )

    async def grep(self, pattern: str, **options: Any) -> dict[str, Any]:
        workspace, remote_path = self._resolve(str(options.pop("path", "/")))
        return await workspace.client.call_tool("file_grep", {"path": remote_path, "pattern": pattern, **options})

    async def delete(self, path: str) -> dict[str, Any]:
        workspace, remote_path = self._resolve(path)
        response = await workspace.client.call_tool("file_delete", {"path": remote_path})
        if self.local_root:
            local_path = self._local_path_for(remote_path)
            if local_path.is_dir() and not local_path.is_symlink():
                shutil.rmtree(local_path, ignore_errors=True)
            else:
                try:
                    local_path.unlink()
                except FileNotFoundError:
                    pass
        return response

    async def checkpoint(self, name: str | None = None) -> dict[str, Any]:
        return await self._workspace.client.call_tool("checkpoint_create", {"checkpoint": name})

    def bash(self) -> AsyncBashRunner:
        return AsyncBashRunner(self)

    async def sync_from_remote(self) -> str:
        root = Path(self._ensure_local_root())
        shutil.rmtree(root, ignore_errors=True)
        root.mkdir(parents=True, exist_ok=True)
        await _TreeSync(self._workspace.client, self._semaphore).pull("/", root)
        return str(root)

    async def sync_to_remote(self) -> None:
        if self.local_root:
            await _TreeSync(self._workspace.client, self._semaphore).push(Path(self.local_root), "/")

    async def aclose(self) -> None:
        try:
            await self._workspace.client.aclose()
        finally:
            if self._local_root:
                self._local_root.cleanup()
                self._local_root = None

    async def __aenter__(self) -> AsyncMountedFS:
        return self

    async def __aexit__(self, exc_type: object, exc: object, tb: object) -> None:
        await self.aclose()

    def _resolve(self, raw_path: str) -> tuple[_AsyncMountedWorkspace, str]:
        return self._workspace, _normalize_remote_path(raw_path)

    def _ensure_local_root(self) -> str:
        if not self._local_root:
            self._local_root = tempfile.TemporaryDirectory(prefix="afs-fs-")
        return self._local_root.name

    def _local_path_for(self, remote_path: str) -> Path:
        if not self.local_root:
            raise AFSError("mount has not been materialized locally yet")
        relative = _normalize_remote_path(remote_path).lstrip("/")
        return Path(self.local_root, relative)


class AsyncBashRunner:
    def __init__(self, mounted_fs: AsyncMountedFS) -> None:
        self._fs = mounted_fs

    async def exec(
        self,
        command: str,
        *,
        cwd: str | None = None,
        env: Mapping[str, str | None] | None = None,
        timeout: float | None = None,
        check: bool = False,
    ) -> BashResult:
        # On timeout this raises asyncio.TimeoutError (an alias of builtin TimeoutError
        # on Python 3.11+) after killing and reaping the spawned child. This is the
        # async API's contract; the sync client raises subprocess.TimeoutExpired, but we
        # intentionally surface the asyncio-native exception here.
        root = await self._fs.sync_from_remote()
        mapped_command = command
        run_env: MutableMapping[str, str] = dict(os.environ)
        if env:
            for key, value in env.items():
                if value is None:
                    run_env.pop(key, None)
                else:
                    run_env[key] = value
        proc = await asyncio.create_subprocess_exec(
            "/bin/bash",
            "-c",
            mapped_command,
            cwd=str(Path(root, cwd)) if cwd else root,
            env=run_env,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        if timeout is not None:
            try:
                stdout_b, stderr_b = await asyncio.wait_for(proc.communicate(), timeout)
            except asyncio.TimeoutError:
                proc.kill()
                await proc.communicate()  # reap the child and drain pipes
                raise
        else:
            stdout_b, stderr_b = await proc.communicate()
        await self._fs.sync_to_remote()
        result = BashResult(
            stdout=stdout_b.decode("utf-8"),
            stderr=stderr_b.decode("utf-8"),
            exit_code=proc.returncode,
            command=command,
            mapped_command=mapped_command,
        )
        if check and result.exit_code != 0:
            raise AFSError(f"command exited with status {result.exit_code}", payload=result)
        return result
