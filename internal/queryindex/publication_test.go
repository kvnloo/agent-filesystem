package queryindex

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestProjectionFollowsCurrentClientJournalAndRevisions(t *testing.T) {
	ctx, rdb, key := setupQueryIndexTest(t)
	if err := rdb.Set(ctx, generationKey(key), "g_original", 0).Err(); err != nil {
		t.Fatal(err)
	}
	writeQueryIndexFile(t, ctx, rdb, key, "2", "/note", []byte("first current publication"))
	if err := rdb.HSet(ctx, InodeKey(key, "2"), "revision", "r1").Err(); err != nil {
		t.Fatal(err)
	}
	if err := ReconcilePublications(ctx, rdb, key); err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessPending(ctx, rdb, key, 10); err != nil {
		t.Fatal(err)
	}
	// Current clients intentionally don't call this product's QueueMarkDirty.
	writeQueryIndexFileWithDirty(t, ctx, rdb, key, "2", "/note", []byte("second current publication"), false)
	if err := rdb.HSet(ctx, InodeKey(key, "2"), "revision", "r2").Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: "afs:{" + key + "}:changes", Values: map[string]any{"payload": "current publication"}}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := EnsureReady(ctx, rdb, key, "/", 100); err != nil {
		t.Fatal(err)
	}
	revision, err := rdb.HGet(ctx, InodeKey(key, "2"), "query_revision").Result()
	if err != nil || revision != "r2" {
		t.Fatalf("query revision=%q %v", revision, err)
	}
	chunks, err := rdb.SMembers(ctx, ChunkSetKey(key, "2")).Result()
	if err != nil || len(chunks) != 1 {
		t.Fatalf("chunks=%v %v", chunks, err)
	}
	text, err := rdb.HGet(ctx, chunks[0], "text").Result()
	if err != nil || !strings.Contains(text, "second current") {
		t.Fatalf("stale projection=%q %v", text, err)
	}
	if err := rdb.Del(ctx, InodeKey(key, "2"), ContentKey(key, "2")).Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: "afs:{" + key + "}:changes", Values: map[string]any{"payload": "current deletion"}}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := EnsureReady(ctx, rdb, key, "/", 100); err != nil {
		t.Fatal(err)
	}
	if rdb.Exists(ctx, chunks[0]).Val() != 0 {
		t.Fatal("current deletion retained searchable stale content")
	}
}

func TestProjectionCannotPublishAcrossWriterRestoreOrDeletion(t *testing.T) {
	for _, change := range []string{"writer", "restore", "delete"} {
		t.Run(change, func(t *testing.T) {
			ctx, rdb, key := setupQueryIndexTest(t)
			peer := redis.NewClient(rdb.Options())
			defer peer.Close()
			if err := rdb.Set(ctx, generationKey(key), "g_original", 0).Err(); err != nil {
				t.Fatal(err)
			}
			writeQueryIndexFile(t, ctx, rdb, key, "2", "/note", []byte("observed bytes"))
			if err := rdb.HSet(ctx, InodeKey(key, "2"), "revision", "r1").Err(); err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			rdb.AddHook(&projectionReadHook{after: func(cmd redis.Cmder) {
				if cmd.Name() == "get" && len(cmd.Args()) > 1 && cmd.Args()[1] == ContentKey(key, "2") {
					once.Do(func() {
						switch change {
						case "writer":
							if err := peer.Set(ctx, ContentKey(key, "2"), "newer bytes", 0).Err(); err != nil {
								t.Fatal(err)
							}
							if err := peer.HSet(ctx, InodeKey(key, "2"), "revision", "r2").Err(); err != nil {
								t.Fatal(err)
							}
						case "restore":
							if err := peer.Set(ctx, generationKey(key), "g_replacement", 0).Err(); err != nil {
								t.Fatal(err)
							}
							if err := peer.Del(ctx, InodeKey(key, "2")).Err(); err != nil {
								t.Fatal(err)
							}
						case "delete":
							if err := peer.Set(ctx, generationKey(key), "deleted", 0).Err(); err != nil {
								t.Fatal(err)
							}
							if err := peer.Del(ctx, InodeKey(key, "2")).Err(); err != nil {
								t.Fatal(err)
							}
						}
					})
				}
			}})
			result, err := ProcessPending(ctx, rdb, key, 10)
			if err != nil {
				t.Fatal(err)
			}
			if result.Errors != 1 {
				t.Fatalf("unguarded projection result: %+v", result)
			}
			if change != "writer" && peer.Exists(ctx, InodeKey(key, "2")).Val() != 0 {
				t.Fatal("projection recreated retired inode")
			}
			chunks, err := peer.SMembers(ctx, ChunkSetKey(key, "2")).Result()
			if err != nil || len(chunks) != 0 {
				t.Fatalf("stale chunks published: %v %v", chunks, err)
			}
		})
	}
}

type projectionReadHook struct{ after func(redis.Cmder) }

func (h *projectionReadHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h *projectionReadHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error { err := next(ctx, cmd); h.after(cmd); return err }
}
func (h *projectionReadHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
