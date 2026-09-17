package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// MarkWorkspaceDirtyHint updates the hint on the latest metadata, never writing
// back a stale caller snapshot of the checkpoint head or ownership fields.
func (s *Store) MarkWorkspaceDirtyHint(ctx context.Context, workspace string) error {
	_, id, err := s.resolveWorkspaceMeta(ctx, workspace)
	if err != nil {
		return err
	}
	generation, err := s.WorkspaceGeneration(ctx, id)
	if err != nil {
		return err
	}
	err = s.rdb.Watch(ctx, func(tx *redis.Tx) error {
		current, err := tx.Get(ctx, WorkspaceGenerationKey(id)).Result()
		if err != nil {
			return err
		}
		if current != generation {
			return ErrWorkspaceConflict
		}
		meta, err := getJSON[WorkspaceMeta](ctx, tx, workspaceMetaKey(id))
		if err != nil {
			return err
		}
		meta.DirtyHint = true
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, workspaceRootDirtyKey(id), "1", 0)
			return setJSON(ctx, pipe, workspaceMetaKey(id), meta)
		})
		return err
	}, WorkspaceGenerationKey(id), workspaceMetaKey(id))
	if err == redis.TxFailedErr {
		return ErrWorkspaceConflict
	}
	return err
}

// Update settings against the metadata read inside the transaction, so a save or
// direct-client update cannot be replaced by an earlier UI snapshot.
func (s *Service) updateWorkspaceSettings(ctx context.Context, workspace string, input updateWorkspaceRequest) (WorkspaceMeta, string, error) {
	_, id, err := s.store.resolveWorkspaceMeta(ctx, workspace)
	if err != nil {
		return WorkspaceMeta{}, "", err
	}
	databaseName := strings.TrimSpace(input.DatabaseName)
	if databaseName == "" {
		return WorkspaceMeta{}, "", fmt.Errorf("database name is required")
	}
	cloudAccount := strings.TrimSpace(input.CloudAccount)
	if cloudAccount == "" {
		return WorkspaceMeta{}, "", fmt.Errorf("cloud account is required")
	}
	name := strings.TrimSpace(input.Name)
	if name != "" {
		if err := ValidateName("workspace", name); err != nil {
			return WorkspaceMeta{}, "", err
		}
	}
	generation, err := s.store.WorkspaceGeneration(ctx, id)
	if err != nil {
		return WorkspaceMeta{}, "", err
	}
	keys := []string{workspaceMetaKey(id), workspaceMetadataArchiveKey(workspaceMetaKey(id)), WorkspaceGenerationKey(id), workspaceNameIndexKey()}
	if name != "" && name != id {
		keys = append(keys, workspaceMetaKey(name))
	}
	var updated WorkspaceMeta
	for attempt := 0; attempt < 8; attempt++ {
		err = s.store.rdb.Watch(ctx, func(tx *redis.Tx) error {
			currentGeneration, err := tx.Get(ctx, WorkspaceGenerationKey(id)).Result()
			if err != nil {
				return err
			}
			if currentGeneration != generation {
				return ErrWorkspaceConflict
			}
			meta, err := getJSON[WorkspaceMeta](ctx, tx, workspaceMetaKey(id))
			if err != nil {
				return err
			}
			oldName := meta.Name
			newName := name
			if newName == "" {
				newName = oldName
			}
			if newName != oldName {
				indexed, err := tx.HGet(ctx, workspaceNameIndexKey(), newName).Result()
				if err != nil && !errors.Is(err, redis.Nil) {
					return err
				}
				if indexed != "" && indexed != id {
					return fmt.Errorf("workspace %q already exists", newName)
				}
				if newName != id {
					exists, err := tx.Exists(ctx, workspaceMetaKey(newName)).Result()
					if err != nil {
						return err
					}
					if exists != 0 {
						return fmt.Errorf("workspace %q already exists", newName)
					}
				}
			}
			oldIndex, err := tx.HGet(ctx, workspaceNameIndexKey(), oldName).Result()
			if err != nil && !errors.Is(err, redis.Nil) {
				return err
			}
			meta = applyWorkspaceMetaDefaults(s.cfg, meta)
			// A name-keyed legacy namespace keeps its storage ID when renamed.
			meta.ID = id
			meta.Name = newName
			meta.Description = strings.TrimSpace(input.Description)
			meta.DatabaseName = databaseName
			meta.CloudAccount = cloudAccount
			meta.Region = strings.TrimSpace(input.Region)
			meta.Tags = workspaceTags(meta.Region, workspaceSource(meta))
			meta.UpdatedAt = time.Now().UTC()
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				if err := setJSON(ctx, pipe, workspaceMetaKey(id), meta); err != nil {
					return err
				}
				pipe.HSet(ctx, workspaceNameIndexKey(), newName, id)
				if oldName != newName && oldIndex == id {
					pipe.HDel(ctx, workspaceNameIndexKey(), oldName)
				}
				return nil
			})
			if err == nil {
				updated = meta
			}
			return err
		}, keys...)
		if !errors.Is(err, redis.TxFailedErr) {
			return updated, id, err
		}
	}
	return WorkspaceMeta{}, "", ErrWorkspaceConflict
}
