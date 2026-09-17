package controlplane

import (
	"context"
	"github.com/redis/go-redis/v9"
)

func workspaceChangesKey(id string) string { return "afs:{" + id + "}:changes" }
func latestWorkspaceChange(ctx context.Context, cmd redis.Cmdable, id string) (string, error) {
	entries, err := cmd.XRevRangeN(ctx, workspaceChangesKey(id), "+", "-", 1).Result()
	if err != nil && err != redis.Nil {
		return "", err
	}
	if len(entries) == 0 {
		return "0-0", nil
	}
	return entries[0].ID, nil
}

// A clean marker is meaningful only for the exact published state captured.
// Watching the journal prevents clearing a concurrent client's dirty marker.
func markCapturedWorkspaceClean(ctx context.Context, store *Store, id, head, generation, changes string) error {
	err := store.rdb.Watch(ctx, func(tx *redis.Tx) error {
		meta, err := getJSON[WorkspaceMeta](ctx, tx, workspaceMetaKey(id))
		if err != nil {
			return err
		}
		if meta.HeadSavepoint != head {
			return ErrWorkspaceConflict
		}
		current, err := tx.Get(ctx, WorkspaceGenerationKey(id)).Result()
		if err != nil {
			return err
		}
		if current != generation {
			return ErrWorkspaceConflict
		}
		latest, err := latestWorkspaceChange(ctx, tx, id)
		if err != nil {
			return err
		}
		if latest != changes {
			return ErrWorkspaceConflict
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, workspaceRootHeadKey(id), head, 0)
			pipe.Set(ctx, workspaceRootDirtyKey(id), "0", 0)
			return nil
		})
		return err
	}, WorkspaceGenerationKey(id), workspaceChangesKey(id), workspaceMetaKey(id))
	if err == redis.TxFailedErr {
		return ErrWorkspaceConflict
	}
	return err
}
