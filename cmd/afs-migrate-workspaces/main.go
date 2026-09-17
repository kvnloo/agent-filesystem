// afs-migrate-workspaces is deliberately separate from server startup and the
// user's saved CLI configuration. Every data source must be explicitly named.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/redis/agent-filesystem/internal/controlplane"
	"github.com/redis/go-redis/v9"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	redisURL := flag.String("redis-url", "", "explicit Redis URL; no saved configuration is loaded")
	catalogPath := flag.String("catalog", "", "existing SQLite catalog to read without schema changes")
	noCatalog := flag.Bool("no-catalog", false, "explicitly confirm this Redis-only installation has no SQL catalog")
	databaseID := flag.String("database-id", "", "catalog database ID for this Redis instance")
	applyPath := flag.String("apply-report", "", "apply generation adoption using a previously reviewed JSON report (default: dry run)")
	stopped := flag.Bool("writers-stopped", false, "confirm all old/current clients and servers have stopped writing")
	flag.Parse()
	if *noCatalog && *catalogPath != "" {
		return fmt.Errorf("--catalog and --no-catalog are mutually exclusive")
	}
	if *redisURL == "" {
		return fmt.Errorf("--redis-url is required; default operation is a read-only JSON report")
	}
	opts, err := redis.ParseURL(*redisURL)
	if err != nil {
		return fmt.Errorf("invalid Redis URL")
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var catalog, tokens []map[string]any
	if *catalogPath != "" {
		catalog, tokens, err = controlplane.ReadWorkspaceMigrationCatalog(ctx, *catalogPath, *databaseID)
		if err != nil {
			return err
		}
	}
	report, err := controlplane.InspectWorkspaceMigration(ctx, rdb, *databaseID, catalog, tokens)
	if err != nil {
		return err
	}
	if *applyPath != "" {
		if *catalogPath == "" && !*noCatalog {
			return fmt.Errorf("--apply-report requires --catalog and --database-id, or --no-catalog for a Redis-only installation")
		}
		raw, err := os.ReadFile(*applyPath)
		if err != nil {
			return err
		}
		var reviewed controlplane.WorkspaceMigrationReport
		if err = json.Unmarshal(raw, &reviewed); err != nil {
			return err
		}
		if err = controlplane.ApplyWorkspaceMigration(ctx, rdb, reviewed, report, *stopped); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Adoption complete. Tree IDs, contents, checkpoints, metadata, catalog and composition records preserved. Reissue reviewed per-workspace credentials before reconnecting clients.")
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
