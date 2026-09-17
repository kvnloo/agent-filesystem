package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/redis/agent-filesystem/internal/controlplane"
	"github.com/redis/agent-filesystem/internal/searchindex"
	"github.com/redis/agent-filesystem/mount/client"
	"github.com/redis/go-redis/v9"
)

type grepProjectionProbe struct{ commands []string }

func (p *grepProjectionProbe) DialHook(next redis.DialHook) redis.DialHook { return next }
func (p *grepProjectionProbe) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if strings.HasPrefix(cmd.Name(), "ft.") {
			p.commands = append(p.commands, cmd.Name())
		}
		return next(ctx, cmd)
	}
}
func (p *grepProjectionProbe) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestGenerationGrepIgnoresLegacyReadyProjection(t *testing.T) {
	_, store, closeStore := seedWorkspaceMountBridgeFixture(t)
	defer closeStore()
	ctx := context.Background()
	fs := client.New(store.rdb, "repo")
	if err := fs.Echo(ctx, "/main.go", []byte("new peer text\n")); err != nil {
		t.Fatal(err)
	}
	stat, err := fs.Stat(ctx, "/main.go")
	if err != nil {
		t.Fatal(err)
	}
	inodeKey := fmt.Sprintf("afs:{repo}:inode:%d", stat.Inode)
	if err := store.rdb.HSet(ctx, inodeKey, "search_state", "ready", "grep_grams_ci", "obsolete").Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.rdb.Set(ctx, searchindex.ReadyKey("repo"), "1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	before, err := store.rdb.HGetAll(ctx, inodeKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	probe := &grepProjectionProbe{}
	store.rdb.AddHook(probe)
	ready, err := ensureWorkspaceSearchIndex(ctx, store.rdb, "repo")
	if err != nil || ready {
		t.Fatalf("legacy projection accepted for fenced tree: ready=%v error=%v", ready, err)
	}
	profile := &grepExecutionProfile{}
	output, err := captureStdout(t, func() error {
		return runFastGrep(ctx, store.rdb, "repo", fs, "/", "repo", grepOptions{patterns: []string{"peer text"}}, profile)
	})
	if err != nil || !strings.Contains(output, "new peer text") {
		t.Fatalf("grep lost current content: output=%q error=%v", output, err)
	}
	if profile.Mode != "fast_backend_grep" {
		t.Fatalf("grep used stale projection: %s", profile.Mode)
	}
	after, err := store.rdb.HGetAll(ctx, inodeKey).Result()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("grep modified inode: before=%v after=%v error=%v", before, after, err)
	}
	if len(probe.commands) != 0 {
		t.Fatalf("grep consulted unverified projection: %v", probe.commands)
	}
}

func TestMaterializeAndMCPRefreshPreservePeerDirtyStateAndHead(t *testing.T) {
	cfg, store, closeStore := seedWorkspaceMountBridgeFixture(t)
	defer closeStore()
	ctx := context.Background()
	staleMeta, err := store.getWorkspaceMeta(ctx, "repo")
	if err != nil {
		t.Fatal(err)
	}
	currentMeta := staleMeta
	currentMeta.HeadSavepoint = "peer-checkpoint"
	if err := store.putWorkspaceMeta(ctx, currentMeta); err != nil {
		t.Fatal(err)
	}
	if err := store.rdb.Set(ctx, controlplane.WorkspaceRootDirtyKey("repo"), "1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	before, err := store.rdb.Get(ctx, "afs:{repo}:workspace:meta").Result()
	if err != nil {
		t.Fatal(err)
	}
	local, err := persistAFSMaterializedState(ctx, cfg, store, staleMeta, false)
	if err != nil || !local.Dirty {
		t.Fatalf("materialize acknowledged unseen edit: dirty=%v error=%v", local.Dirty, err)
	}
	server := &afsMCPServer{store: store}
	dirty, err := server.refreshWorkspaceLiveState(ctx, "repo")
	if err != nil || !dirty {
		t.Fatalf("MCP refresh acknowledged unseen edit: dirty=%v error=%v", dirty, err)
	}
	after, err := store.rdb.Get(ctx, "afs:{repo}:workspace:meta").Result()
	if err != nil || before != after {
		t.Fatalf("read path rewrote peer metadata: before=%s after=%s error=%v", before, after, err)
	}
	rootDirty, known, err := store.workspaceRootDirtyState(ctx, "repo")
	if err != nil || !known || !rootDirty {
		t.Fatalf("read path cleared dirty: dirty=%v known=%v error=%v", rootDirty, known, err)
	}
}
