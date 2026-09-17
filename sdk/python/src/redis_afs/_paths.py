"""Remote path normalization and workspace path resolution."""

from __future__ import annotations

import posixpath

from .errors import AFSError


def normalize_remote_path(path: str) -> str:
    raw = path.strip()
    if not raw:
        return "/"
    parts = [part for part in raw.split("/") if part]
    if ".." in parts:
        raise AFSError(f"path {path} must not contain '..'")
    normalized = posixpath.normpath(raw if raw.startswith("/") else f"/{raw}")
    return "/" if normalized == "." else normalized
