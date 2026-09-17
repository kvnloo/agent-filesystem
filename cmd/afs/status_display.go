package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// appendAuthStatusRows appends cloud-auth hint rows when action is needed.
// We intentionally drop the "signed in: yes" confirmation (it's noise — the
// cloud database in the core rows already implies success), the control-plane
// URL (duplicate of configStatusRow), and the cloud database id (already in
// the core "database" row). Only actionable rows (e.g. "needs refresh")
// survive.
func appendAuthStatusRows(rows []outputRow) []outputRow {
	bin := filepath.Base(os.Args[0])
	info, _ := authConnectionInfo(bin)
	for _, row := range info {
		switch row.Label {
		case "hint":
			rows = append(rows, row)
		case "signed in":
			if row.Value != "yes" {
				rows = append(rows, row)
			}
		}
	}
	return rows
}

func userModeLabel(backendName string) string {
	switch backendName {
	case mountBackendNone:
		return "None"
	case mountBackendFuse:
		return "FUSE"
	case mountBackendNFS:
		return "NFS"
	default:
		return strings.ToUpper(backendName)
	}
}

func redisDatabaseLabel(addr string, db int, tls bool) string {
	scheme := "redis"
	if tls {
		scheme = "rediss"
	}
	return fmt.Sprintf("%s://%s/%d", scheme, addr, db)
}

func statusRemoteLabel(addr string, db int) string {
	return redisDatabaseLabel(addr, db, false)
}

func configRemoteLabel(cfg config) string {
	productMode, err := effectiveProductMode(cfg)
	if err == nil && productMode != productModeLocal {
		label := strings.TrimSpace(cfg.URL)
		if label == "" {
			label = "<control plane url not configured>"
		}
		if db := strings.TrimSpace(cfg.DatabaseID); db != "" {
			return fmt.Sprintf("%s (%s)", label, db)
		}
		return label + " (auto database)"
	}
	return redisDatabaseLabel(cfg.RedisAddr, cfg.RedisDB, cfg.RedisTLS)
}

func currentConfigScopeLabel(cfg config) string {
	mode, err := effectiveProductMode(cfg)
	if err != nil {
		return configRemoteLabel(cfg)
	}
	return strings.TrimSpace(productModeDisplayLabel(mode) + " " + configRemoteLabel(cfg))
}

func mountRecordScopeLabel(rec mountRecord) string {
	mode := mountRecordProductMode(rec)
	label := productModeDisplayLabel(mode)
	switch mode {
	case productModeSelfHosted, productModeCloud:
		target := strings.TrimSpace(rec.ControlPlaneURL)
		if target == "" {
			target = "<control plane url not configured>"
		}
		if db := strings.TrimSpace(rec.ControlPlaneDatabase); db != "" {
			target = fmt.Sprintf("%s (%s)", target, db)
		}
		return strings.TrimSpace(label + " " + target)
	default:
		addr := strings.TrimSpace(rec.RedisAddr)
		if addr == "" {
			addr = "<redis not configured>"
		}
		return strings.TrimSpace(label + " " + redisDatabaseLabel(addr, rec.RedisDB, false))
	}
}

func mountRecordProductMode(rec mountRecord) string {
	mode := strings.TrimSpace(rec.ProductMode)
	if mode != "" {
		return mode
	}
	if strings.TrimSpace(rec.ControlPlaneURL) != "" {
		return productModeSelfHosted
	}
	return productModeLocal
}

func configPathLabel() string {
	return clr(ansiGray, compactDisplayPath(configPath()))
}

func statusConfigPathLabel() string {
	path := configPath()
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return clr(ansiGray, homeRelativeDisplayPath(path))
}

func homeRelativeDisplayPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return path
	}
	home, err := os.UserHomeDir()
	cleanPath := filepath.Clean(path)
	if err == nil {
		home = filepath.Clean(home)
		if cleanPath == home {
			return "~"
		}
		rel, err := filepath.Rel(home, cleanPath)
		if err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".." {
			return filepath.Join("~", rel)
		}
	}
	if macHome, ok := macUserHomeRelativeDisplayPath(cleanPath); ok {
		return macHome
	}
	return path
}

func macUserHomeRelativeDisplayPath(path string) (string, bool) {
	slashPath := filepath.ToSlash(path)
	if !strings.HasPrefix(slashPath, "/Users/") {
		return "", false
	}
	rest := strings.TrimPrefix(slashPath, "/Users/")
	user, rel, hasRel := strings.Cut(rest, "/")
	if user == "" {
		return "", false
	}
	if !hasRel || rel == "" {
		return "~", true
	}
	return filepath.Join("~", filepath.FromSlash(rel)), true
}

