package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/redis/go-redis/v9"
)

// WorkspaceMigrationReport is a reviewable inventory, not a startup migration.
// Raw metadata preserves fields unknown to this revision. No credentials are
// exported; only token IDs and their grants are included.
type WorkspaceMigrationReport struct {
	Version      int                    `json:"version"`
	DatabaseID   string                 `json:"database_id,omitempty"`
	Fingerprint  string                 `json:"fingerprint"`
	Workspaces   []MigrationWorkspace   `json:"workspaces"`
	Compositions []MigrationComposition `json:"compositions"`
	Catalog      []map[string]any       `json:"catalog"`
	Tokens       []map[string]any       `json:"tokens"`
	Issues       []MigrationIssue       `json:"issues"`
	Instructions []string               `json:"instructions"`
}
type MigrationWorkspace struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Generation        string          `json:"generation,omitempty"`
	Metadata          json.RawMessage `json:"metadata"`
	PreservedMetadata json.RawMessage `json:"preserved_metadata,omitempty"`
	Action            string          `json:"action"`
}
type MigrationComposition struct {
	ID          string                     `json:"id"`
	Name        string                     `json:"name"`
	Metadata    json.RawMessage            `json:"metadata"`
	Attachments []MigrationAttachment      `json:"attachments"`
	Bookmarks   map[string]json.RawMessage `json:"bookmarks"`
	Disposition string                     `json:"disposition"`
}
type MigrationAttachment struct {
	WorkspaceID       string `json:"workspace_id"`
	WorkspaceName     string `json:"workspace_name"`
	PreviousMountPath string `json:"previous_mount_path"`
	Readonly          bool   `json:"readonly"`
	TokenID           string `json:"token_id,omitempty"`
	Exists            bool   `json:"exists"`
}
type MigrationIssue struct {
	Code     string   `json:"code"`
	Name     string   `json:"name,omitempty"`
	IDs      []string `json:"ids,omitempty"`
	Blocking bool     `json:"blocking"`
	Message  string   `json:"message"`
}

