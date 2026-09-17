package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"os"
	"strings"
)

// WorkspaceGenerationKey is deliberately outside root inode/content cleanup.
// Deletion retains a tombstone so an old daemon cannot recreate this ID.
func WorkspaceGenerationKey(id string) string { return "afs:{" + id + "}:generation" }

func newWorkspaceGeneration() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return "g_" + hex.EncodeToString(token[:]), nil
}

func (s *Store) WorkspaceGeneration(ctx context.Context, workspace string) (string, error) {
	meta, err := s.GetWorkspaceMeta(ctx, workspace)
	if err != nil {
		return "", err
	}
	generation, err := s.rdb.Get(ctx, WorkspaceGenerationKey(workspaceStorageID(meta))).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrWorkspaceAdoptionRequired
	}
	if err != nil {
		return "", err
	}
	if generation == "deleted" {
		return "", os.ErrNotExist
	}
	if !strings.HasPrefix(generation, "g_") {
		return "", fmt.Errorf("workspace is being restored; wait for restore to complete")
	}
	return generation, nil
}

func (s *Service) WorkspaceGeneration(ctx context.Context, workspace string) (string, error) {
	return s.store.WorkspaceGeneration(ctx, workspace)
}

var ErrWorkspaceAdoptionRequired = errors.New("workspace requires explicit generation adoption; stop existing writers and run workspace migration")

type rootReplacementKey struct{}
type rootReplacement struct{ id, token, generation string }

// AdoptWorkspaceGeneration is migration-only. The caller must have stopped old
// writers; their protocol cannot be fenced. No startup or read path calls it.
func (s *Store) AdoptWorkspaceGeneration(ctx context.Context, workspace string) (string, error) {
	meta, err := s.GetWorkspaceMeta(ctx, workspace)
	if err != nil {
		return "", err
	}
	id := workspaceStorageID(meta)
	lock, err := AcquireImportLock(ctx, s, id)
	if err != nil {
		return "", err
	}
	defer lock.Release(context.Background())
	existing, err := s.rdb.Get(ctx, WorkspaceGenerationKey(id)).Result()
	if err == nil && !strings.HasPrefix(existing, "g_") {
		return "", ErrWorkspaceConflict
	}
	if err != nil && !errors.Is(err, redis.Nil) {
		return "", err
	}
	exists, err := workspaceRootExists(ctx, s.rdb, id, meta.HeadSavepoint)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("cannot adopt workspace without an intact live root")
	}
	raw, err := s.rdb.Get(ctx, workspaceMetaKey(id)).Bytes()
	if err != nil {
		return "", err
	}
	archiveKey := workspaceMetadataArchiveKey(workspaceMetaKey(id))
	archived, err := s.rdb.Get(ctx, archiveKey).Bytes()
	if err != nil && !errors.Is(err, redis.Nil) {
		return "", err
	}
	if err == nil {
		// A slim client's latest metadata may omit fields in an earlier archive.
		// Retain those fields while capturing explicit values from the live record.
		var preserved, current map[string]json.RawMessage
		if err := json.Unmarshal(archived, &preserved); err != nil {
			return "", err
		}
		if err := json.Unmarshal(raw, &current); err != nil {
			return "", err
		}
		for key, value := range current {
			preserved[key] = value
		}
		raw, err = json.Marshal(preserved)
		if err != nil {
			return "", err
		}
	}
	generation := existing
	if generation == "" {
		generation, err = newWorkspaceGeneration()
		if err != nil {
			return "", err
		}
	}
	if err := mutateWorkspaceGeneration(ctx, s.rdb, id, lock.Token(), existing, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, archiveKey, raw, 0)
		if existing == "" {
			pipe.Set(ctx, WorkspaceGenerationKey(id), generation, 0)
		}
		return nil
	}); err != nil {
		return "", err
	}
	return generation, nil
}

// mutateWorkspaceGeneration makes ownership of a replacement part of the same
// Redis transaction as its writes. A heartbeat is only advisory: a paused worker
// must not resume writes after its lease expired and another worker took over.
func mutateWorkspaceGeneration(ctx context.Context, rdb *redis.Client, id, token, generation string, queue func(redis.Pipeliner) error) error {
	if token == "" {
		return ErrImportInProgress
	}
	err := rdb.Watch(ctx, func(tx *redis.Tx) error {
		actualToken, err := tx.Get(ctx, ImportLockKey(id)).Result()
		if errors.Is(err, redis.Nil) || (err == nil && actualToken != token) {
			return ErrImportInProgress
		}
		if err != nil {
			return err
		}
		actualGeneration, err := tx.Get(ctx, WorkspaceGenerationKey(id)).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		if actualGeneration != generation {
			return ErrWorkspaceConflict
		}
		_, err = tx.TxPipelined(ctx, queue)
		return err
	}, ImportLockKey(id), WorkspaceGenerationKey(id))
	if errors.Is(err, redis.TxFailedErr) {
		return ErrWorkspaceConflict
	}
	return err
}

func transitionRootGeneration(ctx context.Context, store *Store, id, token, expected, next string) error {
	return mutateWorkspaceGeneration(ctx, store.rdb, id, token, expected, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, WorkspaceGenerationKey(id), next, 0)
		return nil
	})
}

func mutateRootReplacement(ctx context.Context, rdb *redis.Client, id string, queue func(redis.Pipeliner) error) error {
	replacement, ok := ctx.Value(rootReplacementKey{}).(rootReplacement)
	if !ok || replacement.id != id || replacement.generation == "" {
		return ErrWorkspaceConflict
	}
	return mutateWorkspaceGeneration(ctx, rdb, id, replacement.token, replacement.generation, queue)
}

func beginRootReplacement(ctx context.Context, store *Store, id, importLockToken string) (context.Context, func(bool) error, error) {
	if replacement, ok := ctx.Value(rootReplacementKey{}).(rootReplacement); ok && replacement.id == id {
		return ctx, func(bool) error { return nil }, nil
	}
	var lock *ImportLock
	var err error
	if importLockToken == "" {
		lock, err = AcquireImportLock(ctx, store, id)
		if err != nil {
			return ctx, nil, err
		}
		importLockToken = lock.Token()
	}
	release := func() {
		if lock != nil {
			_ = lock.Release(context.Background())
		}
	}
	fail := func(err error) (context.Context, func(bool) error, error) { release(); return ctx, nil, err }
	old, err := store.rdb.Get(ctx, WorkspaceGenerationKey(id)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fail(err)
	}
	if errors.Is(err, redis.Nil) {
		exists, e := workspaceRootExists(ctx, store.rdb, id, "")
		if e != nil {
			return fail(e)
		}
		if exists {
			return fail(ErrWorkspaceAdoptionRequired)
		}
	} else if !strings.HasPrefix(old, "g_") {
		return fail(ErrWorkspaceConflict)
	}
	next, err := newWorkspaceGeneration()
	if err != nil {
		return fail(err)
	}
	restoring := "restoring:" + next
	if err := transitionRootGeneration(ctx, store, id, importLockToken, old, restoring); err != nil {
		return fail(err)
	}
	ctx = context.WithValue(ctx, rootReplacementKey{}, rootReplacement{id: id, token: importLockToken, generation: restoring})
	released := false
	return ctx, func(success bool) error {
		if released {
			return nil
		}
		released = true
		defer release()
		if !success {
			return nil
		}
		return transitionRootGeneration(ctx, store, id, importLockToken, restoring, next)
	}, nil
}