// appendConfigRows adds user-facing config metadata rows without overloading
// "config" to mean both the configuration source and the config file path.
func appendConfigRows(rows []outputRow, cfg config) []outputRow {
	if row := configSourceStatusRow(cfg); row.Label != "" {
		rows = append(rows, row)
	}
	return append(rows, outputRow{Label: "config file", Value: configPathLabel()})
}

func configSourceStatusRow(cfg config) outputRow {
	productMode, err := effectiveProductMode(cfg)
	if err != nil {
		return outputRow{}
	}

	if productMode == productModeLocal {
		return outputRow{Label: "config source", Value: productModeDisplayLabel(productMode)}
	}

	value := strings.TrimSpace(cfg.URL)
	if value == "" {
		value = "<control plane url not configured>"
	}
	return outputRow{Label: "control plane", Value: value}
}

func statusDisplayRows(cfg config, rows []outputRow) []outputRow {
	ordered := make([]outputRow, 0, len(rows)+2)
	if row := configSourceStatusRow(cfg); row.Label != "" {
		ordered = append(ordered, row)
	}

	var databaseRows []outputRow
	for _, row := range rows {
		if row.Label == "database" {
			databaseRows = append(databaseRows, row)
			continue
		}
		ordered = append(ordered, row)
	}
	ordered = append(ordered, outputRow{Label: "config file", Value: statusConfigPathLabel()})
	ordered = append(ordered, databaseRows...)
	return ordered
}

func commandContextRows(cfg config, workspace string) []outputRow {
	rows := make([]outputRow, 0, 2)
	if strings.TrimSpace(workspace) != "" {
		rows = append(rows, outputRow{Label: "workspace", Value: workspace})
	}
	rows = append(rows, outputRow{Label: "database", Value: configRemoteLabel(cfg)})
	return rows
}

func statusTitle(prefix string, pid int) string {

	if pid > 0 {
		return prefix + " " + clr(ansiBold, fmt.Sprintf("AFS Running (PID: %d)", pid))
	}
	return prefix + " " + clr(ansiBold, "AFS Running")
}

func statusNotRunningTitle() string {
	return clr(ansiDim, "○") + " " + clr(ansiBold, "AFS Not Running")
}

func localSurfacePath(cfg config) string {
	return cfg.LocalPath
}

// statusRows returns the consistent core rows. Sync mode no longer reports
// saved workspace/local values because per-workspace mount rows carry the
// live local paths.
// Mount backend is included only for FUSE/NFS. In cloud mode the database row
// shows the cloud database id instead of the local Redis endpoint so users see
// the database they're actually talking to.
func statusRows(cfg config, workspace, localPath, mode, backendName, redisAddr string, redisDB int) []outputRow {
	var rows []outputRow
	if mode != modeSync {
		if ws := strings.TrimSpace(workspace); ws != "" {
			rows = append(rows, outputRow{Label: "workspace", Value: ws})
		}
		if localPath != "" {
			rows = append(rows, outputRow{Label: "local", Value: homeRelativeDisplayPath(localPath)})
		}
	}
	rows = append(rows, outputRow{Label: "database", Value: databaseStatusLabel(cfg, redisAddr, redisDB)})
	if strings.TrimSpace(mode) != "" {
		rows = append(rows, outputRow{Label: "mode", Value: mode})
	}
	if backendName != "" && backendName != mountBackendNone {
		rows = append(rows, outputRow{Label: "mount backend", Value: userModeLabel(backendName)})
	}
	if account := strings.TrimSpace(cfg.Account); account != "" {
		if productMode, _ := effectiveProductMode(cfg); productMode == productModeCloud {
			rows = append(rows, outputRow{Label: "account", Value: account})
		}
	}
	return rows
}

func runtimeStatusRows(cfg config, workspace, localPath, mode, backendName, redisAddr string, redisDB int, running bool) []outputRow {
	rows := statusRows(cfg, workspace, localPath, mode, backendName, redisAddr, redisDB)
	if running {
		return rows
	}
	filtered := make([]outputRow, 0, len(rows)+1)
	if ws := strings.TrimSpace(workspace); ws != "" {
		filtered = append(filtered, outputRow{Label: "workspace", Value: ws})
	}
	for _, row := range rows {
		switch row.Label {
		case "workspace", "local", "mode", "mount backend":
			continue
		default:
			filtered = append(filtered, row)
		}
	}
	return filtered
}

