import { Fragment, useMemo } from "react";
import type { TooltipContent } from "../Tooltip/Tooltip";
import { Mark, type MarkForce } from "./TimelineGuide";
import { Track } from "./Lane";
import { ms, useTimeScale, useTrackWidth } from "./scale";
import { readoutLine, type TrackHover } from "./trackHover";

/** `text` replaces the drawn text (default: the state for presence, else `label ?? state`). */
export type Segment = { from: string; to: string; state: string; label?: string | null; text?: string };
/** A mode change too brief for its own segment, drawn as a focus tick: "→ default ×2 in 20 s"; `to`: the run's end. */
export type StateChange = { t: string; label: string; to?: string };

/** Hover (§6B.4): segments and change ticks explain themselves; a click selects their span. */
export type StateInteraction = {
  tip: (s: Segment) => TooltipContent | null;
  onSelect?: (s: Segment) => void;
  changeTip?: (c: StateChange) => TooltipContent | null;
  onChange?: (c: StateChange) => void;
  /** Forces a segment (by `from`) or a change (by `t`) into a state. */
  force?: { at: string; state: MarkForce };
};

/** Mono 10.5px: about 6.3px a character (whether a network label fits its segment). */
const CHAR_PX = 6.3;

/**
 * State over time (handoff §6.4): outlined segments labelled with their value,
 * focus ticks for brief changes. Permission; and network (§11G.A3), where a
 * bypassed segment is bordered and labelled in danger, and a label shows only when
 * the segment is at least its text + 16px wide (else the tooltip carries it).
 * Presence, focus and sleep are rows (RowTrack).
 */
export function StateTrack({
  segments,
  palette,
  changes = [],
  nowrap = false,
  interact,
}: {
  segments: Segment[];
  /** `approval`: Codex's approval policy · sandbox mode, in place of permission (handoff §11J). */
  palette: "permission" | "network" | "approval";
  changes?: StateChange[];
  /** Keep each segment's text on one line, clipped (the session page's lanes). */
  nowrap?: boolean;
  interact?: StateInteraction;
}) {
  const scale = useTimeScale();
  const trackPx = useTrackWidth();
  const forced = (at: string) => (interact?.force?.at === at ? interact.force.state : undefined);
  const segment = (s: Segment, className: string, label: string) => {
    let text = label;
    const a = ms(s.from);
    const w = scale.width(a, ms(s.to));
    const style = { left: `${scale.x(a)}%`, width: `max(2px, ${w}%)` };
    const classes = `tl-state ${className} ${nowrap ? "tl-nowrap" : ""}`;
    if (palette === "network" && (w / 100) * trackPx < text.length * CHAR_PX + 16) text = "";
    // Network labels are their own line (§11G.A3); permission's sit in the segment.
    const bypassed = palette === "network" && s.state === "bypassed";
    const body = palette === "network" ? text && <span className={`tl-state-text ${bypassed ? "text-danger" : ""}`}>{text}</span> : text;
    if (!interact) {
      return (
        <div key={s.from} className={classes} style={style}>
          {body}
        </div>
      );
    }
    return (
      <Mark
        key={s.from}
        t={a}
        tip={interact.tip(s)}
        onActivate={interact.onSelect && (() => interact.onSelect?.(s))}
        link={{ source: "timeline", span: [s.from, s.to] }}
        label={`${label}: ${s.from} to ${s.to}`}
        className={classes}
        style={style}
        force={forced(s.from)}
      >
        {body}
      </Mark>
    );
  };
  // Between segments (§11H.2): nothing recorded there.
  const nothing = { network: "no network recorded", permission: "no permission mode recorded", approval: "no approval policy recorded" }[palette];
  const hover = useMemo<TrackHover>(() => ({ readout: (t) => readoutLine(t, nothing) }), [nothing]);
  return (
    <Track interactive={!!interact} hover={hover}>
      {segments.map((s) => {
        const bypassed = palette === "network" && s.state === "bypassed";
        return segment(s, `tl-state-${palette} ${bypassed ? "tl-state-bypassed" : ""}`, s.text ?? s.label ?? s.state);
      })}
      {changes.map((c) => {
        const left = scale.x(ms(c.t));
        const tip = interact?.changeTip?.(c);
        return (
          <Fragment key={c.t}>
            {tip ? (
              <Mark
                t={ms(c.t)}
                tip={tip}
                onActivate={interact?.onChange && (() => interact.onChange?.(c))}
                label={c.label}
                className="tl-change"
                style={{ left: `${left}%` }}
                force={forced(c.t)}
              />
            ) : (
              <div className="tl-change" style={{ left: `${left}%` }} />
            )}
            <span className="tl-change-label" style={{ left: `calc(${left}% + 4px)` }}>
              {c.label}
            </span>
          </Fragment>
        );
      })}
    </Track>
  );
}
