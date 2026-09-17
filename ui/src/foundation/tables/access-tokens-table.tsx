import { Button, Select, Typography } from "@redis-ui/components";
import { Table } from "@redis-ui/table";
import type { ColumnDef, SortingState } from "@redis-ui/table";
import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import styled from "styled-components";
import {
  DialogBody,
  DialogCard,
  DialogCloseButton,
  DialogHeader,
  DialogOverlay,
  DialogTitle,
} from "../../components/afs-kit";
import { PlugIcon } from "../../components/lucide-icons";
import { getControlPlaneURL } from "../api/afs";
import { isControlPlaneScope } from "../types/afs";
import type { AFSMCPProfile, APIKey } from "../types/afs";
import { findTemplate } from "../../features/templates/templates-data";
import { compareValues } from "../sort-compare";
import { formatCapability } from "./api-key-format";
import * as S from "./workspace-table.styles";

export { formatCapability } from "./api-key-format";

type APIKeySortField =
  | "name"
  | "scope"
  | "lastUsedAt"
  | "expiresAt"
  | "createdAt";

export type APIKeyFilterChip = "all" | "workspace" | "control-plane";

type Props = {
  rows: APIKey[];
  loading?: boolean;
  error?: boolean;
  errorMessage?: string;
  workspaceNameById?: Map<string, string>;
  databaseNameById?: Map<string, string>;
  toolbarAction?: ReactNode;
  onRevoke: (key: APIKey) => void;
  revoking?: boolean;
  initialFilter?: APIKeyFilterChip;
};

/* ------------------------------------------------------------------ */
/*  Formatters                                                        */
/* ------------------------------------------------------------------ */

function keyScopeKind(key: APIKey): "control-plane" | "workspace" {
  if (isControlPlaneScope(key.scope)) return "control-plane";
  return "workspace";
}

/** Summarizes the capability for this workspace or control-plane key. */
function summarizeCapability(
  key: APIKey,
  profile?: AFSMCPProfile,
): string {
  return formatCapability(key.capability, profile);
}

function formatScopeKind(scopeKind: "control-plane" | "workspace"): string {
  return scopeKind === "control-plane" ? "Control plane" : "Workspace";
}

