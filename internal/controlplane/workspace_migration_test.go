package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestWorkspaceMigrationDryRunAndAdoption(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	for _, id := range []string{"a", "b"} {
		meta := WorkspaceMeta{Version: 1, ID: id, Name: id, HeadSavepoint: "cp1", Description: "keep metadata", Tags: []string{"keep"}}
		if err := setJSON(ctx, rdb, workspaceMetaKey(id), meta); err != nil {
			t.Fatal(err)
		}
		manifest := Manifest{Version: 1, Workspace: id, Savepoint: "cp1", Entries: map[string]ManifestEntry{"/": {Type: "dir", Mode: 0755}}}
		if err := SyncWorkspaceRoot(ctx, NewStore(rdb), id, manifest); err != nil {
			t.Fatal(err)
		}
		if err := rdb.Del(ctx, WorkspaceGenerationKey(id)).Err(); err != nil {
			t.Fatal(err)
		}
		// Sentinel fixtures model data that adoption must never rewrite.
		for key, value := range map[string]string{"content:2": "uncheckpointed live bytes", "checkpoint:cp1": "existing checkpoint", "inode:2:permissions": "0444"} {
			if err := rdb.Set(ctx, "afs:{"+id+"}:"+key, value, 0).Err(); err != nil {
				t.Fatal(err)
			}
		}
	}
	comps := []workspaceComposition{
		{ID: "one", Name: "single", OwnerSubject: "alice", Mounts: []workspaceCompositionMount{{VolumeID: "a", MountPath: "/repo", Readonly: true, VolumeTokenID: "restricted-token"}}},
		{ID: "many", Name: "a", OwnerSubject: "bob", Mounts: []workspaceCompositionMount{{VolumeID: "a", MountPath: "/read", Readonly: true}, {VolumeID: "b", MountPath: "/write"}}},
	}
	for _, comp := range comps {
		if err := setJSON(ctx, rdb, workspaceCompositionMetaKey(comp.ID), comp); err != nil {
			t.Fatal(err)
		}
	}
	if err := rdb.Set(ctx, "afs:{many}:workspace:composition:bookmark:before", `{"name":"before","volumes":[{"volume_id":"a","checkpoint_id":"cp1"}]}`, 0).Err(); err != nil {
		t.Fatal(err)
	}
	before := mr.Dump()
	report, err := InspectWorkspaceMigration(ctx, rdb, "db", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mr.Dump() != before {
		t.Fatal("dry run changed Redis")
	}
	if len(report.Workspaces) != 2 || len(report.Compositions) != 2 {
		t.Fatalf("report: %#v", report)
	}
	var found bool
	for _, issue := range report.Issues {
		if issue.Code == "composition_name_collision" {
			found = true
			if issue.Blocking {
				t.Fatal("retained composition name should not block independent IDs")
			}
		}
	}
	if !found {
		t.Fatal("missing collision report")
	}
	for _, comp := range report.Compositions {
		if comp.ID == "one" {
			a := comp.Attachments[0]
			if !a.Readonly || a.TokenID != "restricted-token" || a.PreviousMountPath != "/repo" || !a.Exists {
				t.Fatalf("lost grant: %#v", a)
			}
		}
	}
	if err := ApplyWorkspaceMigration(ctx, rdb, report, report, false); err == nil {
		t.Fatal("accepted adoption without stopped writers")
	}
	if mr.Dump() != before {
		t.Fatal("failed adoption mutated Redis")
	}
	if err := ApplyWorkspaceMigration(ctx, rdb, report, report, true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		generation, err := rdb.Get(ctx, "afs:{"+id+"}:generation").Result()
		if err != nil || !strings.HasPrefix(generation, "g_") {
			t.Fatalf("generation: %q %v", generation, err)
		}
		if got, _ := rdb.Get(ctx, "afs:{"+id+"}:content:2").Result(); got != "uncheckpointed live bytes" {
			t.Fatal("live content changed")
		}
		if got, _ := rdb.Get(ctx, "afs:{"+id+"}:checkpoint:cp1").Result(); got != "existing checkpoint" {
			t.Fatal("checkpoint changed")
		}
		if got, _ := rdb.Get(ctx, "afs:{"+id+"}:inode:2:permissions").Result(); got != "0444" {
			t.Fatal("permissions changed")
		}
	}
	for _, comp := range comps {
		raw, err := rdb.Get(ctx, workspaceCompositionMetaKey(comp.ID)).Bytes()
		if err != nil {
			t.Fatal(err)
		}
		expected, _ := json.Marshal(comp)
		if string(raw) != string(expected) {
			t.Fatal("composition changed")
		}
	}
	fresh, err := InspectWorkspaceMigration(ctx, rdb, "db", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyWorkspaceMigration(ctx, rdb, report, fresh, true); err == nil {
		t.Fatal("accepted stale reviewed inventory")
	}
}

func TestWorkspaceMigrationBlocksMissingAttachmentsAndTreeNameCollisions(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	for _, id := range []string{"a", "b"} {
		if err := setJSON(ctx, rdb, workspaceMetaKey(id), WorkspaceMeta{ID: id, Name: "duplicate"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := setJSON(ctx, rdb, workspaceCompositionMetaKey("c"), workspaceComposition{ID: "c", Name: "composed", Mounts: []workspaceCompositionMount{{VolumeID: "missing", Readonly: true}}}); err != nil {
		t.Fatal(err)
	}
	report, err := InspectWorkspaceMigration(ctx, rdb, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, issue := range report.Issues {
		codes[issue.Code] = issue.Blocking
	}
	if !codes["missing_attachment"] || !codes["tree_name_collision"] {
		t.Fatalf("issues: %#v", report.Issues)
	}
	before := mr.Dump()
	if err := ApplyWorkspaceMigration(ctx, rdb, report, report, true); err == nil {
		t.Fatal("accepted ambiguous migration")
	}
	if before != mr.Dump() {
		t.Fatal("blocked migration changed data")
	}
}

func TestWorkspaceMigrationPreservesMetadataWithExistingGeneration(t *testing.T) {
	for _, existingArchive := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing_archive", true: "merge_richer_archive"}[existingArchive], func(t *testing.T) {
			ctx, service, _, id := newStorageSafetyFixture(t)
			rdb := service.store.rdb
			generation, err := rdb.Get(ctx, WorkspaceGenerationKey(id)).Result()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := rdb.Get(ctx, workspaceMetaKey(id)).Bytes()
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if existingArchive {
				delete(fields, "database_id")
				delete(fields, "region")
			} else if err := rdb.Del(ctx, workspaceMetadataArchiveKey(workspaceMetaKey(id))).Err(); err != nil {
				t.Fatal(err)
			}
			fields["tenant_extension"] = json.RawMessage(`{"permission":"read-only"}`)
			raw, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := rdb.Set(ctx, workspaceMetaKey(id), raw, 0).Err(); err != nil {
				t.Fatal(err)
			}
			report, err := InspectWorkspaceMigration(ctx, rdb, "db", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := ApplyWorkspaceMigration(ctx, rdb, report, report, true); err != nil {
				t.Fatal(err)
			}
			if got, _ := rdb.Get(ctx, WorkspaceGenerationKey(id)).Result(); got != generation {
				t.Fatal("active generation changed")
			}
			if got, _ := rdb.Get(ctx, workspaceMetaKey(id)).Result(); got != string(raw) {
				t.Fatal("primary metadata changed")
			}
			// A subsequent current-client rewrite omits product metadata.
			for _, key := range []string{"database_id", "region", "tenant_extension"} {
				delete(fields, key)
			}
			slim, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := rdb.Set(ctx, workspaceMetaKey(id), slim, 0).Err(); err != nil {
				t.Fatal(err)
			}
			meta, err := service.store.GetWorkspaceMeta(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if meta.DatabaseID != "db-original" || meta.Region != "us-test" || string(meta.extraFields["tenant_extension"]) != `{"permission":"read-only"}` {
				t.Fatalf("metadata not retained: %+v", meta)
			}
		})
	}
}

func TestLegacyCompositionCredentialsFailClosed(t *testing.T) {
	manager, databaseID := newTestManager(t)
	ctx := context.Background()
	service, _, err := manager.serviceFor(ctx, databaseID)
	if err != nil {
		t.Fatal(err)
	}
	comp := workspaceComposition{ID: "old-composition", Name: "repo", Mounts: []workspaceCompositionMount{{VolumeID: "repo", MountPath: "/repo", Readonly: true}}}
	if err := setJSON(ctx, service.store.rdb, workspaceCompositionMetaKey(comp.ID), comp); err != nil {
		t.Fatal(err)
	}
	cli, err := manager.createWorkspaceCLIAccessTokenRecord(ctx, "", "", databaseID, comp.ID, comp.Name, createCLIAccessTokenRequest{Capability: cliCapabilityMountRW})
	if err != nil {
		t.Fatal(err)
	}
	mcp, err := manager.createMCPAccessTokenRecord(ctx, "", "", databaseID, comp.ID, comp.Name, createMCPAccessTokenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.AuthenticateCLIAccessToken(ctx, cli.Token); !errors.Is(err, ErrCLIAccessTokenInvalid) {
		t.Fatalf("composition CLI token accepted: %v", err)
	}
	if _, err = manager.AuthenticateMCPAccessToken(ctx, mcp.Token); !errors.Is(err, ErrMCPAccessTokenInvalid) {
		t.Fatalf("composition MCP token accepted: %v", err)
	}
	if _, err := service.store.rdb.Get(ctx, workspaceCompositionMetaKey(comp.ID)).Result(); err != nil {
		t.Fatal("retirement deleted composition")
	}
}

func TestWorkspaceMCPTokenCannotExpandScopeAndMountIsReadonly(t *testing.T) {
	manager, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := manager.CreateResolvedWorkspace(ctx, createWorkspaceRequest{Name: "other", Source: sourceRef{Kind: SourceBlank}}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateResolvedMCPAccessToken(ctx, "repo", createMCPAccessTokenRequest{Scope: "control-plane"}); err == nil {
		t.Fatal("workspace endpoint minted admin scope")
	}
	if _, err := manager.CreateResolvedMCPAccessToken(ctx, "repo", createMCPAccessTokenRequest{MountCapabilities: []mcpAccessTokenMountCapability{{VolumeID: "other", Capability: "rw"}}}); err == nil {
		t.Fatal("accepted per-attachment grants")
	}
	token, err := manager.CreateResolvedMCPAccessToken(ctx, "repo", createMCPAccessTokenRequest{Capability: MCPCapabilityRO})
	if err != nil {
		t.Fatal(err)
	}
	if token.Scope != workspaceScope(token.WorkspaceID) {
		t.Fatalf("scope %q", token.Scope)
	}
	server := httptest.NewServer(NewHandler(manager, "*"))
	defer server.Close()
	for _, test := range []struct {
		path   string
		status int
	}{{"/v1/client/workspaces/repo/sessions", http.StatusCreated}, {"/v1/client/workspaces/other/sessions", http.StatusNotFound}, {"/v1/account/developer/reset", http.StatusForbidden}} {
		req, _ := http.NewRequest(http.MethodPost, server.URL+test.path, strings.NewReader(`{"readonly":false}`))
		req.Header.Set("Authorization", "Bearer "+token.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != test.status {
			t.Fatalf("%s got %d, want %d", test.path, resp.StatusCode, test.status)
		}
		if test.status == http.StatusCreated {
			var session workspaceSession
			if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
				t.Fatal(err)
			}
			if !session.Readonly {
				t.Fatal("read-only MCP token issued writable mount")
			}
		}
		resp.Body.Close()
	}
}

func TestMigrationCatalogReadOnlyPreservesOwnersAndOmitsSecrets(t *testing.T) {
	manager, databaseID := newTestManager(t)
	ctx := context.Background()
	token, err := manager.createMCPAccessTokenRecord(ctx, "alice", "Alice", databaseID, "legacy-comp", "legacy", createMCPAccessTokenRequest{Capability: MCPCapabilityRO})
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.catalog.GetMCPAccessToken(ctx, token.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-migration catalog grant; the application never issues these.
	record.ID = "legacy-with-attachments"
	record.MountCapabilities = []mcpAccessTokenMountCapability{{VolumeID: "repo", Capability: MCPCapabilityRO}}
	if err := manager.catalog.CreateMCPAccessToken(ctx, record); err != nil {
		t.Fatal(err)
	}
	path := catalogStorePath(manager.configPathOverride)
	catalog, tokens, err := ReadWorkspaceMigrationCatalog(ctx, path, databaseID)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 1 || len(tokens) != 2 {
		t.Fatalf("catalog=%#v tokens=%#v", catalog, tokens)
	}
	for _, row := range tokens {
		if _, ok := row["secret"]; ok {
			t.Fatal("exported secret")
		}
		if _, ok := row["secret_hash"]; ok {
			t.Fatal("exported secret hash")
		}
		if row["owner_subject"] != "alice" {
			t.Fatal("owner missing")
		}
		if row["id"] == record.ID && !strings.Contains(row["mount_capabilities"].(string), `"capability":"ro"`) {
			t.Fatal("read-only attachment grant missing")
		}
	}
	after, err := manager.catalog.GetMCPAccessToken(ctx, token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastUsedAt != "" || after.RevokedAt != "" || after.SecretHash != record.SecretHash {
		t.Fatal("read-only catalog report changed credential")
	}
}

func TestWorkspaceTokenGrantsCheckedBeforeMutations(t *testing.T) {
	manager, databaseID := newTestManager(t)
	ctx := context.Background()
	if _, err := manager.CreateResolvedWorkspaceCLIAccessToken(ctx, "repo", createCLIAccessTokenRequest{Readonly: true, Capability: cliCapabilityMountRW}); err == nil {
		t.Fatal("contradictory readonly CLI grant accepted")
	}
	if _, err := manager.CreateResolvedMCPAccessToken(ctx, "repo", createMCPAccessTokenRequest{Profile: MCPProfileWorkspaceRW, Capability: MCPCapabilityRO}); err == nil {
		t.Fatal("contradictory MCP grants accepted")
	}
	service, _, err := manager.serviceFor(ctx, databaseID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := service.store.GetWorkspaceMeta(ctx, "repo")
	if err != nil {
		t.Fatal(err)
	}
	generation, err := service.WorkspaceGeneration(ctx, "repo")
	if err != nil {
		t.Fatal(err)
	}
	provider := &hostedMCPProvider{manager: manager, databaseID: databaseID, workspace: "repo", profile: MCPProfileWorkspaceRW}
	for _, test := range []struct {
		name string
		args map[string]any
	}{{"checkpoint_create", map[string]any{"checkpoint": "denied"}}, {"checkpoint_restore", map[string]any{"checkpoint": "initial"}}} {
		result := provider.callWorkspaceTool(ctx, test.name, test.args)
		if !result.IsError {
			t.Fatalf("%s was allowed", test.name)
		}
	}
	after, err := service.store.GetWorkspaceMeta(ctx, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if after.HeadSavepoint != before.HeadSavepoint {
		t.Fatal("denied call moved checkpoint head")
	}
	next, err := service.WorkspaceGeneration(ctx, "repo")
	if err != nil || next != generation {
		t.Fatal("denied restore changed generation")
	}
	if _, err := service.store.GetSavepointMeta(ctx, "repo", "denied"); err == nil {
		t.Fatal("denied call created checkpoint")
	}
}

func TestLegacyTokenScopeCannotOverrideBoundTree(t *testing.T) {
	manager, databaseID := newTestManager(t)
	ctx := context.Background()
	for _, scope := range []string{"workspace:other", "volume:other", "account", "control-plane"} {
		id, secret, err := newMCPAccessTokenParts()
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.catalog.CreateMCPAccessToken(ctx, mcpAccessTokenRecord{ID: id, DatabaseID: databaseID, WorkspaceID: "repo", WorkspaceName: "repo", Scope: scope, Capability: MCPCapabilityRW, Profile: MCPProfileWorkspaceRW, SecretHash: hashMCPAccessTokenSecret(secret)}); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.AuthenticateMCPAccessToken(ctx, formatMCPAccessToken(id, secret)); !errors.Is(err, ErrMCPAccessTokenInvalid) {
			t.Fatalf("accepted old mismatched scope %q: %v", scope, err)
		}
	}
	id, secret, err := newCLIAccessTokenParts()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.catalog.CreateCLIAccessToken(ctx, cliAccessTokenRecord{ID: id, DatabaseID: databaseID, WorkspaceID: "repo", Scope: "workspace:other", Capability: cliCapabilityMountRW, SecretHash: hashCLIAccessTokenSecret(secret)}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AuthenticateCLIAccessToken(ctx, formatCLIAccessToken(id, secret)); !errors.Is(err, ErrCLIAccessTokenInvalid) {
		t.Fatalf("accepted mismatched CLI scope: %v", err)
	}
}
