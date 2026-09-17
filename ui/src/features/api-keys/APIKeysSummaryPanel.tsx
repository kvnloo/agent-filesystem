import { Button, Typography } from "@redis-ui/components";
import { useNavigate } from "@tanstack/react-router";
import { useMemo } from "react";
import styled from "styled-components";
import { useAllAPIKeys } from "../../foundation/hooks/use-afs";
import type { APIKey } from "../../foundation/types/afs";
import { isControlPlaneScope } from "../../foundation/types/afs";
import { formatCapability } from "../../foundation/tables/api-key-format";

type Props = {
  /** Workspace / workspace id the summary should filter on. */
  workspaceId: string;
  workspaceName: string;
  databaseId?: string;
  /** Headline label — defaults to "API keys for this workspace". */
  headline?: string;
  /** Empty state message when no keys exist yet. */
  emptyState?: string;
  /** Optional cap on how many rows render before "see all". */
  preview?: number;
};

function timeAgo(value?: string) {
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

function keyTypeLabel(key: APIKey): "MCP" | "CLI" {
  return key.kind === "cli" ? "CLI" : "MCP";
}

export function APIKeysSummaryPanel({
  workspaceId,
  workspaceName,
  databaseId,
  headline = "API keys for this workspace",
  emptyState = "No API keys for this workspace yet. Create one to let agents or the CLI reach it.",
  preview = 5,
}: Props) {
  const navigate = useNavigate();
  const apiKeys = useAllAPIKeys(true);

  const scopedRows = useMemo(() => {
    return apiKeys.data
      .filter((key) => {
        if (isControlPlaneScope(key.scope)) return false;
        return key.workspaceId === workspaceId;
      })
      .sort((left, right) => {
        const leftTime = left.lastUsedAt
          ? new Date(left.lastUsedAt).getTime()
          : new Date(left.createdAt).getTime();
        const rightTime = right.lastUsedAt
          ? new Date(right.lastUsedAt).getTime()
          : new Date(right.createdAt).getTime();
        return rightTime - leftTime;
      });
  }, [apiKeys.data, workspaceId]);

  const previewRows = scopedRows.slice(0, preview);
  const lastUsed = scopedRows.find((row) => row.lastUsedAt)?.lastUsedAt;

  const openCentral = () => {
    void navigate({
      to: "/api-keys",
      search: {
        workspaceId,
        ...(databaseId ? { databaseId } : {}),
      },
    });
  };

  return (
    <PanelCard>
      <PanelHeader>
        <div>
          <PanelTitle>{headline}</PanelTitle>
          <PanelSubtitle>
            {apiKeys.isLoading
              ? "Loading…"
              : scopedRows.length === 0
                ? `Bound to ${workspaceName}.`
                : `${scopedRows.length} active · last used ${timeAgo(lastUsed)}.`}
          </PanelSubtitle>
        </div>
        <PanelActions>
          <Button size="small" onClick={openCentral}>
            Create key
          </Button>
          <Button size="small" variant="secondary-fill" onClick={openCentral}>
            Open API Keys →
          </Button>
        </PanelActions>
      </PanelHeader>

      {apiKeys.isError ? (
        <EmptyHint role="alert">Unable to load API keys.</EmptyHint>
      ) : scopedRows.length === 0 ? (
        <EmptyHint>{emptyState}</EmptyHint>
      ) : (
        <RowList>
          {previewRows.map((row) => {
            const profile = row.kind === "mcp" ? row.profile : undefined;
            return (
              <Row
                key={row.id}
                type="button"
                onClick={openCentral}
                title="Open in API Keys"
              >
                <RowName>
                  <Typography.Body component="strong">
                    {row.name?.trim() || "Unnamed"}
                  </Typography.Body>
                  <Typography.Body
                    color="secondary"
                    component="span"
                    style={{ fontSize: 11 }}
                  >
                    {row.id}
                  </Typography.Body>
                </RowName>
                <RowType $tone={keyTypeLabel(row) === "CLI" ? "cli" : "mcp"}>
                  {keyTypeLabel(row)}
                </RowType>
                <RowCapability>
                  {formatCapability(row.capability, profile)}
                </RowCapability>
                <RowMeta>
                  {row.lastUsedAt ? `Used ${timeAgo(row.lastUsedAt)}` : "Unused"}
                </RowMeta>
              </Row>
            );
          })}
          {scopedRows.length > preview ? (
            <SeeMore type="button" onClick={openCentral}>
              See all {scopedRows.length} keys →
            </SeeMore>
          ) : null}
        </RowList>
      )}
    </PanelCard>
  );
}

const PanelCard = styled.div`
  display: grid;
  gap: 14px;
  padding: 16px 18px;
  border: 1px solid var(--afs-line);
  border-radius: 14px;
  background: var(--afs-panel);

  [data-skin="situation-room"] && {
    border-radius: var(--afs-r-2);
    border-color: var(--afs-line-strong);
    background: var(--afs-bg-1);
  }
`;

const PanelHeader = styled.div`
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
`;

const PanelTitle = styled.div`
  color: var(--afs-ink);
  font-size: 14px;
  font-weight: 700;
`;

const PanelSubtitle = styled.div`
  color: var(--afs-muted);
  font-size: 12.5px;
  margin-top: 2px;
`;

const PanelActions = styled.div`
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
`;

const EmptyHint = styled.div`
  color: var(--afs-muted);
  font-size: 13px;
  line-height: 1.55;
  padding: 8px 0;
`;

const RowList = styled.div`
  display: grid;
  gap: 6px;
`;

const Row = styled.button`
  appearance: none;
  text-align: left;
  display: grid;
  grid-template-columns: minmax(0, 1.4fr) auto auto auto;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid transparent;
  border-radius: 10px;
  background: transparent;
  color: inherit;
  cursor: pointer;
  transition:
    border-color 120ms ease,
    background 120ms ease;

  &:hover {
    border-color: var(--afs-line);
    background: var(--afs-bg-1, rgba(0, 0, 0, 0.03));
  }

  @media (max-width: 640px) {
    grid-template-columns: 1fr;
  }
`;

const RowName = styled.div`
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
`;

const RowType = styled.span<{ $tone: "mcp" | "cli" }>`
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: 6px;
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.02em;
  border: 1px solid
    ${({ $tone }) =>
      $tone === "cli"
        ? "color-mix(in srgb, #7c3aed 45%, var(--afs-line))"
        : "color-mix(in srgb, var(--afs-accent, #2563eb) 45%, var(--afs-line))"};
  background: ${({ $tone }) =>
    $tone === "cli"
      ? "color-mix(in srgb, #7c3aed 14%, transparent)"
      : "color-mix(in srgb, var(--afs-accent, #2563eb) 14%, transparent)"};
  color: ${({ $tone }) =>
    $tone === "cli" ? "#6d28d9" : "var(--afs-accent, #2563eb)"};
`;

const RowCapability = styled.span`
  color: var(--afs-ink);
  font-size: 12.5px;
  font-weight: 600;
`;

const RowMeta = styled.span`
  color: var(--afs-muted);
  font-size: 11.5px;
`;

const SeeMore = styled.button`
  appearance: none;
  border: none;
  background: transparent;
  color: var(--afs-accent, #2563eb);
  font-size: 12.5px;
  font-weight: 700;
  text-align: left;
  padding: 6px 10px;
  cursor: pointer;

  &:hover {
    text-decoration: underline;
  }
`;
