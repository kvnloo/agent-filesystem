package controlplane

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	afsclient "github.com/redis/agent-filesystem/mount/client"
	"github.com/redis/go-redis/v9"
)

// Steal ownership immediately before EXEC, after the replacement has checked
// its lease. This models a paused worker resuming after another restore won.
type rootReplacementTakeoverHook struct {
	fired  atomic.Bool
	match  func([]redis.Cmder) bool
	before func()
}

func (h *rootReplacementTakeoverHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *rootReplacementTakeoverHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return next
}
func (h *rootReplacementTakeoverHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		if h.match(commands) && h.fired.CompareAndSwap(false, true) {
			h.before()
		}
		return next(ctx, commands)
	}
}

func TestRootReplacementRejectsTakeoverAtEveryBatch(t *testing.T) {
	for _, stage := range []string{"cleanup", "materialize", "clean-marker", "publish-generation"} {
		t.Run(stage, func(t *testing.T) {
			ctx, service, mr, id := newStorageSafetyFixture(t)
			if err := afsclient.New(service.store.rdb, id).Echo(ctx, "/existing", []byte("original tree")); err != nil {
				t.Fatal(err)
			}
			peer := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = peer.Close() })
			var afterTakeover string
			hook := &rootReplacementTakeoverHook{
				match: func(commands []redis.Cmder) bool {
					for _, cmd := range commands {
						args := cmd.Args()
						if len(args) < 2 {
							continue
						}
						key := fmt.Sprint(args[1])
						switch stage {
						case "cleanup":
							if cmd.Name() == "del" && strings.HasPrefix(key, "afs:{"+id+"}:inode:") {
								return true
							}
						case "materialize":
							if cmd.Name() == "hset" && key == workspaceFSInodeKey(id, "1") {
								return true
							}
						case "clean-marker":
							if cmd.Name() == "set" && key == workspaceRootDirtyKey(id) && fmt.Sprint(args[2]) == "0" {
								return true
							}
						case "publish-generation":
							if cmd.Name() == "set" && key == WorkspaceGenerationKey(id) && strings.HasPrefix(fmt.Sprint(args[2]), "g_") {
								return true
							}
						}
					}
					return false
				},
				before: func() {
					_, err := peer.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
						pipe.Set(ctx, ImportLockKey(id), "new-owner", time.Minute)
						pipe.Set(ctx, WorkspaceGenerationKey(id), "g_new-owner", 0)
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					afterTakeover = mr.Dump()
				},
			}
			service.store.rdb.AddHook(hook)
			manifest := Manifest{Version: formatVersion, Workspace: id, Savepoint: "old-worker", Entries: map[string]ManifestEntry{
				"/obsolete": {Type: "file", Mode: 0600, Inline: base64.StdEncoding.EncodeToString([]byte("must never publish after takeover"))},
			}}
			err := SyncWorkspaceRoot(ctx, service.store, id, manifest)
			if !errors.Is(err, ErrWorkspaceConflict) && !errors.Is(err, ErrImportInProgress) {
				t.Fatalf("lost replacement returned %v", err)
			}
			if !hook.fired.Load() {
				t.Fatal("did not inject lease takeover")
			}
			if got := mr.Dump(); got != afterTakeover {
				t.Fatalf("obsolete replacement mutated Redis after takeover:\nbefore: %s\nafter: %s", afterTakeover, got)
			}
		})
	}
}

func TestRootReplacementRequiresBothLeaseAndGeneration(t *testing.T) {
	for _, changed := range []string{"lease", "generation"} {
		t.Run(changed, func(t *testing.T) {
			ctx, service, mr, id := newStorageSafetyFixture(t)
			peer := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = peer.Close() })
			if err := peer.Set(ctx, ImportLockKey(id), "owner", time.Minute).Err(); err != nil {
				t.Fatal(err)
			}
			generation, err := service.WorkspaceGeneration(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			hook := &rootReplacementTakeoverHook{
				match: func(commands []redis.Cmder) bool {
					for _, cmd := range commands {
						if cmd.Name() == "set" && fmt.Sprint(cmd.Args()[1]) == workspaceRootDirtyKey(id) {
							return true
						}
					}
					return false
				},
				before: func() {
					key, value := ImportLockKey(id), "new-owner"
					if changed == "generation" {
						key, value = WorkspaceGenerationKey(id), "g_new-owner"
					}
					if err := peer.Set(ctx, key, value, 0).Err(); err != nil {
						t.Fatal(err)
					}
				},
			}
			service.store.rdb.AddHook(hook)
			err = mutateWorkspaceGeneration(ctx, service.store.rdb, id, "owner", generation, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, workspaceRootDirtyKey(id), "obsolete", 0)
				return nil
			})
			if !errors.Is(err, ErrWorkspaceConflict) {
				t.Fatalf("lost %s returned %v", changed, err)
			}
			if got := peer.Get(ctx, workspaceRootDirtyKey(id)).Val(); got != "0" {
				t.Fatalf("lost %s wrote dirty marker %q", changed, got)
			}
		})
	}
}
