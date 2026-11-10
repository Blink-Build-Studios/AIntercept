import type { ReactNode } from "react";
import { DataTable } from "../DataTable/DataTable";
import { requestColumns, requestMinWidth } from "../RequestRow/RequestRow";

export type RequestTableProps = {
  /** Person scope adds the session column and dates the time; session scope has the prompt column. */
  scope: "person" | "session";
  /** A person's All agents requests (§11M 3.3): the agent-dot column; rows take `agentDot` too. */
  agentDot?: boolean;
  /** RequestRows in time order (hidden prewarm and title calls only when shown: quiet rows). */
  children?: ReactNode;
  loading?: boolean;
  empty?: ReactNode;
  footer?: ReactNode;
};

/**
 * Every call on the wire, in time order (handoff §6.5, §11L.6): the canonical request
 * columns, any product's channels in the Kind column, the detail cell shaped per channel.
 */
export function RequestTable({ scope, agentDot, children, loading, empty, footer }: RequestTableProps) {
  const person = scope === "person";
  return (
    <DataTable columns={requestColumns(person, agentDot)} minWidth={requestMinWidth(person, agentDot)} label="Requests" loading={loading} empty={empty} footer={footer}>
      {children}
    </DataTable>
  );
}
