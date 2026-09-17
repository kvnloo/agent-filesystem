package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	afsclient "github.com/redis/agent-filesystem/mount/client"
	"github.com/redis/go-redis/v9"
)

func newStorageSafetyFixture(t *testing.T) (context.Context, *Service, *miniredis.Miniredis, string) {
	t.Helper()
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	service := NewService(Config{}, NewStore(rdb))
	detail, err := service.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "safety", Description: "preserve me", DatabaseID: "db-original", Region: "us-test"})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, service, mr, detail.ID
}

func TestStorageLegacyReadsDoNotHydrateOrMigrate(t *testing.T) {
	ctx, s, mr, id := newStorageSafetyFixture(t)
	c := afsclient.New(s.store.rdb, id)
	if err := c.Echo(ctx, "/note", []byte("inline legacy")); err != nil {
		t.Fatal(err)
	}
	// Remove the protocol marker to model a pre-adoption tree.
	if err := s.store.rdb.Del(ctx, WorkspaceGenerationKey(id)).Err(); err != nil {
		t.Fatal(err)
	}
	before := mr.Dump()
	if _, err := s.GetTree(ctx, id, "working-copy", "/", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFileContent(ctx, id, "working-copy", "/note"); err != nil {
		t.Fatal(err)
	}
	if before != mr.Dump() {
		t.Fatal("legacy read changed Redis")
	}
	if err := afsclient.New(s.store.rdb, id).Echo(ctx, "/note", []byte("forbidden")); !errors.Is(err, afsclient.ErrGenerationRequired) {
		t.Fatalf("unadopted write: %v", err)
	}
	if before != mr.Dump() {
		t.Fatal("rejected legacy write changed Redis")
	}
	if err := s.store.rdb.Del(ctx, workspaceFSInodeKey(id, "1"), workspaceFSInfoKey(id)).Err(); err != nil {
		t.Fatal(err)
	}
	before = mr.Dump()
	if _, err := s.GetTree(ctx, id, "working-copy", "/", 2); err == nil {
		t.Fatal("missing root read succeeded")
	}
	if _, err := s.GetFileContent(ctx, id, "working-copy", "/note"); err == nil {
		t.Fatal("missing content read succeeded")
	}
	_, _ = s.GetWorkspace(ctx, id)
	if before != mr.Dump() {
		t.Fatal("read reconstructed missing root")
	}
}

func TestStorageAdoptionPreservesRawAndSlimMetadata(t *testing.T) {
	ctx, s, _, id := newStorageSafetyFixture(t)
	rdb := s.store.rdb
	if err := rdb.Del(ctx, WorkspaceGenerationKey(id)).Err(); err != nil {
		t.Fatal(err)
	}
	data, err := rdb.Get(ctx, workspaceMetaKey(id)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["tenant_extension"] = json.RawMessage(`{"owner":"original","permission":"read-only"}`)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, workspaceMetaKey(id), raw, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.AdoptWorkspaceGeneration(ctx, id); err != nil {
		t.Fatal(err)
	}
	after, err := rdb.Get(ctx, workspaceMetaKey(id)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("adoption rewrote original metadata")
	}
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
	meta, err := s.store.GetWorkspaceMeta(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.DatabaseID != "db-original" || meta.Region != "us-test" || string(meta.extraFields["tenant_extension"]) != `{"owner":"original","permission":"read-only"}` {
		t.Fatalf("lost retained metadata: %+v", meta)
	}
	if err := s.store.PutWorkspaceMeta(ctx, meta); err != nil {
		t.Fatal(err)
	}
	got, err := rdb.Get(ctx, workspaceMetaKey(id)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var rewritten map[string]json.RawMessage
	if err := json.Unmarshal(got, &rewritten); err != nil {
		t.Fatal(err)
	}
	if string(rewritten["tenant_extension"]) != `{"owner":"original","permission":"read-only"}` {
		t.Fatal("write dropped unknown metadata")
	}
}

func TestStorageRestoreAndDeleteFenceExistingClients(t *testing.T) {
	ctx, s, _, id := newStorageSafetyFixture(t)
	old := afsclient.New(s.store.rdb, id)
	if err := old.Echo(ctx, "/note", []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	if err := old.Chmod(ctx, "/note", 0600); err != nil {
		t.Fatal(err)
	}
	if saved, err := s.SaveCheckpointFromLive(ctx, id, "saved"); err != nil || !saved {
		t.Fatalf("save: %v %v", saved, err)
	}
	if err := old.Echo(ctx, "/note", []byte("published live")); err != nil {
		t.Fatal(err)
	}
	result, err := s.RestoreCheckpointWithResult(ctx, id, "saved")
	if err != nil {
		t.Fatal(err)
	}
	if !result.SafetyCheckpointCreated {
		t.Fatal("restore omitted safety checkpoint")
	}
	safety, err := s.store.GetManifest(ctx, id, result.SafetyCheckpointID)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := base64.StdEncoding.DecodeString(safety.Entries["/note"].Inline)
	if err != nil || string(bytes) != "published live" {
		t.Fatalf("safety content=%q %v", bytes, err)
	}
	if err := old.Echo(ctx, "/note", []byte("stale")); !errors.Is(err, afsclient.ErrWorkspaceChanged) {
		t.Fatalf("old client accepted restore: %v", err)
	}
	current := afsclient.New(s.store.rdb, id)
	bytes, err = current.Cat(ctx, "/note")
	if err != nil || string(bytes) != "checkpoint" {
		t.Fatalf("restored bytes=%q %v", bytes, err)
	}
	st, err := current.Stat(ctx, "/note")
	if err != nil || st.Mode != 0600 {
		t.Fatalf("restored permissions: %+v %v", st, err)
	}
	if err := s.DeleteWorkspace(ctx, id); err != nil {
		t.Fatal(err)
	}
	generation, err := s.store.rdb.Get(ctx, WorkspaceGenerationKey(id)).Result()
	if err != nil || generation != "deleted" {
		t.Fatalf("missing deletion tombstone=%q %v", generation, err)
	}
	if err := current.Echo(ctx, "/note", []byte("resurrect")); !errors.Is(err, afsclient.ErrWorkspaceChanged) {
		t.Fatalf("deleted client wrote: %v", err)
	}
	if err := afsclient.New(s.store.rdb, id).Echo(ctx, "/note", []byte("resurrect")); !errors.Is(err, afsclient.ErrWorkspaceChanged) {
		t.Fatalf("new client bypassed tombstone: %v", err)
	}
}

func TestStorageCheckpointSnapshotDoesNotReplacePublishedTree(t *testing.T) {
	ctx, s, _, id := newStorageSafetyFixture(t)
	current := afsclient.New(s.store.rdb, id)
	if err := current.Echo(ctx, "/live", []byte("peer changes")); err != nil {
		t.Fatal(err)
	}
	m, err := s.store.GetManifest(ctx, id, "initial")
	if err != nil {
		t.Fatal(err)
	}
	m.Savepoint = "other-snapshot"
	if saved, err := s.SaveCheckpoint(ctx, SaveCheckpointRequest{Workspace: id, ExpectedHead: "initial", CheckpointID: m.Savepoint, Manifest: m, AllowUnchanged: true}); err != nil || !saved {
		t.Fatalf("save %v %v", saved, err)
	}
	got, err := current.Cat(ctx, "/live")
	if err != nil || string(got) != "peer changes" {
		t.Fatalf("checkpoint lost live bytes=%q %v", got, err)
	}
	dirty, _, err := WorkspaceRootDirtyState(ctx, s.store, id)
	if err != nil || !dirty {
		t.Fatalf("snapshot incorrectly acknowledged live tree: %v %v", dirty, err)
	}
}

func TestStorageMetadataAndNamespaceMutationsDirtyAfterCheckpoint(t *testing.T) {
	for _, operation := range []string{"chmod", "chown", "mkdir", "symlink", "rename"} {
		t.Run(operation, func(t *testing.T) {
			ctx, s, _, id := newStorageSafetyFixture(t)
			c := afsclient.New(s.store.rdb, id)
			if err := c.Echo(ctx, "/note", []byte("bytes")); err != nil {
				t.Fatal(err)
			}
			if err := c.Chmod(ctx, "/note", 0600); err != nil {
				t.Fatal(err)
			}
			if saved, err := s.SaveCheckpointFromLive(ctx, id, "before"); err != nil || !saved {
				t.Fatalf("save before=%v %v", saved, err)
			}
			var err error
			switch operation {
			case "chmod":
				err = c.Chmod(ctx, "/note", 0640)
			case "chown":
				err = c.Chown(ctx, "/note", 123, 456)
			case "mkdir":
				err = c.Mkdir(ctx, "/directory")
			case "symlink":
				err = c.Ln(ctx, "/note", "/link")
			case "rename":
				err = c.Mv(ctx, "/note", "/renamed")
			}
			if err != nil {
				t.Fatal(err)
			}
			dirty, _, err := WorkspaceRootDirtyState(ctx, s.store, id)
			if err != nil || !dirty {
				t.Fatalf("%s after checkpoint lost dirty marker: %v %v", operation, dirty, err)
			}
			// Chown is published metadata but not represented in version-1 manifests.
			if operation != "chown" {
				if saved, err := s.SaveCheckpointFromLive(ctx, id, "after"); err != nil || !saved {
					t.Fatalf("second save=%v %v", saved, err)
				}
			}
		})
	}
}

func TestStorageUploadedCheckpointDirtiesCleanTreeAndFencesOldClean(t *testing.T) {
	ctx, s, _, id := newStorageSafetyFixture(t)
	generation, err := s.WorkspaceGeneration(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := latestWorkspaceChange(ctx, s.store.rdb, id)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.store.GetManifest(ctx, id, "initial")
	if err != nil {
		t.Fatal(err)
	}
	m.Savepoint = "uploaded"
	m.Entries["/uploaded-only"] = ManifestEntry{Type: "file", Mode: 0600, Inline: base64.StdEncoding.EncodeToString([]byte("snapshot only")), Size: 13}
	if saved, err := s.SaveCheckpoint(ctx, SaveCheckpointRequest{Workspace: id, ExpectedHead: "initial", CheckpointID: "uploaded", Manifest: m}); err != nil || !saved {
		t.Fatalf("uploaded save=%v %v", saved, err)
	}
	dirty, _, err := WorkspaceRootDirtyState(ctx, s.store, id)
	if err != nil || !dirty {
		t.Fatalf("uploaded head left live root clean=%v %v", dirty, err)
	}
	if err := markCapturedWorkspaceClean(ctx, s.store, id, "initial", generation, changes); !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("old capture cleaned newer head: %v", err)
	}
	if saved, err := s.SaveCheckpointFromLive(ctx, id, "published"); err != nil || !saved {
		t.Fatalf("published root capture=%v %v", saved, err)
	}
}

func TestStorageDeletionStopsAfterImportLeaseReplacement(t *testing.T) {
	ctx, s, _, id := newStorageSafetyFixture(t)
	c := afsclient.New(s.store.rdb, id)
	if err := c.Echo(ctx, "/note", []byte("retained")); err != nil {
		t.Fatal(err)
	}
	peer := redis.NewClient(s.store.rdb.Options())
	defer peer.Close()
	var once sync.Once
	s.store.rdb.AddHook(&storageCommandHook{after: func(cmd redis.Cmder) {
		if cmd.Name() == "scan" {
			once.Do(func() {
				if err := peer.Set(ctx, ImportLockKey(id), "replacement-owner", time.Minute).Err(); err != nil {
					t.Fatal(err)
				}
				if err := peer.Set(ctx, WorkspaceGenerationKey(id), "g_replacement", 0).Err(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}})
	if err := s.DeleteWorkspace(ctx, id); err == nil {
		t.Fatal("stale deletion succeeded after lease replacement")
	}
	got, err := afsclient.New(peer, id).Cat(ctx, "/note")
	if err != nil || string(got) != "retained" {
		t.Fatalf("stale deletion removed newer owner's tree: %q %v", got, err)
	}
	if _, err := s.store.GetWorkspaceMeta(ctx, id); err != nil {
		t.Fatalf("stale deletion removed metadata: %v", err)
	}
	if token, err := peer.Get(ctx, ImportLockKey(id)).Result(); err != nil || token != "replacement-owner" {
		t.Fatalf("stale release removed newer lock=%q %v", token, err)
	}
}

func TestStorageCheckpointDetectsMutationWithPubSubDisabled(t *testing.T) {
	ctx, s, _, id := newStorageSafetyFixture(t)
	c := afsclient.New(s.store.rdb, id)
	c.DisableInvalidationPublishing()
	if err := c.Echo(ctx, "/note", []byte("first")); err != nil {
		t.Fatal(err)
	}
	peerRedis := redis.NewClient(s.store.rdb.Options())
	defer peerRedis.Close()
	peer := afsclient.New(peerRedis, id)
	peer.DisableInvalidationPublishing()
	var once sync.Once
	s.store.rdb.AddHook(&storageCommandHook{after: func(cmd redis.Cmder) {
		if cmd.Name() == "get" && len(cmd.Args()) > 1 && strings.Contains(fmt.Sprint(cmd.Args()[1]), ":content:") {
			once.Do(func() {
				if err := peer.Echo(ctx, "/note", []byte("newer")); err != nil {
					t.Fatal(err)
				}
			})
		}
	}})
	if saved, err := s.SaveCheckpointFromLive(ctx, id, "racing"); saved || !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("captured concurrent publication: %v %v", saved, err)
	}
	meta, err := s.store.GetWorkspaceMeta(ctx, id)
	if err != nil || meta.HeadSavepoint != "initial" {
		t.Fatalf("conflicted capture moved head: %+v %v", meta, err)
	}
	dirty, _, err := WorkspaceRootDirtyState(ctx, s.store, id)
	if err != nil || !dirty {
		t.Fatalf("conflicted capture cleared dirty: %v %v", dirty, err)
	}
}

type storageCommandHook struct{ after func(redis.Cmder) }

func (h *storageCommandHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *storageCommandHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error { err := next(ctx, cmd); h.after(cmd); return err }
}
func (h *storageCommandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestStorageSettingsUpdatePreservesConcurrentCheckpoint(t *testing.T) {
	ctx, s, _, id := newStorageSafetyFixture(t)
	peerDB := redis.NewClient(s.store.rdb.Options())
	t.Cleanup(func() { _ = peerDB.Close() })
	peer := NewService(s.cfg, NewStore(peerDB))
	if err := afsclient.New(peerDB, id).Echo(ctx, "/note", []byte("checkpoint me")); err != nil {
		t.Fatal(err)
	}
	var armed, fired bool
	s.store.rdb.AddHook(&storageCommandHook{after: func(cmd redis.Cmder) {
		if cmd.Name() == "watch" {
			armed = true
		}
		if !armed || fired || cmd.Name() != "get" || cmd.Args()[1] != workspaceMetaKey(id) {
			return
		}
		fired = true
		if saved, err := peer.SaveCheckpointFromLive(ctx, id, "peer-checkpoint"); err != nil || !saved {
			t.Fatalf("peer checkpoint: %v %v", saved, err)
		}
	}})
	detail, err := s.UpdateWorkspace(ctx, id, UpdateWorkspaceRequest{Name: "renamed", Description: "new settings", DatabaseName: "display-db", CloudAccount: "Direct Redis", Region: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("checkpoint race was not injected")
	}
	meta, err := s.store.GetWorkspaceMeta(ctx, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != id || detail.ID != id || meta.HeadSavepoint != "peer-checkpoint" || meta.DirtyHint || meta.Description != "new settings" || meta.DatabaseID != "db-original" {
		t.Fatalf("settings overwrote concurrent state: %+v", meta)
	}
	if _, err := s.store.GetWorkspaceMeta(ctx, "safety"); err == nil {
		t.Fatal("old name index retained")
	}
}

func TestStorageSettingsRenamePreservesLegacyStorageID(t *testing.T) {
	ctx, s, mr, _ := newStorageSafetyFixture(t)
	const id = "legacy-name"
	meta := WorkspaceMeta{Name: id, HeadSavepoint: "initial", CreatedAt: time.Now().UTC()}
	if err := s.store.PutWorkspaceMeta(ctx, meta); err != nil {
		t.Fatal(err)
	}
	mr.Set(WorkspaceGenerationKey(id), "g_legacy")
	updated, resolved, err := s.updateWorkspaceSettings(ctx, id, updateWorkspaceRequest{Name: "renamed-legacy", DatabaseName: "db", CloudAccount: "Direct Redis"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != id || updated.ID != id {
		t.Fatalf("renamed namespace: %+v %q", updated, resolved)
	}
	got, err := s.store.GetWorkspaceMeta(ctx, "renamed-legacy")
	if err != nil || got.ID != id {
		t.Fatalf("name resolution: %+v %v", got, err)
	}
	if exists, err := s.store.rdb.Exists(ctx, workspaceMetaKey("renamed-legacy")).Result(); err != nil || exists != 0 {
		t.Fatalf("created a different namespace: %d %v", exists, err)
	}
}