// databaseStatusLabel picks the right label for the "database" row: in cloud
// mode the friendly database id (e.g. "afs-cloud"), otherwise the local
// Redis endpoint.
func databaseStatusLabel(cfg config, redisAddr string, redisDB int) string {
	productMode, _ := effectiveProductMode(cfg)
	if productMode == productModeCloud {
		if id := strings.TrimSpace(cfg.DatabaseID); id != "" {
			return id
		}
	}
	return statusRemoteLabel(redisAddr, redisDB)
}

// statusTitleForAlive returns daemon state with an optional PID.
func statusTitleForAlive(alive bool, pid int) string {
	if alive {
		return statusTitle(markerSuccess, pid)
	}
	return statusNotRunningTitle()
}

// appendUptimeRows appends the uptime row and, if set, the readonly row.
func appendUptimeRows(rows []outputRow, st state) []outputRow {
	rows = append(rows, outputRow{Label: "uptime", Value: formatDuration(time.Since(st.StartedAt))})
	if st.ReadOnly {
		rows = append(rows, outputRow{Label: "readonly", Value: "yes"})
	}
	return rows
}

func appendConnectedAgentRows(rows []outputRow, cfg config, st state) []outputRow {
	if strings.TrimSpace(st.SessionID) == "" {
		return rows
	}
	if id := strings.TrimSpace(cfg.ID); id != "" {
		rows = append(rows, outputRow{Label: "agent id", Value: id})
	}
	return rows
}

// loadSyncStateForStatus loads the sync snapshot for a workspace, returning
// nil if the workspace is empty or the state cannot be loaded.
func loadSyncStateForStatus(workspace string) *SyncState {
	if workspace == "" {
		return nil
	}
	st, err := loadSyncState(workspace)
	if err != nil {
		return nil
	}
	return st
}

func currentWorkspaceLabel(workspace string) string {
	if strings.TrimSpace(workspace) == "" {
		return "none"
	}
	return workspace
}

// cmdStatus dispatches to the status renderer for the current local runtime:
// no state, sync mount, or live mount.
type statusOptions struct {
	verbose bool
}

var statusSyncDaemonPIDs = currentConfigSyncDaemonPIDs

func cmdStatusArgs(args []string) error {
	if len(args) > 0 && isHelpArg(args[0]) {
		fmt.Fprint(os.Stderr, statusUsageText(filepath.Base(os.Args[0])))
		return nil
	}
	opts, err := parseStatusOptions(args)
	if err != nil {
		return err
	}
	return cmdStatusWithOptions(opts)
}

func parseStatusOptions(args []string) (statusOptions, error) {
	var opts statusOptions
	for _, arg := range args {
		switch arg {
		case "--verbose", "-v":
			opts.verbose = true
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown flag %q\n\n%s", arg, statusUsageText(filepath.Base(os.Args[0])))
			}
			return opts, fmt.Errorf("%s", statusUsageText(filepath.Base(os.Args[0])))
		}
	}
	return opts, nil
}

func cmdStatus() error {
	return cmdStatusWithOptions(statusOptions{})
}

func cmdStatusWithOptions(opts statusOptions) error {
	reg, regErr := loadMountRegistry()
	hasMountRecords := regErr == nil && len(reg.Mounts) > 0
	processPIDs, _ := statusSyncDaemonPIDs()
	st, err := loadState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if hasMountRecords {
				if err := cmdStatusFromMountRegistry(reg, processPIDs); err != nil {
					return err
				}
			} else if len(processPIDs) > 0 {
				if err := cmdStatusFromSyncDaemons(processPIDs); err != nil {
					return err
				}
			} else if err := cmdStatusNotRunning(); err != nil {
				return err
			}
		} else {
			return err
		}
	} else if strings.TrimSpace(st.Mode) == modeSync || (strings.TrimSpace(st.Mode) == "" && st.SyncPID > 0) {
		cmdStatusSync(st, processPIDs, hasMountRecords)
	} else if err := cmdStatusMount(st, hasMountRecords); err != nil {
		return err
	}

	if hasMountRecords {
		printMountStatus(reg, opts.verbose)
	}
	return nil
}

