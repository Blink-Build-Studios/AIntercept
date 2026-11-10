import "./HiddenCallsNote.css";

export type HiddenKind = "prewarm" | "title";

export type HiddenCallsNoteProps = {
  /** Hidden calls in this prompt, by kind; kinds with none are left out. */
  counts: Partial<Record<HiddenKind, number>>;
  /** Shows them, as quiet rows (§11F.1). */
  onShow: () => void;
};

/**
 * The faint line under a prompt's steps for the calls hidden by default (handoff §11J
 * gap 2): `+1 prewarm call hidden · show`. The whole line is the control.
 */
export function HiddenCallsNote({ counts, onShow }: HiddenCallsNoteProps) {
  const kinds = (["prewarm", "title"] as const).filter((k) => (counts[k] ?? 0) > 0);
  if (!kinds.length) return null;
  const text = kinds.map((k) => `+${counts[k]} ${k} ${counts[k] === 1 ? "call" : "calls"}`).join(" · ");
  return (
    <button type="button" onClick={onShow} className="hidden-calls-note text-ink-faint">
      {`${text} hidden · show`}
    </button>
  );
}
