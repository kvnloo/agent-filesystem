package controlplane

// Legacy composition records are read only for migration and credential
// retirement. No API or lifecycle operations create, modify, or delete them.
import (
	"context"
	"net/http"
	"time"
)

type workspaceCompositionMount struct {
	VolumeID      string `json:"volume_id"`
	VolumeName    string `json:"volume_name,omitempty"`
	MountPath     string `json:"mount_path"`
	Readonly      bool   `json:"readonly"`
	VolumeTokenID string `json:"volume_token_id,omitempty"`
}

type workspaceComposition struct {
	Version        int                         `json:"version"`
	ID             string                      `json:"id"`
	Name           string                      `json:"name"`
	Description    string                      `json:"description,omitempty"`
	DatabaseID     string                      `json:"database_id,omitempty"`
	DatabaseName   string                      `json:"database_name,omitempty"`
	CloudAccount   string                      `json:"cloud_account,omitempty"`
	OwnerSubject   string                      `json:"owner_subject,omitempty"`
	OwnerLabel     string                      `json:"owner_label,omitempty"`
	Mounts         []workspaceCompositionMount `json:"mounts"`
	CreatedAt      time.Time                   `json:"created_at"`
	UpdatedAt      time.Time                   `json:"updated_at"`
	LastActivityAt time.Time                   `json:"last_activity_at,omitempty"`
}

func workspaceCompositionMetaKey(id string) string {
	return "afs:{" + id + "}:workspace:composition:meta"
}

func retiredCompositionRoute(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusGone, map[string]string{
		"error":   "composition_model_retired",
		"message": "One workspace now owns one tree. Use /v1/workspaces with an existing tree ID. Composition records are preserved; run afs-migrate-workspaces for a migration report and reissue workspace-scoped credentials.",
	})
}

// Validating the exact record key avoids accidentally reinterpreting an old
// composition ID/name as a tree. Existing composition tokens fail closed.
func (m *DatabaseManager) tokenBindsTree(ctx context.Context, databaseID, workspaceID string) bool {
	service, _, err := m.serviceFor(ctx, databaseID)
	if err != nil {
		return false
	}
	exists, err := service.store.rdb.Exists(ctx, workspaceCompositionMetaKey(workspaceID)).Result()
	if err != nil || exists != 0 {
		return false
	}
	meta, err := getJSON[WorkspaceMeta](ctx, service.store.rdb, workspaceMetaKey(workspaceID))
	return err == nil && WorkspaceStorageID(meta) == workspaceID
}
