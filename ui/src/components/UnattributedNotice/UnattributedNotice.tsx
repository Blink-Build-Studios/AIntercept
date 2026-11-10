import { formatCount } from "../../lib/format";
import { formatDay } from "../../lib/time";
import "./UnattributedNotice.css";

export type UnattributedNoticeProps = { sessions: number; modelCalls: number; firstSeen: string; reason: string; href: string };

/** Traffic no person could be tied to (handoff §6.3): shown above the People table when there is any. */
export function UnattributedNotice({ sessions, modelCalls, firstSeen, reason, href }: UnattributedNoticeProps) {
  return (
    <div role="note" className="unattributed border border-line bg-surface">
      <span className="type-data-strong text-ink">{`${formatCount(sessions)} ${sessions === 1 ? "session" : "sessions"} not tied to a person`}</span>
      <span className="type-body text-ink-muted">{`${formatDay(firstSeen)} · ${formatCount(modelCalls)} model calls · ${reason}`}</span>
      <span className="flex-1" />
      <a href={href} className="link type-data">
        view traffic
      </a>
    </div>
  );
}
