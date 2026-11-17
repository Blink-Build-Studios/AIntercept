// Hover and click for every lane (handoff §6B.4), from one timeline payload:
// each lane's tooltip builder and click result. Pages pass the click results
// (select a range, open a session or request, jump to a prompt); stories pass fakes.
import type { Schemas, SessionTimeline } from "../../api/types";
import type { BandInteraction } from "./BandTrack";
import type { BinInteraction, BinSlot } from "./BarTrack";
import type { FrictionInteraction } from "./FrictionTrack";
import type { MarkLabel, MarkerInteraction } from "./MarkerTrack";
import type { TurnInteraction } from "./TurnTrack";
import { ms } from "./scale";
import {
  compactionTip,
  contextTip,
  frictionClusterTip,
  modelBinTip,
  promptTip,
  turnTip,
  workBinTip,
  sessionBandTip,
} from "./timelineTips";

export type TimelineActions = {
  /** Select a range (a bin, a segment, a gap, brief changes). */
  select: (r: { from: string; to: string }) => void;
  openSession?: (sessionId: string) => void;
  openRequest?: (requestId: string) => void;
  /** Prompts tab: scroll to, expand and select the prompt. */
  /** Show prompt #index in the Prompts tab; `t` (its time) lets the page drop a range that excludes it. */
  jumpToPrompt?: (index: number, t?: string) => void;
};

type AnyTimeline = Schemas["Timeline"] | SessionTimeline;

const byT = <T extends { t: string }>(xs: T[]) => new Map(xs.map((x) => [ms(x.t), x]));
const binLabel = (what: string) => (s: BinSlot) => `${s.from} to ${s.to}: ${s.bin?.v ?? 0} ${what}`;

/** The lanes both timelines share (person and session pages). */
export function laneInteractions(d: AnyTimeline, on: TimelineActions) {
  const calls = byT(d.modelCalls);
  const prompts = byT(d.prompts);
  const sessions = new Map(d.sessions.map((s) => [s.sessionId, s]));
  const selectBin = (s: BinSlot) => on.select({ from: s.from, to: s.to });
  const work = byT(d.work);
  return {
    work: {
      tip: (s) => workBinTip(s.from, s.to, work.get(ms(s.from)), d.binSeconds),
      label: (s) => `${s.from} to ${s.to}: ${work.get(ms(s.from))?.steps ?? 0} steps`,
      onSelect: selectBin,
    } satisfies BinInteraction,
    modelCalls: {
      tip: (s) => modelBinTip(s.from, s.to, calls.get(ms(s.from)), d.binSeconds),
      label: binLabel("model calls"),
      onSelect: selectBin,
    } satisfies BinInteraction,
    bands: {
      tip: (b) => {
        const s = sessions.get(b.id);
        return s ? sessionBandTip(s, d.now ?? undefined) : { title: b.label };
      },
      onOpen: on.openSession && ((b) => on.openSession?.(b.id)),
    } satisfies BandInteraction,
    prompts: {
      tip: (m) => {
        const p = prompts.get(ms(m.t));
        return p ? promptTip(p) : null;
      },
      onActivate: (m) => {
        const index = prompts.get(ms(m.t))?.index;
        if (index !== null && index !== undefined) on.jumpToPrompt?.(index, m.t);
      },
    } satisfies MarkerInteraction,
    friction: {
      tip: (c) => frictionClusterTip(c.marks),
      // A cluster selects its span (a lone mark, its minute) and jumps to its first prompt.
      onActivate: (c) => {
        const last = c.marks.at(-1)?.t ?? c.t;
        on.select({ from: c.t, to: ms(last) > ms(c.t) ? last : new Date(ms(c.t) + 60_000).toISOString() });
        const index = c.marks.find((m) => m.promptIndex !== null)?.promptIndex;
        if (index !== null && index !== undefined) on.jumpToPrompt?.(index);
      },
    } satisfies FrictionInteraction,
  };
}

/** The session page's own lanes: context and compactions. */
export function sessionInteractions(d: SessionTimeline, on: TimelineActions) {
  const context = d.context.map((c) => ({ c, t: ms(c.t) })).sort((a, b) => a.t - b.t);
  const compactions = new Map(d.compactions.map((c) => [ms(c.t), c]));
  // The context bar of a bin is its last call's, carried forward through bins without calls:
  // the last point before the bin's end (a binary search; this runs for every bin's tooltip).
  const pointFor = (s: BinSlot) => {
    const end = ms(s.to);
    let lo = 0;
    let hi = context.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if ((context[mid]?.t ?? Infinity) < end) lo = mid + 1;
      else hi = mid;
    }
    return context[lo - 1]?.c;
  };
  const turns = new Map(d.turns.map((t) => [t.promptId, t]));
  return {
    turns: {
      tip: (b) => {
        const t = turns.get(b.id);
        return t ? turnTip(t, d.now ?? d.to) : null;
      },
      onActivate: (b) => {
        if (b.index !== null) on.jumpToPrompt?.(b.index, b.from);
      },
    } satisfies TurnInteraction,
    context: {
      tip: (s) => {
        const c = pointFor(s);
        return c ? contextTip(c) : null;
      },
      label: (s) => `${s.from}: context ${s.bin?.v ?? 0} tokens`,
      onSelect: on.openRequest && ((s) => {
        const c = pointFor(s);
        if (c) on.openRequest?.(c.requestId);
      }),
    } satisfies BinInteraction,
    compactionTip: (l: MarkLabel) => {
      const c = compactions.get(ms(l.t));
      return c ? compactionTip(c) : null;
    },
  };
}
