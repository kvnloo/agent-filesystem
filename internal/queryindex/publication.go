package queryindex

import (
	"context"
	"errors"
	"strings"

	"github.com/redis/go-redis/v9"
)

type projectionObservationKey struct{}
type projectionObservation struct{ generation, revision, path, kind string }

func generationKey(fsKey string) string { return "afs:{" + fsKey + "}:generation" }

// Direct AFS writers publish revisions and a journal, not this product's query
// dirty flags. Reconcile changed observations before trusting the projection.
func ReconcilePublications(ctx context.Context, rdb *redis.Client, fsKey string) error {
	generation, err := rdb.Get(ctx, generationKey(fsKey)).Result()
	if err != nil && err != redis.Nil {
		return err
	}
	if generation != "" && !strings.HasPrefix(generation, "g_") {
		return ErrProjectionStale
	}
	entries, err := rdb.XRevRangeN(ctx, "afs:{"+fsKey+"}:changes", "+", "-", 1).Result()
	if err != nil && err != redis.Nil {
		return err
	}
	last := "0-0"
	if len(entries) > 0 {
		last = entries[0].ID
	}
	key := ReadyKey(fsKey) + ":journal"
	prior, _ := rdb.Get(ctx, key).Result()
	position := generation + ":" + last
	if prior == position {
		return nil
	}
	ids := []interface{}{}
	err = scanFiles(ctx, rdb, fsKey, "/", func(fields map[string]string) error {
		if fields["query_revision"] != fields["revision"] || fields["query_path"] != fields["path"] || fields["query_index_version"] != ProjectionVersion {
			ids = append(ids, fields["id"])
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Missing inode IDs also need their old chunk projections removed.
	var cursor uint64
	prefix := "afs:{" + fsKey + "}:qchunks:"
	for {
		keys, next, e := rdb.Scan(ctx, cursor, prefix+"*", 256).Result()
		if e != nil {
			return e
		}
		for _, chunkSet := range keys {
			id := strings.TrimPrefix(chunkSet, prefix)
			exists, e := rdb.Exists(ctx, InodeKey(fsKey, id)).Result()
			if e != nil {
				return e
			}
			if exists == 0 {
				ids = append(ids, id)
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return rdb.Watch(ctx, func(tx *redis.Tx) error {
		current, e := tx.Get(ctx, generationKey(fsKey)).Result()
		if e != nil && e != redis.Nil {
			return e
		}
		if current != generation {
			return ErrProjectionStale
		}
		_, e = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			if len(ids) > 0 {
				pipe.SAdd(ctx, DirtySetKey(fsKey), ids...)
			}
			pipe.Set(ctx, key, position, 0)
			return nil
		})
		return e
	}, generationKey(fsKey))
}

func guardedProjection(ctx context.Context, rdb *redis.Client, fsKey, inodeID string, build func(redis.Pipeliner) error) error {
	expected, ok := ctx.Value(projectionObservationKey{}).(projectionObservation)
	if !ok {
		return errors.New("query projection missing publication observation")
	}
	err := rdb.Watch(ctx, func(tx *redis.Tx) error {
		generation, e := tx.Get(ctx, generationKey(fsKey)).Result()
		if e != nil && e != redis.Nil {
			return e
		}
		if generation != expected.generation {
			return ErrProjectionStale
		}
		values, e := tx.HMGet(ctx, InodeKey(fsKey, inodeID), "type", "revision", "path").Result()
		if e != nil {
			return e
		}
		if redisString(values[0]) != expected.kind || redisString(values[1]) != expected.revision || redisString(values[2]) != expected.path {
			return ErrProjectionStale
		}
		_, e = tx.TxPipelined(ctx, build)
		return e
	}, generationKey(fsKey), InodeKey(fsKey, inodeID), ChunkSetKey(fsKey, inodeID))
	if err == redis.TxFailedErr {
		return ErrProjectionStale
	}
	return err
}

var markExistingDirty = redis.NewScript(`
if redis.call('EXISTS',KEYS[1]) == 1 then
 for i=2,#ARGV,2 do redis.call('HSET',KEYS[1],ARGV[i],ARGV[i+1]) end
end
redis.call('SADD',KEYS[2],ARGV[1])
return 1
`)