func cmdStatusFromMountRegistry(reg mountRegistry, processPIDs []int) error {
	cfg := loadConfigOrDefault()
	if err := resolveConfigPaths(&cfg); err != nil {
		cfg.WorkRoot = defaultWorkRoot()
	}
	backendName := cfg.MountBackend
	if backendName == "" {
		backendName = mountBackendNone
	}
	mode, _ := effectiveMode(cfg)

	mountPIDs := runningMountPIDs(reg.Mounts)
	runningPIDs := uniqueSortedPIDs(append(append([]int{}, mountPIDs...), processPIDs...))
	title := statusTitleForPIDs(runningPIDs)

	rows := statusDisplayRows(cfg, runtimeStatusRows(cfg, cfg.CurrentWorkspace, localSurfacePath(cfg), mode, backendName, cfg.RedisAddr, cfg.RedisDB, len(runningPIDs) > 0))
	rows = omitMountedWorkspaceSummaryRows(rows)
	if unmanaged := unmanagedSyncDaemonPIDs(processPIDs, mountPIDs); len(unmanaged) > 0 {
		rows = append(rows, outputRow{Label: "unmanaged daemons", Value: pidsLabel(unmanaged)})
	}
	rows = appendAuthStatusRows(rows)
	printSection(title, rows)
	return nil
}

