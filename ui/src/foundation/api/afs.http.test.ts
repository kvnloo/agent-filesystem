import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

describe("one-tree HTTP workspace contract", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.stubEnv("VITE_AFS_CLIENT_MODE", "http");
    vi.stubEnv("VITE_AFS_API_BASE_URL", "https://test.afs.invalid");
  });
  afterEach(() => { vi.unstubAllGlobals(); vi.unstubAllEnvs(); });

  test("checkpoints published Redis state without posting a replacement manifest", async () => {
    const fetch = vi.fn().mockImplementation(async (_url: string, init?: RequestInit) =>
      init?.method === "POST" ? new Response(null, { status: 204 }) : new Response("not found", { status: 404 }),
    );
    vi.stubGlobal("fetch", fetch);
    const { afsApi } = await import("./afs");
    await afsApi.createSavepoint({ databaseId: "db-a", workspaceId: "tree-123", name: "before-review", note: "Published state" });
    const [url, init] = fetch.mock.calls[0];
    expect(url).toBe("https://test.afs.invalid/v1/databases/db-a/workspaces/tree-123:save-from-live");
    expect(JSON.parse(init.body)).toEqual({ checkpoint_id: "before-review", description: "Published state", source: "web", allow_unchanged: true });
    expect(fetch.mock.calls.every(([path]) => !path.includes("/v2/"))).toBe(true);
  });

  test("forks the selected tree in its database", async () => {
    const fetch = vi.fn().mockImplementation(async (_url: string, init?: RequestInit) =>
      init?.method === "POST" ? new Response(null, { status: 204 }) : new Response("not found", { status: 404 }),
    );
    vi.stubGlobal("fetch", fetch);
    const { afsApi } = await import("./afs");
    await afsApi.forkWorkspace({ databaseId: "db-a", workspaceId: "tree-123", name: "experiment" });
    expect(fetch.mock.calls[0][0]).toBe("https://test.afs.invalid/v1/databases/db-a/workspaces/tree-123:fork");
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ new_workspace: "experiment" });
  });

  test("issues read-only keys against a tree, preserving scope and capability", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "key-1", database_id: "db-a", workspace_id: "tree-123", scope: "workspace:tree-123", profile: "workspace-ro", capability: "ro", readonly: true, created_at: "2026-09-17T00:00:00Z" }), { status: 201 }));
    vi.stubGlobal("fetch", fetch);
    const { afsApi } = await import("./afs");
    const token = await afsApi.createMCPAccessToken({ databaseId: "db-a", workspaceId: "tree-123", name: "reader", profile: "workspace-ro", capability: "ro" });
    expect(fetch.mock.calls[0][0]).toBe("https://test.afs.invalid/v1/databases/db-a/workspaces/tree-123/mcp-tokens");
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toMatchObject({ name: "reader", profile: "workspace-ro", capability: "ro" });
    expect(token).toMatchObject({ workspaceId: "tree-123", readonly: true, capability: "ro" });
  });
});
