import "../Chip/Chip.css";
import "./FilterChip.css";

export type FilterChipProps = {
  /** Plain words; shown in micro caps. */
  label: string;
  /** Removes the filter. Without it the chip is a static reminder (an empty state's "active filters"). */
  onRemove?: () => void;
  /** The remove button's accessible name, e.g. "Remove filter: not captured". */
  removeLabel?: string;
  /** Pins the remove button's hover or focus state for stories. */
  "data-force"?: "hover" | "focus";
};

/**
 * An active filter set from elsewhere (the coverage line, a +N stat, the
 * timeline's hatch), shown after the filter input until it's removed (handoff
 * §11E.2). Danger tone: it only ever filters to calls the proxy didn't capture.
 */
export function FilterChip({ label, onRemove, removeLabel, ...rest }: FilterChipProps) {
  return (
    <span className={`chip filter-chip rounded-sm border border-danger bg-surface-raised text-danger ${onRemove ? "filter-chip-removable" : ""}`}>
      {label.toUpperCase()}
      {onRemove && (
        <button type="button" className="filter-chip-remove text-ink" aria-label={removeLabel ?? `Remove filter: ${label}`} onClick={onRemove} data-force={rest["data-force"]}>
          ×
        </button>
      )}
    </span>
  );
}