function formatTimestamp(value?: string) {
  if (!value) return "Never";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function timeAgoOrDate(value?: string) {
  if (!value) return "Never";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const seconds = Math.floor((Date.now() - date.getTime()) / 1000);
  if (seconds < 60) return "Just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 14) return `${days}d ago`;
  return date.toLocaleDateString();
}

function expiryLabel(value?: string) {
  if (!value) return "No expiry";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const delta = date.getTime() - Date.now();
  if (delta <= 0) return "Expired";
  const hours = Math.floor(delta / (1000 * 60 * 60));
  if (hours < 24) return `${hours}h left`;
  const days = Math.floor(hours / 24);
  return `${days}d left`;
}

function keyMatchesFilter(key: APIKey, filter: APIKeyFilterChip): boolean {
  switch (filter) {
    case "all":
      return true;
    case "workspace":
      return !isControlPlaneScope(key.scope);
    case "control-plane":
      return isControlPlaneScope(key.scope);
  }
}

/* ------------------------------------------------------------------ */
/*  Detail dialog                                                     */
/* ------------------------------------------------------------------ */

function APIKeyDetailDialog({
  apiKey,
  workspaceNameById,
  databaseNameById,
  revoking,
  onClose,
  onRevoke,
}: {
  apiKey: APIKey;
  workspaceNameById?: Map<string, string>;
  databaseNameById?: Map<string, string>;
  revoking?: boolean;
  onClose: () => void;
  onRevoke: (key: APIKey) => void;
}) {
  const [copied, setCopied] = useState<string | null>(null);
  const [confirmingRevoke, setConfirmingRevoke] = useState(false);
  const [snippetTab, setSnippetTab] = useState<"mcp" | "cli" | "sdk">("mcp");
  const scopeKind = keyScopeKind(apiKey);
  const workspaceId = apiKey.workspaceId ?? "";
  const workspaceName =
    apiKey.workspaceName || workspaceNameById?.get(workspaceId) || workspaceId;
  const databaseId = apiKey.databaseId ?? "";
  const databaseName = databaseNameById?.get(databaseId) || databaseId;
  const profile = apiKey.kind === "mcp" ? apiKey.profile : undefined;
  const titleLabel = apiKey.name?.trim() || "API key";

  function copy(text: string, label: string) {
    void navigator.clipboard.writeText(text).then(() => {
      setCopied(label);
      window.setTimeout(() => setCopied(null), 2000);
    });
  }

  const mcpSnippet = buildMCPSnippet({
    workspaceName,
    controlPlane: scopeKind === "control-plane",
  });
  const cliLogin = buildCLILoginSnippet(workspaceName);
  const sdkSnippet = buildSDKSnippet({
    workspaceName,
    controlPlane: scopeKind === "control-plane",
  });

  return (
    <>
      <DialogOverlay
        onClick={(event) => {
          if (event.target === event.currentTarget) onClose();
        }}
      >
        <DialogCard>
          <DialogHeader>
            <div>
              <DialogTitle>{titleLabel}</DialogTitle>
              <DialogBody>
                {scopeKind === "control-plane" ? (
                  <>Account-wide admin API key.</>
                ) : (
                  <>API key for <strong>{workspaceName}</strong>.</>
                )}
              </DialogBody>
            </div>
            <DialogCloseButton onClick={onClose}>&times;</DialogCloseButton>
          </DialogHeader>

          <DetailGrid>
            <DetailField>
              <DetailLabel>Key ID</DetailLabel>
              <DetailValue
                style={{
                  fontFamily:
                    "var(--afs-mono, ui-monospace, SFMono-Regular, Menlo, monospace)",
                  fontSize: 12,
                }}
              >
                {apiKey.id}
              </DetailValue>
            </DetailField>
            <DetailField>
              <DetailLabel>Scope</DetailLabel>
              <DetailValue>{formatScopeKind(scopeKind)}</DetailValue>
            </DetailField>
            <DetailField>
              <DetailLabel>Capability</DetailLabel>
              <DetailValue>
                {summarizeCapability(apiKey, profile)}
              </DetailValue>
            </DetailField>
            {scopeKind !== "control-plane" ? (
              <>
                <DetailField>
                  <DetailLabel>Target</DetailLabel>
                  <DetailValue>{workspaceName}</DetailValue>
                </DetailField>
                <DetailField>
                  <DetailLabel>Database</DetailLabel>
                  <DetailValue>{databaseName || "—"}</DetailValue>
                </DetailField>
              </>
            ) : null}
            <DetailField>
              <DetailLabel>Created</DetailLabel>
              <DetailValue>{formatTimestamp(apiKey.createdAt)}</DetailValue>
            </DetailField>
            <DetailField>
              <DetailLabel>Last used</DetailLabel>
              <DetailValue>
                {apiKey.lastUsedAt ? formatTimestamp(apiKey.lastUsedAt) : "Never"}
              </DetailValue>
            </DetailField>
            <DetailField>
              <DetailLabel>Expires</DetailLabel>
              <DetailValue>
                {apiKey.expiresAt ? formatTimestamp(apiKey.expiresAt) : "Never"}
              </DetailValue>
            </DetailField>
          </DetailGrid>

          <SnippetBlock>
            <SnippetHeader>
              <DetailLabel>How to use this key</DetailLabel>
              <SnippetTabs>
                <SnippetTab
                  type="button"
                  $active={snippetTab === "mcp"}
                  onClick={() => setSnippetTab("mcp")}
                >
                  MCP client
                </SnippetTab>
                <SnippetTab
                  type="button"
                  $active={snippetTab === "cli"}
                  onClick={() => setSnippetTab("cli")}
                >
                  CLI
                </SnippetTab>
                <SnippetTab
                  type="button"
                  $active={snippetTab === "sdk"}
                  onClick={() => setSnippetTab("sdk")}
                >
                  SDK
                </SnippetTab>
              </SnippetTabs>
            </SnippetHeader>

            <SnippetHint>
              {snippetTab === "mcp" ? (
                <>
                  Paste into your client's MCP config (e.g.{" "}
                  <code>claude_desktop_config.json</code>) and replace{" "}
                  <code>{"<YOUR_KEY>"}</code> with the value copied at creation.
                </>
              ) : snippetTab === "cli" ? (
                <>Sign the AFS CLI in with this key, then mount workspaces.</>
              ) : (
                <>
                  Use this key as the bearer token from any SDK that talks to
                  the AFS control plane.
                </>
              )}
            </SnippetHint>

            <CodeBlock>
              {snippetTab === "mcp"
                ? mcpSnippet
                : snippetTab === "cli"
                  ? cliLogin
                  : sdkSnippet}
            </CodeBlock>
            <SnippetActions>
              <Button
                size="small"
                variant="secondary-fill"
                onClick={() =>
                  copy(
                    snippetTab === "mcp"
                      ? mcpSnippet
                      : snippetTab === "cli"
                        ? cliLogin
                        : sdkSnippet,
                    snippetTab,
                  )
                }
              >
                {copied === snippetTab ? "Copied!" : "Copy snippet"}
              </Button>
            </SnippetActions>
          </SnippetBlock>

          <DialogFooterRow>
            <RemoveButton
              size="medium"
              type="button"
              disabled={revoking}
              onClick={() => setConfirmingRevoke(true)}
            >
              Revoke API key
            </RemoveButton>
            <Button
              size="medium"
              type="button"
              variant="secondary-fill"
              onClick={onClose}
            >
              Close
            </Button>
          </DialogFooterRow>
        </DialogCard>
      </DialogOverlay>

      {confirmingRevoke ? (
        <ConfirmOverlay
          onClick={(event) => {
            if (event.target === event.currentTarget && !revoking) {
              setConfirmingRevoke(false);
            }
          }}
        >
          <ConfirmCard role="alertdialog" aria-live="polite">
            <DialogHeader>
              <div>
                <DialogTitle>Revoke this API key?</DialogTitle>
                <DialogBody>
                  Any agent or CLI using this key will immediately lose access
                  {scopeKind === "control-plane" ? null : (
                    <>
                      {" to "}<strong>{workspaceName}</strong>
                    </>
                  )}
                  . This can&rsquo;t be undone.
                </DialogBody>
              </div>
              <DialogCloseButton
                onClick={() => {
                  if (!revoking) setConfirmingRevoke(false);
                }}
              >
                &times;
              </DialogCloseButton>
            </DialogHeader>
            <ConfirmRevokeActions>
              <Button
                size="medium"
                type="button"
                variant="secondary-fill"
                onClick={() => setConfirmingRevoke(false)}
                disabled={revoking}
              >
                Cancel
              </Button>
              <RemoveButton
                size="medium"
                type="button"
                disabled={revoking}
                onClick={() => onRevoke(apiKey)}
              >
                {revoking ? "Revoking..." : "Yes, revoke"}
              </RemoveButton>
            </ConfirmRevokeActions>
          </ConfirmCard>
        </ConfirmOverlay>
      ) : null}
    </>
  );
}

