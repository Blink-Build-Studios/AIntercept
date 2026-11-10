import type { ReactNode } from "react";
import { DataTable } from "../DataTable/DataTable";
import {
  SESSION_AGENT_COLUMNS,
  SESSION_AGENT_MIN_WIDTH,
  SESSION_COLUMNS,
  SESSION_COMMON_COLUMNS,
  SESSION_COMMON_MIN_WIDTH,
  SESSION_MIN_WIDTH,
} from "../SessionRow/SessionRow";

/**
 * Which columns a person's sessions table has (handoff §11M 2.7): `mixed` is All agents for a
 * person with more than one agent (only the common columns, with Agent); `claude-code` is the
 * canonical SessionRow; `codex` the common columns without Agent.
 */
export type SessionTableView = "mixed" | "claude-code" | "codex";

export const sessionTableColumns = (view: SessionTableView) =>
  view === "mixed"
    ? { columns: SESSION_AGENT_COLUMNS, minWidth: SESSION_AGENT_MIN_WIDTH }
    : view === "codex"
      ? { columns: SESSION_COMMON_COLUMNS, minWidth: SESSION_COMMON_MIN_WIDTH }
      : { columns: SESSION_COLUMNS, minWidth: SESSION_MIN_WIDTH };

export type SessionTableProps = {
  view: SessionTableView;
  /** SessionRows; with `mixed` they take `agentColumn`, with `codex` they take `common`. */
  children?: ReactNode;
  /** Nested threads: a treegrid. */
  expandable?: boolean;
  loading?: boolean;
  empty?: ReactNode;
  footer?: ReactNode;
};

/** A person's sessions (handoff §6.5, §11M 2.7), with the columns its scope has. */
export function SessionTable({ view, children, expandable, loading, empty, footer }: SessionTableProps) {
  const { columns, minWidth } = sessionTableColumns(view);
  return (
    <DataTable columns={columns} minWidth={minWidth} label="Sessions" expandable={expandable} loading={loading} skeletonRows={6} empty={empty} footer={footer}>
      {children}
    </DataTable>
  );
}
