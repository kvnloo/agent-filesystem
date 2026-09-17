package controlplane

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	afsclient "github.com/redis/agent-filesystem/mount/client"
	"github.com/redis/go-redis/v9"
)

// Opt-in integration uses a separately built current AFS CLI. Everything it
// creates, including Redis, config, registry and local mounts, is disposable.
func TestCurrentAFSClientInteroperability(t *testing.T) {
	binary := os.Getenv("AFS_CURRENT_CLI")
	if binary == "" {
		t.Skip("set AFS_CURRENT_CLI to a current AFS binary built outside the reference checkout")
	}
	server, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server is unavailable")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	redisProcess := exec.CommandContext(ctx, server, "--bind", "127.0.0.1", "--port", strconv.Itoa(port), "--save", "", "--appendonly", "no", "--dir", root)
	if err := redisProcess.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redisProcess.Process.Kill(); _ = redisProcess.Wait() })
	rdb := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("127.0.0.1:%d", port)})
	t.Cleanup(func() { _ = rdb.Close() })
	waitStorageCondition(t, ctx, func() bool { return rdb.Ping(ctx).Err() == nil }, "Redis startup")
	config := filepath.Join(root, "config.json")
	if err := os.WriteFile(config, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "AFS_STATE_DIR="+filepath.Join(root, "state"), "AFS_REDIS_URL=", "AFS_REDIS_PASSWORD=")
	args := []string{"--config", config, "--redis", fmt.Sprintf("redis://127.0.0.1:%d/0", port), "--json"}
	run := func(command ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, append(append([]string{}, args...), command...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("current AFS %v: %v\n%s", command, err, out)
		}
		return out
	}
	s := NewService(Config{}, NewStore(rdb))
	detail, err := s.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "bridge", Description: "rich metadata", DatabaseID: "keep-db", Region: "keep-region"})
	if err != nil {
		t.Fatal(err)
	}
	id := detail.ID
	old := afsclient.New(rdb, id)
	if err := old.Echo(ctx, "/note.txt", []byte("checkpoint bytes")); err != nil {
		t.Fatal(err)
	}
	if saved, err := s.SaveCheckpointFromLive(ctx, id, "saved"); err != nil || !saved {
		t.Fatalf("save: %v %v", saved, err)
	}
	if !bytes.Contains(run("info", "bridge"), []byte(id)) {
		t.Fatal("current client did not preserve control-plane ID")
	}
	run("cp", "list", "bridge")
	mountDir := filepath.Join(root, "mount")
	mount := exec.CommandContext(ctx, binary, append(append([]string{}, args...), "mount", "bridge", mountDir, "--foreground")...)
	mount.Env = env
	var mountLog lockedStorageBuffer
	mount.Stdout = &mountLog
	mount.Stderr = &mountLog
	if err := mount.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- mount.Wait() }()
	t.Cleanup(func() {
		_ = mount.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(time.Second):
			_ = mount.Process.Kill()
			<-done
		}
	})
	waitStorageCondition(t, ctx, func() bool {
		b, e := os.ReadFile(filepath.Join(mountDir, "note.txt"))
		return e == nil && string(b) == "checkpoint bytes"
	}, "current AFS mount hydration")
	if err := os.WriteFile(filepath.Join(mountDir, "note.txt"), []byte("current client edit"), 0600); err != nil {
		t.Fatal(err)
	}
	waitStorageCondition(t, ctx, func() bool { b, e := old.Cat(ctx, "/note.txt"); return e == nil && string(b) == "current client edit" }, "current AFS publication")
	observed, err := old.Stat(ctx, "/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mountDir, "note.txt"), []byte("new current edit"), 0600); err != nil {
		t.Fatal(err)
	}
	waitStorageCondition(t, ctx, func() bool { b, e := old.Cat(ctx, "/note.txt"); return e == nil && string(b) == "new current edit" }, "second current AFS publication")
	if err := old.Echo(afsclient.WithExpectedStat(ctx, observed), "/note.txt", []byte("stale server candidate")); !errors.Is(err, afsclient.ErrWriteConflict) {
		t.Fatalf("server ignored current client revision: %v", err)
	}
	restored, err := s.RestoreCheckpointWithResult(ctx, id, "saved")
	if err != nil || !restored.SafetyCheckpointCreated {
		t.Fatalf("server restore: %+v %v", restored, err)
	}
	if err := old.Echo(ctx, "/note.txt", []byte("stale server")); !errors.Is(err, afsclient.ErrWorkspaceChanged) {
		t.Fatalf("server ignored restore generation: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mountDir, "note.txt"), []byte("stale local edit"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(350 * time.Millisecond)
	fresh := afsclient.New(rdb, id)
	b, err := fresh.Cat(ctx, "/note.txt")
	if err != nil || string(b) != "checkpoint bytes" {
		t.Fatalf("current client bypassed restore fence: %q %v; mount log=%s", b, err, mountLog.String())
	}
	run("unmount", mountDir, "--force")
	if err := fresh.Echo(ctx, "/note.txt", []byte("server edit")); err != nil {
		t.Fatal(err)
	}
	run("cp", "create", "bridge", "--name", "from-current")
	meta, err := s.store.GetWorkspaceMeta(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != id || meta.DatabaseID != "keep-db" || meta.Region != "keep-region" {
		t.Fatalf("current checkpoint dropped rich metadata: %+v", meta)
	}
	run("cp", "restore", "bridge", "saved", "--yes")
	if err := fresh.Echo(ctx, "/note.txt", []byte("stale after current restore")); !errors.Is(err, afsclient.ErrWorkspaceChanged) {
		t.Fatalf("current restore did not fence server: %v", err)
	}
	if err := s.ForkWorkspace(ctx, id, "forked"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(run("list")), "forked") {
		t.Fatal("current client missed server fork")
	}
	run("delete", "forked", "--yes")
	if _, err := s.store.GetWorkspaceMeta(ctx, "forked"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("server did not observe current deletion: %v", err)
	}
}

func waitStorageCondition(t *testing.T, ctx context.Context, condition func() bool, label string) {
	t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) && ctx.Err() == nil {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", label)
}

type lockedStorageBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedStorageBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
func (b *lockedStorageBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}