function buildMCPSnippet({
  workspaceName,
  controlPlane,
}: {
  workspaceName: string;
  controlPlane: boolean;
}) {
  const serverName = controlPlane
    ? "agent-filesystem"
    : `afs-${(workspaceName || "workspace").trim()}`;
  return JSON.stringify(
    {
      mcpServers: {
        [serverName]: {
          url: `${getControlPlaneURL().replace(/\/+$/, "")}/mcp`,
          headers: {
            Authorization: "Bearer <YOUR_KEY>",
          },
        },
      },
    },
    null,
    2,
  );
}

function buildCLILoginSnippet(workspaceName: string) {
  const cp = getControlPlaneURL();
  return [
    `afs auth login --url ${shellQuote(cp)} --access-token <YOUR_KEY>`,
    `afs ws mount ${shellQuote(workspaceName || "<workspace>")} <directory>`,
  ].join("\n");
}

function buildSDKSnippet({
  workspaceName,
  controlPlane,
}: {
  workspaceName: string;
  controlPlane: boolean;
}) {
  const target = controlPlane ? "control plane" : workspaceName || "<workspace>";
  return [
    `// TypeScript / Python SDK`,
    `// Pass the key as a bearer token to the control plane (${target}).`,
    `const afs = new AFS({`,
    `  baseURL: ${JSON.stringify(getControlPlaneURL())},`,
    `  apiKey: "<YOUR_KEY>",`,
    `});`,
  ].join("\n");
}

