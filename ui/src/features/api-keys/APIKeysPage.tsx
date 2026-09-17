import { useNavigate } from "@tanstack/react-router";
import { Button, Loader } from "@redis-ui/components";
import { useMemo, useState } from "react";
import styled from "styled-components";
import {
  DialogError,
  InlineActions,
  NoticeBody,
  NoticeCard,
  NoticeTitle,
  PageStack,
} from "../../components/afs-kit";
import { CreateMCPAccessDialog } from "../agents/CreateMCPAccessDialog";
import { LocalMCPAccessDialog } from "../agents/LocalMCPAccessDialog";
import { AccessTokenEmptyState } from "../../components/access-token-empty-state";
import { useAuthSession } from "../../foundation/auth-context";
import { useDatabaseScope } from "../../foundation/database-scope";
import {
  useAllAPIKeys,
  useDatabases,
  useRevokeCLIAccessTokenMutation,
  useRevokeControlPlaneTokenMutation,
  useRevokeMCPAccessTokenMutation,
  useWorkspaceSummaries,
} from "../../foundation/hooks/use-afs";
import { isControlPlaneScope } from "../../foundation/types/afs";
import type { APIKey } from "../../foundation/types/afs";
import { APIKeysTable } from "../../foundation/tables/access-tokens-table";
import { useDrawerCommands } from "../../foundation/drawer-context";
import type { CommandsDrawerConfig } from "../../foundation/drawer-context";

const COMMANDS: CommandsDrawerConfig = {
  title: "Connect via MCP",
  subline: "Snippets for wiring AFS into MCP-capable agents.",
  sections: [
    {
      title: "Run local stdio MCP",
      description: "Workspace-scoped, runs from your shell.",
      command: "afs mcp --workspace my-workspace --profile workspace-rw",
    },
    {
      title: "Add to Codex CLI",
      description: "Hosted endpoint, bearer key from $AFS_TOKEN.",
      command:
        "codex mcp add agent-filesystem --transport http https://afs.cloud/mcp --bearer-token-env AFS_TOKEN",
    },
    {
      title: "Claude / Cursor config",
      description: "Drop into your client's MCP config file.",
      command: `{
  "mcpServers": {
    "agent-filesystem": {
      "url": "https://afs.cloud/mcp",
      "headers": { "Authorization": "Bearer $AFS_TOKEN" }
    }
  }
}`,
    },
  ],
};

export type APIKeysSearchParams = {
  workspaceId?: string;
  databaseId?: string;
};

type Props = {
  search: APIKeysSearchParams;
  /**
   * Where the "Show all" button navigates when scope filters are cleared.
   * Defaults to `/api-keys`. `/mcp` is supported for backward compat.
   */
  basePath?: "/api-keys" | "/mcp";
};

