import "./SegmentedControl.css";
import { Tooltip } from "../Tooltip/Tooltip";

export type SegmentedControlProps = {
  /** `dot`: a background class for a 7px dot before the label (the agent scope's agent dots, §11M 3.3). */
  options: { value: string; label: string; dot?: string }[];
  value: string;
  onChange: (value: string) => void;
  /** The option whose request is in flight: shown pending, not yet confirmed (§6A.4). */
  pending?: string | null;
  disabled?: boolean;
  disabledReason?: string;
  "aria-label": string;
  /** The id of a visible label naming the control (it then wins over aria-label). */
  labelledBy?: string;
  /** Pins hover or focus on one option, for stories. */
  force?: { value: string; state: "hover" | "focus" };
};

/** Buttons with hairline separators (handoff §6.1), e.g. the time window 1h | 24h | 7d | 14d | All. */
export function SegmentedControl({ options, value, onChange, pending, disabled, disabledReason, force, labelledBy, ...rest }: SegmentedControlProps) {
  return (
    <div
      role="group"
      aria-label={labelledBy ? undefined : rest["aria-label"]}
      aria-labelledby={labelledBy}
      aria-disabled={disabled || undefined}
      className="segmented rounded-sm"
    >
      {options.map((o) => {
        const selected = o.value === value;
        return (
          <Tooltip key={o.value} kind="disabled" content={disabled && disabledReason ? { body: disabledReason } : null}>
          <button
            type="button"
            aria-pressed={selected}
            aria-busy={pending === o.value || undefined}
            aria-disabled={disabled || undefined}
            data-force={force?.value === o.value ? force.state : undefined}
            className="segmented-option type-label"
            onPointerDown={() => !disabled && !selected && onChange(o.value)}
            onClick={() => !disabled && !selected && onChange(o.value)}
          >
            {o.dot && <span aria-hidden className={`segmented-dot rounded-pill ${o.dot}`} />}
            {o.label}
          </button>
          </Tooltip>
        );
      })}
    </div>
  );
}