function shellQuote(value: string) {
  return `'${value.replaceAll("'", "'\\''")}'`;
}

/* ------------------------------------------------------------------ */
/*  Main component                                                    */
/* ------------------------------------------------------------------ */

export function APIKeysTable({
  rows,
  loading = false,
  error = false,
  errorMessage = "Unable to load API keys. Please retry.",
  workspaceNameById,
  databaseNameById,
  toolbarAction,
  onRevoke,
  revoking = false,
  initialFilter = "all",
}: Props) {
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState<APIKeyFilterChip>(initialFilter);
  const [sortBy, setSortBy] = useState<APIKeySortField>("createdAt");
  const [sortDirection, setSortDirection] = useState<"asc" | "desc">("desc");
  const [selectedKey, setSelectedKey] = useState<APIKey | null>(null);
  const navigate = useNavigate();

  const filteredRows = useMemo(() => {
    const query = search.trim().toLowerCase();
    const baseRows = rows
      .filter((row) => keyMatchesFilter(row, filter))
      .filter((row) => {
        if (query === "") return true;
        return [
          row.name ?? "",
          row.id,
          row.workspaceName ?? "",
          row.workspaceId ?? "",
          row.databaseId ?? "",
          row.scope ?? "",
          row.capability ?? "",
          row.kind === "mcp" ? row.profile : "",
        ].some((value) => value.toLowerCase().includes(query));
      });

    return [...baseRows].sort((left, right) => {
      const leftValue = sortValue(left, sortBy, workspaceNameById);
      const rightValue = sortValue(right, sortBy, workspaceNameById);
      return compareValues(leftValue, rightValue, sortDirection);
    });
  }, [rows, search, sortBy, sortDirection, workspaceNameById, filter]);

  const sorting = useMemo<SortingState>(
    () => [{ id: sortBy, desc: sortDirection === "desc" }],
    [sortBy, sortDirection],
  );

  const columns = useMemo(
    () =>
      [
        {
          accessorKey: "name",
          header: "Name",
          size: 200,
          enableSorting: true,
          cell: ({ row }) => {
            const original = row.original;
            const templateSlug =
              original.kind === "mcp" ? original.templateSlug : undefined;
            const template = templateSlug ? findTemplate(templateSlug) : undefined;
            const templateLabel = template?.title ?? templateSlug;
            return (
              <NameCell>
                <NameIconBox>
                  <PlugIcon customSize={18} />
                </NameIconBox>
                <S.Stack>
                  <Typography.Body component="strong">
                    {original.name?.trim() || "Unnamed"}
                  </Typography.Body>
                  <Typography.Body
                    color="secondary"
                    component="span"
                    style={{ fontSize: 11 }}
                  >
                    {original.id}
                  </Typography.Body>
                  {templateSlug && original.workspaceId ? (
                    <TemplateChip
                      type="button"
                      title={`Open installed template: ${templateLabel}`}
                      onClick={(event) => {
                        event.stopPropagation();
                        void navigate({
                          to: "/templates/installed/$workspaceId",
                          params: { workspaceId: original.workspaceId ?? "" },
                          search: original.databaseId
                            ? { databaseId: original.databaseId }
                            : {},
                        });
                      }}
                    >
                      from: {templateLabel}
                    </TemplateChip>
                  ) : null}
                </S.Stack>
              </NameCell>
            );
          },
        },
        {
          accessorKey: "scope",
          header: "Scope",
          size: 280,
          enableSorting: true,
          cell: ({ row }) => {
            const key = row.original;
            if (isControlPlaneScope(key.scope)) {
              return <ScopeBadge $tone="control">Control plane</ScopeBadge>;
            }
            const name =
              key.workspaceName ||
              workspaceNameById?.get(key.workspaceId ?? "") ||
              key.workspaceId ||
              "—";
            const db =
              databaseNameById?.get(key.databaseId ?? "") || key.databaseId;
            return (
              <ScopeCell>
                <ScopeBadge
                  $tone="workspace"
                  title={db ? `Database: ${db}` : undefined}
                >
                  Workspace: {name}
                </ScopeBadge>
              </ScopeCell>
            );
          },
        },
        {
          accessorKey: "capability",
          header: "Capability",
          size: 180,
          enableSorting: false,
          cell: ({ row }) => {
            const key = row.original;
            const profile = key.kind === "mcp" ? key.profile : undefined;
            return (
              <Typography.Body component="span">
                {summarizeCapability(key, profile)}
              </Typography.Body>
            );
          },
        },
        {
          accessorKey: "lastUsedAt",
          header: "Last used",
          size: 100,
          enableSorting: true,
          cell: ({ row }) => (
            <Typography.Body
              component="span"
              color={row.original.lastUsedAt ? undefined : "secondary"}
            >
              {timeAgoOrDate(row.original.lastUsedAt)}
            </Typography.Body>
          ),
        },
        {
          accessorKey: "expiresAt",
          header: "Expires",
          size: 100,
          enableSorting: true,
          cell: ({ row }) => (
            <S.Stack>
              <Typography.Body component="span">
                {expiryLabel(row.original.expiresAt)}
              </Typography.Body>
              {row.original.expiresAt ? (
                <Typography.Body
                  color="secondary"
                  component="span"
                  style={{ fontSize: 11 }}
                >
                  {new Date(row.original.expiresAt).toLocaleDateString()}
                </Typography.Body>
              ) : null}
            </S.Stack>
          ),
        },
        {
          accessorKey: "createdAt",
          header: "Created",
          size: 100,
          enableSorting: true,
          cell: ({ row }) => (
            <Typography.Body component="span" color="secondary">
              {timeAgoOrDate(row.original.createdAt)}
            </Typography.Body>
          ),
        },
      ] as ColumnDef<APIKey>[],
    [databaseNameById, workspaceNameById, navigate],
  );

  const isFiltering = search.trim() !== "" || filter !== "all";

  return (
    <>
      <S.TableBlock>
        <S.HeadingWrap style={{ padding: 0 }}>
          <S.SearchInput
            value={search}
            onChange={setSearch}
            placeholder="Search API keys..."
          />
          <FilterSelectWrap>
            <Select
              aria-label="Filter API keys"
              options={[
                { value: "all", label: "All keys" },
                { value: "workspace", label: "Workspace" },
                { value: "control-plane", label: "Control plane" },
              ]}
              value={filter}
              onChange={(next) => setFilter(next as APIKeyFilterChip)}
            />
          </FilterSelectWrap>
          {toolbarAction}
        </S.HeadingWrap>

        {loading ? <S.EmptyState>Loading API keys...</S.EmptyState> : null}
        {error ? <S.EmptyState role="alert">{errorMessage}</S.EmptyState> : null}
        {!loading && !error && filteredRows.length === 0 ? (
          <S.EmptyState>
            {isFiltering
              ? "No API keys match the current filter."
              : 'No API keys yet. Click "Create API key" to issue one.'}
          </S.EmptyState>
        ) : null}

        {!loading && !error && filteredRows.length > 0 ? (
          <S.TableCard>
            <S.DenseTableViewport>
              <Table
                columns={columns}
                data={filteredRows}
                sorting={sorting}
                manualSorting
                onSortingChange={(nextState) => {
                  if (nextState.length === 0) {
                    setSortBy("createdAt");
                    setSortDirection("desc");
                    return;
                  }
                  const next = nextState[0];
                  setSortBy(next.id as APIKeySortField);
                  setSortDirection(next.desc ? "desc" : "asc");
                }}
                enableSorting
                stripedRows
                onRowClick={(rowData) => setSelectedKey(rowData)}
              />
            </S.DenseTableViewport>
          </S.TableCard>
        ) : null}
      </S.TableBlock>

      {selectedKey != null ? (
        <APIKeyDetailDialog
          apiKey={selectedKey}
          workspaceNameById={workspaceNameById}
          databaseNameById={databaseNameById}
          revoking={revoking}
          onClose={() => setSelectedKey(null)}
          onRevoke={(key) => {
            onRevoke(key);
            setSelectedKey(null);
          }}
        />
      ) : null}
    </>
  );
}