func omitMountedWorkspaceSummaryRows(rows []outputRow) []outputRow {
	filtered := make([]outputRow, 0, len(rows))
	for _, row := range rows {
		switch row.Label {
		case "workspace", "local", "mode":
			continue
		default:
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func cmdStatusFromSyncDaemons(pids []int) error {
	cfg := loadConfigOrDefault()
	if err := resolveConfigPaths(&cfg); err != nil {
		cfg.WorkRoot = defaultWorkRoot()
	}
	title := statusTitleForPIDs(pids)
	rows := statusDisplayRows(cfg, runtimeStatusRows(cfg, cfg.CurrentWorkspace, localSurfacePath(cfg), modeSync, "", cfg.RedisAddr, cfg.RedisDB, len(pids) > 0))
	if ws := strings.TrimSpace(cfg.CurrentWorkspace); ws != "" {
		rows = append([]outputRow{{Label: "workspace", Value: ws}}, rows...)
	}
	rows = append(rows, outputRow{Label: "daemon", Value: daemonStatusSummary(pids)})
	rows = append(rows, outputRow{Label: "unmanaged daemons", Value: pidsLabel(pids)})
	rows = appendAuthStatusRows(rows)
	printSection(title, rows)
	return nil
}

func printMountStatus(reg mountRegistry, verbose bool) {
	if len(reg.Mounts) == 0 {
		fmt.Println()
		fmt.Println("No mounted workspaces.")
		fmt.Println()
		return
	}
	workspaces := mountStatusItems(reg.Mounts)
	running, stopped := splitMountStatusItems(workspaces)
	if len(running) > 0 {
		fmt.Print("\nMounted workspaces\n\n")
		printPlainTable(mountStatusItemHeaders(running, "Workspace"), mountStatusItemRows(running))
	} else {
		fmt.Println("\nNo mounted workspaces.")
	}
	if len(stopped) > 0 {
		fmt.Print("\nStopped workspace records\n\n")
		printPlainTable(mountStatusItemHeaders(stopped, "Workspace"), mountStatusItemRows(stopped))
	}
	if verbose {
		for _, item := range append(running, stopped...) {
			fmt.Println()
			printMountStatusItemVerbose(item)
		}
	}
	fmt.Println()

}

type mountStatusItem struct {
	Name                 string
	Status               string
	Mode                 string
	ProductMode          string
	ControlPlaneURL      string
	ControlPlaneDatabase string
	RedisAddr            string
	RedisDB              int
	Path                 string
	Records              []mountRecord
}

func mountStatusItems(records []mountRecord) []mountStatusItem {
	items := make([]mountStatusItem, 0, len(records))
	for _, rec := range sortedMountRecords(records) {
		items = append(items, mountStatusItemFromRecords([]mountRecord{rec}, rec.Workspace, rec.LocalPath))
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := strings.ToLower(items[i].Name), strings.ToLower(items[j].Name)
		if left == right {
			return items[i].Path < items[j].Path
		}
		return left < right
	})
	return items
}

func agentWorkspaceMountKey(rec mountRecord) string {
	root := strings.TrimSpace(rec.AgentWorkspaceRoot)
	name := strings.TrimSpace(rec.AgentWorkspace)
	id := strings.TrimSpace(rec.AgentWorkspaceID)
	if root == "" || (name == "" && id == "") {
		return ""
	}
	return id + "\x00" + name + "\x00" + filepath.Clean(root)
}

func mountStatusItemFromRecords(records []mountRecord, name, path string) mountStatusItem {
	item := mountStatusItem{
		Name:    strings.TrimSpace(name),
		Status:  mountStatusForRecords(records),
		Mode:    commonMountRecordValue(records, func(rec mountRecord) string { return rec.Mode }, "mixed"),
		Path:    filepath.Clean(path),
		Records: append([]mountRecord(nil), records...),
	}
	if item.Mode == "" {
		item.Mode = "unknown"
	}
	item.ProductMode = commonMountRecordValue(records, func(rec mountRecord) string { return rec.ProductMode }, "")
	item.ControlPlaneURL = commonMountRecordValue(records, func(rec mountRecord) string { return rec.ControlPlaneURL }, "")
	item.ControlPlaneDatabase = commonMountRecordValue(records, func(rec mountRecord) string { return rec.ControlPlaneDatabase }, "")
	item.RedisAddr = commonMountRecordValue(records, func(rec mountRecord) string { return rec.RedisAddr }, "")
	item.RedisDB = commonMountRecordDB(records)
	if item.Name == "" && len(records) > 0 {
		item.Name = records[0].Workspace
	}
	return item
}

func mountStatusForRecords(records []mountRecord) string {
	running := 0
	stopped := 0
	for _, rec := range records {
		if mountStatus(rec) == "running" {
			running++
			continue
		}
		stopped++
	}
	switch {
	case running > 0 && stopped > 0:
		return "partial"
	case running > 0:
		return "running"
	default:
		return "stopped"
	}
}

func commonMountRecordValue(records []mountRecord, value func(mountRecord) string, mixed string) string {
	common := ""
	for _, rec := range records {
		current := strings.TrimSpace(value(rec))
		if current == "" {
			continue
		}
		if common == "" {
			common = current
			continue
		}
		if common != current {
			return mixed
		}
	}
	return common
}

func commonMountRecordDB(records []mountRecord) int {
	if len(records) == 0 {
		return 0
	}
	common := records[0].RedisDB
	for _, rec := range records[1:] {
		if rec.RedisDB != common {
			return 0
		}
	}
	return common
}

func splitMountStatusItems(items []mountStatusItem) (running []mountStatusItem, stopped []mountStatusItem) {
	for _, item := range items {
		if item.Status == "running" || item.Status == "partial" {
			running = append(running, item)
			continue
		}
		stopped = append(stopped, item)
	}
	return running, stopped
}

func sortedMountRecords(records []mountRecord) []mountRecord {
	mounts := append([]mountRecord(nil), records...)
	sort.SliceStable(mounts, func(i, j int) bool {
		left := strings.ToLower(mounts[i].Workspace)
		right := strings.ToLower(mounts[j].Workspace)
		if left == right {
			return mounts[i].LocalPath < mounts[j].LocalPath
		}
		return left < right
	})
	return mounts
}

func splitMountRecords(records []mountRecord) (running []mountRecord, stopped []mountRecord) {
	for _, rec := range sortedMountRecords(records) {
		if mountStatus(rec) == "running" {
			running = append(running, rec)
			continue
		}
		stopped = append(stopped, rec)
	}
	return running, stopped
}

func runningMountPIDs(mounts []mountRecord) []int {
	seen := map[int]struct{}{}
	var pids []int
	for _, rec := range mounts {
		if rec.PID <= 0 || !processAlive(rec.PID) {
			continue
		}
		if _, ok := seen[rec.PID]; ok {
			continue
		}
		seen[rec.PID] = struct{}{}
		pids = append(pids, rec.PID)
	}
	sort.Ints(pids)
	return pids
}

func syncDaemonPIDs() ([]int, error) {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	return parseSyncDaemonPIDs(string(out)), nil
}

func currentConfigSyncDaemonPIDs() ([]int, error) {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	return parseSyncDaemonPIDsForConfig(string(out), configPath()), nil
}

func parseSyncDaemonPIDs(psOutput string) []int {
	return parseSyncDaemonPIDsForConfig(psOutput, "")
}

func parseSyncDaemonPIDsForConfig(psOutput, configFile string) []int {
	var pids []int
	for _, rawLine := range strings.Split(psOutput, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		executable := fields[1]
		if filepath.Base(executable) != "afs" {
			continue
		}
		command := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		if !strings.Contains(command, "_sync-daemon") {
			continue
		}
		if configFile != "" {
			daemonConfig := syncDaemonConfigPath(strings.Fields(command))
			if daemonConfig == "" {
				daemonConfig = defaultSyncDaemonConfigPath(executable)
			}
			if !sameConfigPath(daemonConfig, configFile) {
				continue
			}
		}
		pids = append(pids, pid)
	}
	return uniqueSortedPIDs(pids)
}

func defaultSyncDaemonConfigPath(executable string) string {
	if strings.TrimSpace(executable) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(executable), "afs.config.json")
}

func sameConfigPath(a, b string) bool {
	return cleanConfigPath(a) == cleanConfigPath(b)
}

func cleanConfigPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func syncDaemonConfigPath(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 < len(args) {
				return args[i+1]
			}
		default:
			if strings.HasPrefix(args[i], "--config=") {
				return strings.TrimPrefix(args[i], "--config=")
			}
		}
	}
	return ""
}

