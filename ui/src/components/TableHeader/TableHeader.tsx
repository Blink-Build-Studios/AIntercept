import { Tooltip } from "../Tooltip/Tooltip";
// The header row and its hover rule live with the table rows.
import "../DataTable/DataTable.css";

export type TableHeaderProps = {
  header: string;
  align?: "left" | "right";
  /** What the column means (§6B.6 copy): dotted underline, cursor help, and a tooltip. */
  definition?: string;
  /** Pins hover for stories. */
  "data-force"?: "hover";
};

/** One column header (handoff §6.5, §6B.6). Headers explain; they never sort: tables stay time-ordered. */
export function TableHeader({ header, align, definition, "data-force": force }: TableHeaderProps) {
  const cls = `${align === "right" ? "dt-right" : ""} ${definition ? "tip-term" : ""}`;
  return (
    <Tooltip content={definition ? { title: header, hint: definition } : null}>
      <span role="columnheader" className={cls} data-force={force}>
        {header}
      </span>
    </Tooltip>
  );
}