/**
 * @deprecated Use APIKeysTable. Kept as a thin alias while consumers migrate.
 */
export const AccessTokensTable = APIKeysTable;

function sortValue(
  key: APIKey,
  field: APIKeySortField,
  workspaceNameById?: Map<string, string>,
): string | number {
  switch (field) {
    case "name":
      return key.name?.trim() || key.id;
    case "scope":
      return (
        key.workspaceName ||
        workspaceNameById?.get(key.workspaceId ?? "") ||
        key.workspaceId ||
        key.scope ||
        ""
      );
    case "lastUsedAt":
      return key.lastUsedAt ? new Date(key.lastUsedAt).getTime() : 0;
    case "expiresAt":
      return key.expiresAt
        ? new Date(key.expiresAt).getTime()
        : Number.MAX_SAFE_INTEGER;
    case "createdAt":
    default:
      return new Date(key.createdAt).getTime();
  }
}

/* ------------------------------------------------------------------ */
/*  Styled                                                            */
/* ------------------------------------------------------------------ */

const FilterSelectWrap = styled.div`
  min-width: 168px;
  flex-shrink: 0;

  /* The Redis Select renders inline-block by default; let it stretch to
     fill the wrapper so the dropdown sits neatly between search and the
     toolbar actions. */
  > * {
    width: 100%;
  }
`;