func unmanagedSyncDaemonPIDs(processPIDs, managedPIDs []int) []int {
	managed := make(map[int]struct{}, len(managedPIDs))
	for _, pid := range managedPIDs {
		managed[pid] = struct{}{}
	}
	var unmanaged []int
	for _, pid := range processPIDs {
		if _, ok := managed[pid]; ok {
			continue
		}
		unmanaged = append(unmanaged, pid)
	}
	return uniqueSortedPIDs(unmanaged)
}

func uniqueSortedPIDs(pids []int) []int {
	seen := make(map[int]struct{}, len(pids))
	unique := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		unique = append(unique, pid)
	}
	sort.Ints(unique)
	return unique
}

func statusTitleForPIDs(pids []int) string {
	switch len(pids) {
	case 0:
		return statusNotRunningTitle()
	case 1:
		return statusTitle(markerSuccess, pids[0])
	default:
		parts := make([]string, 0, len(pids))
		for _, pid := range pids {
			parts = append(parts, fmt.Sprintf("%d", pid))
		}
		return markerSuccess + " " + clr(ansiBold, fmt.Sprintf("AFS Running (PIDs: %s)", strings.Join(parts, ", ")))
	}
}

func pidsLabel(pids []int) string {
	parts := make([]string, 0, len(pids))
	for _, pid := range pids {
		parts = append(parts, fmt.Sprintf("%d", pid))
	}
	return strings.Join(parts, ", ")
}

func daemonStatusSummary(pids []int) string {
	switch len(pids) {
	case 0:
		return "not running"
	case 1:
		return fmt.Sprintf("running (PID: %d)", pids[0])
	default:
		return fmt.Sprintf("running (%d daemons)", len(pids))
	}
}

func mountStatusItemHeaders(items []mountStatusItem, nameHeader string) []string {
	if strings.TrimSpace(nameHeader) == "" {
		nameHeader = "Workspace"
	}
	headers := []string{nameHeader, "Status", "Mode"}
	if mountStatusItemShowsScope(items) {
		headers = append(headers, "Source", "Database")
	}
	return append(headers, "Path")
}

func mountStatusItemRows(items []mountStatusItem) [][]string {
	rows := make([][]string, 0, len(items))
	showScope := mountStatusItemShowsScope(items)
	for _, item := range items {
		mode := strings.TrimSpace(item.Mode)
		if mode == "" {
			mode = "unknown"
		}
		row := []string{item.Name, item.Status, mode}
		if showScope {
			row = append(row, mountStatusItemSource(item), mountStatusItemDatabase(item))
		}
		row = append(row, homeRelativeDisplayPath(item.Path))
		rows = append(rows, row)
	}
	return rows
}

func mountStatusItemShowsScope(items []mountStatusItem) bool {
	for _, item := range items {
		if strings.TrimSpace(item.ProductMode) != "" || strings.TrimSpace(item.ControlPlaneURL) != "" || strings.TrimSpace(item.ControlPlaneDatabase) != "" {
			return true
		}
	}
	return false
}

func mountStatusItemSource(item mountStatusItem) string {
	return productModeDisplayLabel(mountRecordProductMode(mountRecord{ProductMode: item.ProductMode, ControlPlaneURL: item.ControlPlaneURL}))
}

func mountStatusItemDatabase(item mountStatusItem) string {
	if db := strings.TrimSpace(item.ControlPlaneDatabase); db != "" {
		return db
	}
	if addr := strings.TrimSpace(item.RedisAddr); addr != "" {
		return redisDatabaseLabel(addr, item.RedisDB, false)
	}
	return "-"
}