export function APIKeysPage({ search, basePath = "/api-keys" }: Props) {
  const navigate = useNavigate();
  const auth = useAuthSession();
  useDrawerCommands(COMMANDS);
  const { unavailableDatabases } = useDatabaseScope();
  const queriesEnabled =
    !auth.isLoading && (!auth.config.enabled || auth.isAuthenticated);
  const databasesQuery = useDatabases(queriesEnabled);
  const workspacesQuery = useWorkspaceSummaries(
    search.databaseId ?? null,
    queriesEnabled,
  );
  const apiKeys = useAllAPIKeys(queriesEnabled);
  const revokeWorkspaceToken = useRevokeMCPAccessTokenMutation();
  const revokeControlPlaneToken = useRevokeControlPlaneTokenMutation();
  const revokeCLIToken = useRevokeCLIAccessTokenMutation();

  const [createOpen, setCreateOpen] = useState(false);
  const [localOpen, setLocalOpen] = useState(false);

  const workspaceId = search.workspaceId;
  const databaseId = search.databaseId;

  const workspaceNameById = useMemo(() => {
    const map = new Map<string, string>();
    for (const workspace of workspacesQuery.data ?? []) {
      map.set(workspace.id, workspace.name);
    }
    return map;
  }, [workspacesQuery.data]);
  const databaseNameById = useMemo(
    () =>
      new Map(
        (databasesQuery.data ?? []).map((database) => [database.id, database.name]),
      ),
    [databasesQuery.data],
  );

  const filteredRows = apiKeys.data.filter((key) => {
    if (key.kind === "mcp" && isControlPlaneScope(key.scope)) return true;
    if (workspaceId != null && key.workspaceId !== workspaceId) {
      return false;
    }
    if (databaseId != null && key.databaseId !== databaseId) {
      return false;
    }
    return true;
  });

  const isFiltered = workspaceId != null || databaseId != null;
  const revoking =
    revokeWorkspaceToken.isPending ||
    revokeControlPlaneToken.isPending ||
    revokeCLIToken.isPending;

  async function revoke(key: APIKey) {
    if (key.kind === "cli") {
      await revokeCLIToken.mutateAsync(key.id);
      return;
    }
    if (isControlPlaneScope(key.scope)) {
      await revokeControlPlaneToken.mutateAsync(key.id);
      return;
    }
    await revokeWorkspaceToken.mutateAsync({
      databaseId: key.databaseId,
      workspaceId: key.workspaceId,
      tokenId: key.id,
    });
  }

  if (auth.isLoading) {
    return <Loader data-testid="loader--spinner" />;
  }

  return (
    <PageStack>
      {unavailableDatabases.length > 0 ? (
        <NoticeCard $tone="warning" role="status">
          <NoticeTitle>Some databases are unavailable</NoticeTitle>
          <NoticeBody>
            Results are partial while these databases are disconnected:{" "}
            {unavailableDatabases
              .map((database) => database.displayName || database.databaseName)
              .join(", ")}
            .
          </NoticeBody>
        </NoticeCard>
      ) : null}

      {isFiltered ? (
        <InlineActions>
          <Button
            kind="ghost"
            size="small"
            onClick={() => {
              void navigate({ to: basePath, search: {} });
            }}
          >
            Show all
          </Button>
        </InlineActions>
      ) : null}

      {apiKeys.error ? (
        <DialogError role="alert">{apiKeys.error.message}</DialogError>
      ) : null}

      {!apiKeys.isLoading &&
      !apiKeys.isError &&
      !isFiltered &&
      filteredRows.length === 0 ? (
        <AccessTokenEmptyState
          onCreateToken={() => setCreateOpen(true)}
          onCreateLocalToken={() => setLocalOpen(true)}
        />
      ) : (
        <APIKeysTable
          rows={filteredRows}
          loading={apiKeys.isLoading}
          error={apiKeys.isError}
          workspaceNameById={workspaceNameById}
          databaseNameById={databaseNameById}
          revoking={revoking}
          onRevoke={(key) => void revoke(key)}
          toolbarAction={
            <HeaderActions>
              <Button
                size="medium"
                variant="secondary-fill"
                onClick={() => setLocalOpen(true)}
              >
                Local
              </Button>
              <Button size="medium" onClick={() => setCreateOpen(true)}>
                Create API key
              </Button>
            </HeaderActions>
          }
        />
      )}

      <CreateMCPAccessDialog
        isOpen={createOpen}
        onClose={() => setCreateOpen(false)}
        initialWorkspaceId={workspaceId}
      />
      <LocalMCPAccessDialog
        isOpen={localOpen}
        onClose={() => setLocalOpen(false)}
        workspaces={workspacesQuery.data ?? []}
        initialWorkspaceId={workspaceId}
        initialDatabaseId={databaseId}
      />
    </PageStack>
  );
}

const HeaderActions = styled.div`
  display: flex;
  flex-wrap: nowrap;
  gap: 10px;
  align-items: center;
  flex-shrink: 0;
  white-space: nowrap;
`;