const DetailGrid = styled.div`
  display: grid;
  gap: 16px;
  grid-template-columns: 1fr 1fr;
  margin-top: 8px;

  @media (max-width: 600px) {
    grid-template-columns: 1fr;
  }
`;

const DetailField = styled.div`
  display: flex;
  flex-direction: column;
  gap: 4px;
`;

const DetailLabel = styled.span`
  color: var(--afs-muted, #71717a);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
`;

const DetailValue = styled.span`
  color: var(--afs-ink, #18181b);
  font-size: 14px;
  word-break: break-all;
`;

const NameCell = styled.div`
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
`;

const NameIconBox = styled.span`
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 32px;
  height: 32px;
  border-radius: 8px;
  background: var(--afs-accent-soft, rgba(37, 99, 235, 0.08));
  color: var(--afs-accent, #2563eb);
`;

const ScopeBadge = styled.span<{ $tone: "control" | "workspace" }>`
  display: inline-flex;
  align-items: center;
  max-width: 100%;
  padding: 3px 10px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.02em;
  overflow-wrap: anywhere;
  white-space: normal;
  border: 1px solid
    ${({ $tone }) =>
      $tone === "control"
        ? "color-mix(in srgb, #d97706 55%, var(--afs-line))"
        : "color-mix(in srgb, #10b981 45%, var(--afs-line))"};
  background: ${({ $tone }) =>
    $tone === "control"
      ? "color-mix(in srgb, #d97706 16%, transparent)"
      : "color-mix(in srgb, #10b981 14%, transparent)"};
  color: ${({ $tone }) => ($tone === "control" ? "#b45309" : "#047857")};
`;