// ReadWorkspaceMigrationCatalog opens an existing SQLite catalog read-only.
// It deliberately bypasses OpenDatabaseManager and schema/startup migrations.
func ReadWorkspaceMigrationCatalog(ctx context.Context, path, databaseID string) ([]map[string]any, []map[string]any, error) {
	if databaseID == "" {
		return nil, nil, fmt.Errorf("database-id is required with a catalog")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	uri := url.URL{Scheme: "file", Path: abs}
	q := uri.Query()
	q.Set("mode", "ro")
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	// No implicit writes, including schema migrations or token touches.
	catalog, err := migrationQuery(ctx, db, `SELECT * FROM workspace_catalog WHERE database_id = ? ORDER BY workspace_id`, databaseID)
	if err != nil {
		return nil, nil, err
	}
	var tokens []map[string]any
	for _, table := range []string{"cli_access_tokens", "mcp_access_tokens"} {
		rows, err := migrationQuery(ctx, db, `SELECT * FROM `+table+` WHERE database_id = ? ORDER BY id`, databaseID)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			delete(row, "secret")
			delete(row, "secret_hash")
			row["kind"] = table
			tokens = append(tokens, row)
		}
	}
	return catalog, tokens, nil
}
func migrationQuery(ctx context.Context, db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		item := map[string]any{}
		for i, k := range columns {
			if b, ok := values[i].([]byte); ok {
				item[k] = string(b)
			} else {
				item[k] = values[i]
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// InspectWorkspaceMigration only reads Redis. Trees keep their IDs/names and
// remain independent; compositions are preserved verbatim for disposition.
func InspectWorkspaceMigration(ctx context.Context, rdb *redis.Client, databaseID string, catalog, tokens []map[string]any) (WorkspaceMigrationReport, error) {
	report := WorkspaceMigrationReport{Version: 1, DatabaseID: databaseID, Workspaces: []MigrationWorkspace{}, Compositions: []MigrationComposition{}, Catalog: catalog, Tokens: tokens, Issues: []MigrationIssue{}, Instructions: []string{
		"Stop all old and current writers before adoption; back up Redis and the SQL catalog together.",
		"Keep each existing tree ID and name. Never concatenate trees or replace live roots from checkpoints.",
		"Composition records/bookmarks remain untouched. Their credentials are retired, not rebound. Reissue one credential per tree only after reviewing ownership and attachment restrictions; read-only grants must stay read-only.",
		"Review name collisions and missing attachments. Composition names are not aliases for tree names.",
		"Adoption initializes missing generation markers and preserves extended metadata in compatibility records. Catalog, contents, checkpoints, primary metadata, permissions and existing generations are unchanged.",
	}}
	treeKeys, err := migrationScan(ctx, rdb, "afs:{*}:workspace:meta")
	if err != nil {
		return report, err
	}
	names := map[string][]string{}
	treeByID := map[string]WorkspaceMeta{}
	for _, key := range treeKeys {
		raw, err := rdb.Get(ctx, key).Bytes()
		if err != nil {
			return report, err
		}
		var meta WorkspaceMeta
		if err = json.Unmarshal(raw, &meta); err != nil {
			return report, fmt.Errorf("%s: %w", key, err)
		}
		id := WorkspaceStorageID(meta)
		if key != workspaceMetaKey(id) {
			return report, fmt.Errorf("metadata/key ID mismatch at %s", key)
		}
		gen, err := rdb.Get(ctx, "afs:{"+id+"}:generation").Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return report, err
		}
		rootExists, err := workspaceRootExists(ctx, rdb, id, meta.HeadSavepoint)
		if err != nil {
			return report, err
		}
		if !rootExists {
			report.Issues = append(report.Issues, MigrationIssue{Code: "missing_live_root", IDs: []string{id}, Blocking: true, Message: "Live root is missing or incomplete; migration will not reconstruct it from a checkpoint. Recover explicitly before adoption."})
		}
		action := "preserve_generation_and_archive_metadata"
		if gen == "" {
			action = "initialize_generation_after_writers_stop"
		} else if !strings.HasPrefix(gen, "g_") {
			report.Issues = append(report.Issues, MigrationIssue{Code: "inactive_generation", IDs: []string{id}, Blocking: true, Message: "Workspace generation is deleted, restoring, or otherwise inactive; recover it before adoption."})
		}
		preserved, err := rdb.Get(ctx, workspaceMetadataArchiveKey(key)).Bytes()
		if err != nil && !errors.Is(err, redis.Nil) {
			return report, err
		}
		report.Workspaces = append(report.Workspaces, MigrationWorkspace{ID: id, Name: meta.Name, Generation: gen, Metadata: raw, PreservedMetadata: preserved, Action: action})
		names[meta.Name] = append(names[meta.Name], id)
		treeByID[id] = meta
	}
	for name, ids := range names {
		if len(ids) > 1 {
			report.Issues = append(report.Issues, MigrationIssue{Code: "tree_name_collision", Name: name, IDs: ids, Blocking: true, Message: "Multiple independent trees share a name; resolve explicitly by stable ID."})
		}
	}
	compKeys, err := migrationScan(ctx, rdb, "afs:{*}:workspace:composition:meta")
	if err != nil {
		return report, err
	}
	for _, key := range compKeys {
		raw, err := rdb.Get(ctx, key).Bytes()
		if err != nil {
			return report, err
		}
		var comp workspaceComposition
		if err = json.Unmarshal(raw, &comp); err != nil {
			return report, err
		}
		if key != workspaceCompositionMetaKey(comp.ID) {
			return report, fmt.Errorf("composition/key ID mismatch at %s", key)
		}
		item := MigrationComposition{ID: comp.ID, Name: comp.Name, Metadata: raw, Attachments: []MigrationAttachment{}, Bookmarks: map[string]json.RawMessage{}, Disposition: "retain_record; expose existing trees independently; reissue reviewed per-tree grants"}
		if _, exists := treeByID[comp.ID]; exists {
			report.Issues = append(report.Issues, MigrationIssue{Code: "resource_id_collision", IDs: []string{comp.ID}, Blocking: true, Message: "An ID identifies both a tree and a composition; resolve before adoption. Tokens are denied."})
		}
		if ids := names[comp.Name]; len(ids) > 0 {
			report.Issues = append(report.Issues, MigrationIssue{Code: "composition_name_collision", Name: comp.Name, IDs: append([]string{comp.ID}, ids...), Message: "Composition name matches an independent tree; the composition is retained and must never become a name alias."})
		}
		for _, mount := range comp.Mounts {
			meta, exists := treeByID[mount.VolumeID]
			item.Attachments = append(item.Attachments, MigrationAttachment{WorkspaceID: mount.VolumeID, WorkspaceName: firstNonEmpty(meta.Name, mount.VolumeName), PreviousMountPath: mount.MountPath, Readonly: mount.Readonly, TokenID: mount.VolumeTokenID, Exists: exists})
			if !exists {
				report.Issues = append(report.Issues, MigrationIssue{Code: "missing_attachment", IDs: []string{comp.ID, mount.VolumeID}, Blocking: true, Message: "Attachment has no matching independent tree. Restore or explicitly resolve the missing record."})
			}
		}
		bookmarks, err := migrationScan(ctx, rdb, "afs:{"+comp.ID+"}:workspace:composition:bookmark:*")
		if err != nil {
			return report, err
		}
		for _, key := range bookmarks {
			value, err := rdb.Get(ctx, key).Bytes()
			if err != nil {
				return report, err
			}
			if !json.Valid(value) {
				return report, fmt.Errorf("invalid bookmark JSON: %s", key)
			}
			item.Bookmarks[key] = value
		}
		report.Compositions = append(report.Compositions, item)
	}
	sort.Slice(report.Issues, func(i, j int) bool {
		a, b := report.Issues[i], report.Issues[j]
		return a.Code+a.Name+strings.Join(a.IDs, ",") < b.Code+b.Name+strings.Join(b.IDs, ",")
	})
	payload, err := json.Marshal(report)
	if err != nil {
		return report, err
	}
	sum := sha256.Sum256(payload)
	report.Fingerprint = hex.EncodeToString(sum[:])
	return report, nil
}
func migrationScan(ctx context.Context, rdb *redis.Client, pattern string) ([]string, error) {
	seen := map[string]bool{}
	var cursor uint64
	for {
		keys, next, err := rdb.Scan(ctx, cursor, pattern, 128).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			seen[key] = true
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	out := make([]string, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}

// ApplyWorkspaceMigration refuses unreviewed or stale inventories. This is an
// explicit offline adoption operation, never invoked by ordinary startup.
func ApplyWorkspaceMigration(ctx context.Context, rdb *redis.Client, reviewed, current WorkspaceMigrationReport, writersStopped bool) error {
	if !writersStopped {
		return fmt.Errorf("adoption requires explicit confirmation that all writers are stopped")
	}
	if reviewed.Version != 1 || reviewed.Fingerprint == "" || reviewed.Fingerprint != current.Fingerprint {
		return fmt.Errorf("migration report is stale or invalid; inspect again and review the new report")
	}
	for _, issue := range current.Issues {
		if issue.Blocking {
			return fmt.Errorf("migration blocked by %s: %s", issue.Code, issue.Message)
		}
	}
	store := NewStore(rdb)
	for _, workspace := range current.Workspaces {
		if _, err := store.AdoptWorkspaceGeneration(ctx, workspace.ID); err != nil {
			return fmt.Errorf("adopt %s: %w; already adopted trees are safe to preserve on a fresh report", workspace.ID, err)
		}
	}
	return nil
}