func printMountVerbose(rec mountRecord) {
	rows := []outputRow{
		{Label: "workspace", Value: rec.Workspace},
		{Label: "status", Value: mountStatus(rec)},
		{Label: "mode", Value: fallbackString(rec.Mode, "unknown")},
		{Label: "path", Value: homeRelativeDisplayPath(rec.LocalPath)},
	}
	if rec.PID > 0 {
		rows = append(rows, outputRow{Label: "pid", Value: fmt.Sprintf("%d", rec.PID)})
	}
	productMode := strings.TrimSpace(rec.ProductMode)
	if productMode != "" {
		rows = append(rows, outputRow{Label: "config source", Value: productModeDisplayLabel(productMode)})
	}
	if controlPlaneURL := strings.TrimSpace(rec.ControlPlaneURL); controlPlaneURL != "" {
		rows = append(rows, outputRow{Label: "control plane", Value: controlPlaneURL})
		if db := strings.TrimSpace(rec.ControlPlaneDatabase); db != "" {
			rows = append(rows, outputRow{Label: "database", Value: db})
		}
	} else if redisAddr := strings.TrimSpace(rec.RedisAddr); redisAddr != "" {
		rows = append(rows, outputRow{Label: "redis", Value: redisDatabaseLabel(redisAddr, rec.RedisDB, false)})
	}
	if sessionID := strings.TrimSpace(rec.SessionID); sessionID != "" {
		rows = append(rows, outputRow{Label: "session", Value: sessionID})
	}
	if mountID := strings.TrimSpace(rec.ID); mountID != "" {
		rows = append(rows, outputRow{Label: "mount", Value: mountID})
	}
	if !rec.StartedAt.IsZero() {
		rows = append(rows, outputRow{Label: "started", Value: formatDisplayTimestamp(rec.StartedAt.UTC().Format(time.RFC3339))})
	}

	fmt.Printf("\n")
	title := plainOutputTitle(rec.Workspace)
	if title != "" {
		fmt.Println(title)
	}
	fmt.Printf("\n")

	maxLabel := 0
	for _, r := range rows {
		if r.Label == "mount" {
			continue
		}
		if w := runeWidth(stripAnsi(r.Label)); w > maxLabel {
			maxLabel = w
		}
	}
	if maxLabel == 0 {
		maxLabel = runeWidth("mount")
	}

	for _, r := range rows {
		label := stripAnsi(strings.TrimSpace(r.Label))
		value := stripAnsi(strings.TrimSpace(r.Value))
		width := maxLabel
		if label == "mount" {
			width = len("database")
		}
		fmt.Printf("%s  %s\n", padVisibleText(label, width), value)
	}
	fmt.Printf("\n")
}

func printMountStatusItemVerbose(item mountStatusItem) {
	for _, rec := range item.Records {
		printMountVerbose(rec)
	}
}

func fallbackString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

// cmdStatusNotRunning renders status when no state file exists.
func cmdStatusNotRunning() error {
	cfg := loadConfigOrDefault()
	if err := resolveConfigPaths(&cfg); err != nil {
		cfg.WorkRoot = defaultWorkRoot()
	}
	backendName := cfg.MountBackend
	if backendName == "" {
		backendName = mountBackendNone
	}
	mode, _ := effectiveMode(cfg)

	title := statusNotRunningTitle()
	rows := statusDisplayRows(cfg, runtimeStatusRows(cfg, cfg.CurrentWorkspace, localSurfacePath(cfg), mode, backendName, cfg.RedisAddr, cfg.RedisDB, false))
	rows = appendAuthStatusRows(rows)
	printSection(title, rows)
	return nil
}