const ScopeCell = styled.div`
  display: flex;
  align-items: center;
  min-width: 0;
  max-width: 100%;
`;

const TemplateChip = styled.button`
  align-self: flex-start;
  margin-top: 3px;
  padding: 2px 8px;
  border-radius: 999px;
  border: 1px solid
    color-mix(in srgb, var(--afs-accent, #2563eb) 30%, var(--afs-line));
  background: color-mix(in srgb, var(--afs-accent, #2563eb) 10%, transparent);
  color: var(--afs-accent, #2563eb);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.02em;
  cursor: pointer;

  &:hover {
    background: color-mix(
      in srgb,
      var(--afs-accent, #2563eb) 18%,
      transparent
    );
  }
`;

const SnippetBlock = styled.div`
  display: grid;
  gap: 8px;
  margin-top: 20px;
`;

const SnippetHeader = styled.div`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
`;

const SnippetTabs = styled.div`
  display: inline-flex;
  gap: 4px;
  padding: 3px;
  border-radius: 8px;
  background: var(--afs-panel);
  border: 1px solid var(--afs-line);
`;

const SnippetTab = styled.button<{ $active: boolean }>`
  appearance: none;
  border: none;
  background: ${({ $active }) =>
    $active ? "var(--afs-accent, #2563eb)" : "transparent"};
  color: ${({ $active }) => ($active ? "#fff" : "var(--afs-ink-soft)")};
  padding: 4px 10px;
  border-radius: 6px;
  font-size: 11.5px;
  font-weight: 700;
  cursor: pointer;
  transition:
    background 120ms ease,
    color 120ms ease;
`;

const SnippetHint = styled.p`
  margin: 0;
  color: var(--afs-muted);
  font-size: 12px;
  line-height: 1.55;

  code {
    font-family: var(--afs-mono, ui-monospace, SFMono-Regular, Menlo, monospace);
    background: var(--afs-panel);
    padding: 1px 5px;
    border-radius: 6px;
    border: 1px solid var(--afs-line);
    font-size: 11px;
  }
`;

const CodeBlock = styled.pre`
  margin: 0;
  padding: 14px;
  border-radius: 14px;
  background: rgba(15, 23, 42, 0.94);
  color: #e2e8f0;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
  overflow: auto;
  max-height: 240px;
`;

const SnippetActions = styled.div`
  display: flex;
  justify-content: flex-end;
`;

const DialogFooterRow = styled.div`
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  margin-top: 24px;
`;

const ConfirmOverlay = styled(DialogOverlay)`
  z-index: 50;
`;

const ConfirmCard = styled(DialogCard)`
  max-width: 460px;
`;

const ConfirmRevokeActions = styled.div`
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 14px;

  @media (max-width: 600px) {
    flex-direction: column-reverse;
  }
`;

const RemoveButton = styled(Button)`
  && {
    background: ${({ theme }) => theme.semantic.color.background.danger500};
    border-color: ${({ theme }) => theme.semantic.color.background.danger500};
    color: ${({ theme }) => theme.semantic.color.text.inverse};
  }

  &&:hover:not(:disabled) {
    background: ${({ theme }) => theme.semantic.color.background.danger600};
    border-color: ${({ theme }) => theme.semantic.color.background.danger600};
  }
`;
