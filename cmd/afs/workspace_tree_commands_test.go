package main

import (
	"strings"
	"testing"
)

func TestWorkspaceCompositionCommandsAreRemoved(t *testing.T) {
	for _, command := range []string{"create-manifest", "list-manifests", "show-manifest", "add", "attach", "detach", "bookmark", "restore-bookmark"} {
		err := cmdWorkspace([]string{"ws", command})
		if err == nil || !strings.Contains(err.Error(), "unknown workspace subcommand") {
			t.Fatalf("ws %s: expected retired command rejection, got %v", command, err)
		}
	}
}

func TestWorkspaceHelpDescribesOneTreeCommands(t *testing.T) {
	help := workspaceUsageText("afs")
	for _, command := range []string{"import", "mount", "unmount", "save", "fork", "delete", "config"} {
		if !strings.Contains(help, command) {
			t.Fatalf("missing command %s in %s", command, help)
		}
	}
	for _, removed := range []string{"volume", "manifest", "attach", "bookmark"} {
		if strings.Contains(help, removed) {
			t.Fatalf("obsolete concept %s in %s", removed, help)
		}
	}
}
