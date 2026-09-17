package controlplane

import (
	"encoding/json"
)

// Unknown fields survive control-plane metadata rewrites. The companion snapshot
// also retains product metadata if a current direct AFS client serializes its
// intentionally smaller WorkspaceMeta. It is populated only on explicit writes
// and migration, never during discovery.
func workspaceMetadataArchiveKey(metaKey string) string { return metaKey + ":preserved" }

func (m *WorkspaceMeta) UnmarshalJSON(data []byte) error {
	type plain WorkspaceMeta
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*m = WorkspaceMeta(value)
	m.extraFields = fields
	return nil
}

func (m WorkspaceMeta) MarshalJSON() ([]byte, error) {
	type plain WorkspaceMeta
	data, err := json.Marshal(plain(m))
	if err != nil {
		return nil, err
	}
	fields := make(map[string]json.RawMessage, len(m.extraFields))
	for k, v := range m.extraFields {
		fields[k] = v
	}
	// Delete known optional fields so explicit clearing is respected.
	for _, key := range []string{"id", "description", "database_id", "database_name", "cloud_account", "region", "source", "tags", "last_materialized_host"} {
		delete(fields, key)
	}
	var current map[string]json.RawMessage
	if err := json.Unmarshal(data, &current); err != nil {
		return nil, err
	}
	for k, v := range current {
		fields[k] = v
	}
	return json.Marshal(fields)
}
