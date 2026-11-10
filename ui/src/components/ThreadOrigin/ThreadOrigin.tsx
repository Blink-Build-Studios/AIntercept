import "./ThreadOrigin.css";

/** How a thread began, when not fresh (handoff §11J gaps 3 and 4); `new` shows nothing. */
export type ThreadOriginData =
  | { kind: "fork"; threadShort: string; turn: number; init: "warm_fork" | "cold_fork"; href: string }
  | { kind: "resume"; threadShort: string; init: "cold_resume"; href: string }
  | { kind: "subagent" | "review"; threadShort: string; turn: number | null; init: "thread_spawn" | "review"; href: string };

const INIT = { warm_fork: "warm fork", cold_fork: "cold fork", cold_resume: "cold resume", thread_spawn: "thread_spawn", review: "review" };

/**
 * A thread's origin in its SessionFacts (handoff §11J gap 4): `origin fork of <thread> at
 * turn #N · warm fork`, `resumed from <thread> · cold resume`, or `subagent of <parent> ·
 * turn #N · thread_spawn`, the thread linking to its page at that turn.
 */
export function ThreadOrigin({ origin: o }: { origin: ThreadOriginData }) {
  const lead = o.kind === "fork" ? "fork of " : o.kind === "resume" ? "resumed from " : o.kind === "review" ? "review of " : "subagent of ";
  const turn = o.kind === "fork" ? ` at turn #${o.turn}` : o.kind !== "resume" && o.turn !== null ? ` · turn #${o.turn}` : "";
  return (
    <span className="thread-origin">
      <span className="type-key text-ink-faint">origin</span>{" "}
      <a href={o.href} className="thread-origin-link text-ink-muted">{`${lead}${o.threadShort}`}</a>
      {`${turn} · ${INIT[o.init]}`}
    </span>
  );
}
