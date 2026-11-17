// Older sheets' friction rows, for their parity specimens only: CompDensity's Lane/frictionSplit
// and CompAgents' Lane/codexFriction still draw the three analytics-era rows (▲ in danger). The
// pages show §11S's two rows (FrictionTrack's FRICTION_KINDS).
import type { FrictionKindRow } from "./FrictionTrack";

export const SPLIT_FRICTION_KINDS: FrictionKindRow[] = [
  { kind: "frustrated", glyph: "↯", name: "frustrated" },
  { kind: "interrupted", glyph: "✕", name: "interrupted" },
  { kind: "destructive", glyph: "▲", name: "destructive", tone: "danger" },
];

export const CODEX_FRICTION_KINDS: FrictionKindRow[] = [
  { kind: "interrupted", glyph: "✕", name: "interrupted" },
  { kind: "destructive", glyph: "▲", name: "destructive", tone: "danger" },
  { kind: "steered", glyph: "↻", name: "steered" },
];
