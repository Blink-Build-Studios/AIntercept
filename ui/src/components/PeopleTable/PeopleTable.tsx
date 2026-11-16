import type { ReactNode } from "react";
import { DataTable } from "../DataTable/DataTable";
import { PERSON_MIN_WIDTH, personColumns, type PeopleView } from "../PersonRow/PersonRow";

export type PeopleTableProps = {
  /** All agents (the default: common columns only) or the selected agent card's view (§11L.1). */
  view: PeopleView;
  /** PersonRows (or PersonRowSkeletons) rendered with the same `view`. */
  children?: ReactNode;
  label?: string;
  loading?: boolean;
  empty?: ReactNode;
  footer?: ReactNode;
  /** The turns form (§11R): Working on, and Turns in place of Prompts; rows pass `turns`. */
  turns?: boolean;
};

/**
 * The People table (handoff §6.3, §11L.1). All agents shows only the fields every
 * provider fills; selecting an agent card scopes it to that agent and appends the
 * agent's columns before the chevron: Claude Code's Bypass and Missed, Codex's
 * `5h · week` and Plan.
 */
export function PeopleTable({ view, children, label = "People", loading, empty, footer, turns = false }: PeopleTableProps) {
  return (
    <DataTable columns={personColumns(view, turns)} minWidth={PERSON_MIN_WIDTH} headClassName={`person-row person-head ${turns ? "person-turns" : ""}`} label={label} loading={loading} empty={empty} footer={footer}>
      {children}
    </DataTable>
  );
}