// cmdStatusSync renders status for a running sync daemon.
func cmdStatusSync(st state, processPIDs []int, hasMountRecords bool) {
	alive := st.SyncPID > 0 && processAlive(st.SyncPID)
	cfg := loadConfigOrDefault()
	if err := resolveConfigPaths(&cfg); err != nil {
		cfg.WorkRoot = defaultWorkRoot()
	}
	title := statusTitleForAlive(alive, st.SyncPID)
	rows := statusDisplayRows(cfg, runtimeStatusRows(cfg, st.CurrentWorkspace, st.LocalPath, modeSync, "", st.RedisAddr, st.RedisDB, alive))
	if ws := strings.TrimSpace(st.CurrentWorkspace); ws != "" && !hasMountRecords {
		rows = append([]outputRow{{Label: "workspace", Value: ws}}, rows...)
	}
	managedPIDs := []int{}
	if alive {
		managedPIDs = append(managedPIDs, st.SyncPID)
	}
	if unmanaged := unmanagedSyncDaemonPIDs(processPIDs, managedPIDs); len(unmanaged) > 0 {
		rows = append(rows, outputRow{Label: "unmanaged daemons", Value: pidsLabel(unmanaged)})
	}
	rows = appendAuthStatusRows(rows)
	rows = appendConnectedAgentRows(rows, cfg, st)
	rows = appendUptimeRows(rows, st)
	if snap := loadSyncStateForStatus(st.CurrentWorkspace); snap != nil {
		live, deleted := syncStateEntryCounts(snap)
		value := fmt.Sprintf("%d", live)
		if deleted > 0 {
			value = fmt.Sprintf("%d live, %d deleted", live, deleted)
		}
		rows = append(rows, outputRow{Label: "entries", Value: value})
		if !snap.UpdatedAt.IsZero() {
			rows = append(rows, outputRow{Label: "last sync", Value: relativeTime(snap.UpdatedAt)})
		}
	}
	printSection(title, rows)
}

// cmdStatusMount renders status for mount mode.
func cmdStatusMount(st state, hasMountRecords bool) error {
	backend, backendName, err := backendForState(st)
	if err != nil {
		return err
	}
	cfg := loadConfigOrDefault()
	if err := resolveConfigPaths(&cfg); err != nil {
		cfg.WorkRoot = defaultWorkRoot()
	}

	workspace := cfg.CurrentWorkspace
	if ws := strings.TrimSpace(st.CurrentWorkspace); ws != "" {
		workspace = ws
	}
	localPath := localSurfacePath(cfg)
	if backendName != mountBackendNone && strings.TrimSpace(st.LocalPath) != "" {
		localPath = st.LocalPath
	}

	alive := false
	if backendName != mountBackendNone {
		alive = backend.IsMounted(st.LocalPath) && st.MountPID > 0 && processAlive(st.MountPID)
	}
	title := statusTitleForAlive(alive, st.MountPID)

	rows := statusDisplayRows(cfg, runtimeStatusRows(cfg, workspace, localPath, modeMount, backendName, st.RedisAddr, st.RedisDB, alive))
	if hasMountRecords {
		rows = omitMountedWorkspaceSummaryRows(rows)
	}
	rows = appendAuthStatusRows(rows)
	rows = appendConnectedAgentRows(rows, cfg, st)
	rows = appendUptimeRows(rows, st)
	if st.ArchivePath != "" {
		rows = append(rows, outputRow{Label: "archive", Value: homeRelativeDisplayPath(st.ArchivePath)})
	}
	printSection(title, rows)
	return nil
}

// relativeTime renders a timestamp like "12s ago", "5m ago", "3h ago", or
// "2d ago". Past times only — future or zero return absolute fallback.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	if d < 0 {
		return t.UTC().Format(time.RFC3339)
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours())/24)
	}
}

func printReadyBox(cfg config, backendName, _ string) {
	localPath := localSurfacePath(cfg)
	mode, _ := effectiveMode(cfg)
	mounted := backendName != mountBackendNone
	if mounted {
		mode = modeMount
	}
	title := statusTitle(markerSuccess, 0)
	if !mounted {
		title = statusTitle(clr(ansiYellow, "○"), 0)
	}
	rows := statusRows(cfg, cfg.CurrentWorkspace, localPath, mode, backendName, cfg.RedisAddr, cfg.RedisDB)

	if cfg.ReadOnly {
		rows = append(rows, outputRow{Label: "readonly", Value: "yes"})
	}
	if backendName == mountBackendNone {
		rows = append(rows, outputRow{})
		rows = append(rows, outputRow{Label: "create", Value: clr(ansiOrange, filepath.Base(os.Args[0])+" ws create <workspace>")})
		rows = append(rows, outputRow{Label: "import", Value: clr(ansiOrange, filepath.Base(os.Args[0])+" ws import <workspace> <directory>")})
		printSection(title, rows)
		return
	}
	rows = append(rows, outputRow{})
	rows = append(rows, outputRow{Label: "try", Value: clr(ansiOrange, "ls "+shellQuote(localPath))})
	rows = append(rows, outputRow{Label: "unmount", Value: clr(ansiOrange, filepath.Base(os.Args[0])+" ws unmount "+shellQuote(localPath))})
	printSection(title, rows)
}
