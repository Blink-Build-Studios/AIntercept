import { formatCount } from "../../lib/format";
import type { Channel } from "../ChannelSwatch/ChannelSwatch";
import { ChannelSwatch } from "../ChannelSwatch/ChannelSwatch";
import "./ToggleChip.css";
import { Tooltip } from "../Tooltip/Tooltip";

export type ToggleChipProps = {
  label: string;
  pressed: boolean;
  onChange: (pressed: boolean) => void;
  swatch?: Channel;
  count?: number;
  disabled?: boolean;
  disabledReason?: string;
  "data-force"?: "hover" | "focus";
};

/**
 * A filter toggle (handoff §6.1), micro caps: callers pass plain words. On is the ink fill with a
 * leading ✓ (§11G.B2); flips on pointer-down (§6A.1).
 */
export function ToggleChip({ label, pressed, onChange, swatch, count, disabled, disabledReason, ...rest }: ToggleChipProps) {
  return (
    <Tooltip kind="disabled" content={disabled && disabledReason ? { body: disabledReason } : null}>
    <button
      type="button"
      aria-pressed={pressed}
      aria-disabled={disabled || undefined}
      data-force={rest["data-force"]}
      className={`toggle-chip rounded-sm ${swatch ? "toggle-chip-swatch" : ""}`}
      onPointerDown={(e) => {
        e.preventDefault();
        if (!disabled) onChange(!pressed);
      }}
      onKeyDown={(e) => {
        if (!disabled && (e.key === "Enter" || e.key === " ")) {
          e.preventDefault();
          onChange(!pressed);
        }
      }}
    >
      {/* On: a leading ✓ (§11G.B2), before the swatch on a channel chip. */}
      {pressed && "✓ "}
      {swatch && <ChannelSwatch channel={swatch} size="small" />}
      {label.toUpperCase()}
      {count !== undefined && ` ${formatCount(count)}`}
    </button>
    </Tooltip>
  );
}
