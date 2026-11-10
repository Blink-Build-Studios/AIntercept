import { TextInput } from "../TextInput/TextInput";
import "./FieldList.css";

/** One extracted field; a null value isn't on the rows yet (observer extraction, M7). */
export type Field = { key: string; value: string | null };

export type FieldListProps = {
  fields: Field[];
  /** With `onFilter`, a filter input above the list ("filter 64 fields…"). */
  filter?: string;
  onFilter?: (q: string) => void;
  className?: string;
};

/** Every field on the request, filterable by key or value (handoff §6.6). */
export function FieldList({ fields, filter = "", onFilter, className = "" }: FieldListProps) {
  const q = filter.trim().toLowerCase();
  const shown = q ? fields.filter((f) => f.key.toLowerCase().includes(q) || (f.value ?? "").toLowerCase().includes(q)) : fields;
  return (
    <div className={`field-list ${className}`}>
      {onFilter && (
        <div className="field-filter">
          <TextInput id="field-filter" label="Filter fields" hideLabel value={filter} onChange={onFilter} placeholder={`filter ${fields.length} fields…`} />
        </div>
      )}
      <dl>
        {shown.map((f) => (
          <div key={f.key} className="field-row type-data">
            <dt className="field-key text-ink-muted">{f.key}</dt>
            <dd className={f.value === null ? "text-ink-muted" : "text-ink"}>{f.value ?? "— not on rows yet"}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}
