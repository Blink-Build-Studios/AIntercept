// Axis ticks at round local times (handoff §11G.A6): the window picks a minor and
// major step from a ladder; majors carry labels, midnights carry the date.
const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

type Step = { kind: "ms"; ms: number } | { kind: "week" } | { kind: "month" };
const ms = (n: number): Step => ({ kind: "ms", ms: n });

/** Window ≤ limit → minor, major. */
const LADDER: [number, Step, Step][] = [
  // An hour or less (a Codex thread, CompAgents TimeAxis/codex, §11N.4): 5-minute ticks, 10-minute labels.
  [HOUR, ms(5 * MIN), ms(10 * MIN)],
  [3 * HOUR, ms(5 * MIN), ms(30 * MIN)],
  [12 * HOUR, ms(15 * MIN), ms(HOUR)],
  [36 * HOUR, ms(HOUR), ms(3 * HOUR)],
  [4 * DAY, ms(3 * HOUR), ms(12 * HOUR)],
  [10 * DAY, ms(6 * HOUR), ms(DAY)],
  [21 * DAY, ms(12 * HOUR), ms(DAY)],
  [90 * DAY, ms(DAY), { kind: "week" }],
  [Infinity, { kind: "week" }, { kind: "month" }],
];

const LONGEST: (typeof LADDER)[number] = [Infinity, { kind: "week" }, { kind: "month" }];

/**
 * Labels whose centres are closer than this drop alternate ones (midnights kept).
 * The spec says 80px; the canvas drops daily labels 86px apart (TimeAxis/ticks14d),
 * and the canvas is what's measured.
 */
export const LABEL_SPACING_PX = 100;

/** Every instant of `step` in [from, to], at round local times. */
function instants(from: number, to: number, step: Step): number[] {
  const out: number[] = [];
  const d = new Date(from);
  d.setHours(0, 0, 0, 0);
  if (step.kind === "week") {
    d.setDate(d.getDate() - ((d.getDay() + 6) % 7)); // back to Monday
    for (; d.getTime() <= to; d.setDate(d.getDate() + 7)) if (d.getTime() >= from) out.push(d.getTime());
    return out;
  }
  if (step.kind === "month") {
    d.setDate(1);
    for (; d.getTime() <= to; d.setMonth(d.getMonth() + 1)) if (d.getTime() >= from) out.push(d.getTime());
    return out;
  }
  if (step.ms >= DAY) {
    for (; d.getTime() <= to; d.setDate(d.getDate() + step.ms / DAY)) if (d.getTime() >= from) out.push(d.getTime());
    return out;
  }
  // Within a day: from local midnight in wall-clock steps (so 03:00 stays 03:00 across DST).
  for (let day = new Date(d); day.getTime() <= to; day.setDate(day.getDate() + 1)) {
    for (let m = 0; m < 24 * 60; m += step.ms / MIN) {
      const t = new Date(day);
      t.setMinutes(m);
      const v = t.getTime();
      if (v >= from && v <= to && v > (out.at(-1) ?? -Infinity)) out.push(v);
    }
  }
  return out;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const pad = (n: number) => String(n).padStart(2, "0");
const isMidnight = (t: number) => {
  const d = new Date(t);
  return d.getHours() === 0 && d.getMinutes() === 0;
};

export type AxisLabel = { t: number; text: string; midnight: boolean };
export type Ticks = { minor: number[]; major: number[]; day: number[]; labels: AxisLabel[] };

/** The ticks and labels for a window drawn `trackPx` wide. */
export function ticksFor(from: number, to: number, trackPx: number): Ticks {
  const span = Math.max(1, to - from);
  const [, minorStep, majorStep] = LADDER.find(([limit]) => span <= limit) ?? LONGEST;
  const minor = instants(from, to, minorStep);
  const major = instants(from, to, majorStep);
  const day = instants(from, to, ms(DAY));
  const px = (t: number) => ((t - from) / span) * trackPx;
  const [m0, m1] = major;
  const spacing = m0 !== undefined && m1 !== undefined ? px(m1) - px(m0) : Infinity;
  const every = Math.max(1, Math.ceil(LABEL_SPACING_PX / Math.max(spacing, 1)));
  // A time at the window's left edge goes unlabelled (§11O.3): centred, it would spill into the
  // label column. A date there is kept, aligned inward (CompDensity's axes).
  const labels = major
    .filter((t, i) => i % every === 0 || (every > 1 && isMidnight(t) && majorStep.kind === "ms" && majorStep.ms < DAY))
    .filter((t) => t !== from || isMidnight(t))
    .map((t) => {
      const d = new Date(t);
      const midnight = isMidnight(t);
      return { t, midnight, text: midnight ? `${MONTHS[d.getMonth()]} ${d.getDate()}` : `${pad(d.getHours())}:${pad(d.getMinutes())}` };
    });
  return { minor, major, day, labels };
}
