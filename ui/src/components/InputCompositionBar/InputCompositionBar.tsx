import type { Schemas } from "../../api/types";
import { formatCount } from "../../lib/format";
import "./InputCompositionBar.css";

/**
 * What the request's input was made of (handoff §6.6): cached prefix and
 * uncached tokens as one bar, with the cache writes listed.
 */
export function InputCompositionBar({ tokens: t }: { tokens: Schemas["TokenCount"] }) {
  const read = t.cacheRead ?? 0;
  const fresh = t.input ?? 0;
  const write = t.cacheWrite ?? 0;
  const total = read + fresh + write;
  return (
    <div className="composition">
      <div className="flex justify-between">
        <span className="type-key text-ink-faint">Input composition · {formatCount(total)} tokens</span>
        <span className="composition-note text-ink-muted">context at this step</span>
      </div>
      <div
        className="composition-bar"
        role="img"
        aria-label={`${formatCount(read)} cached, ${formatCount(fresh)} uncached, ${formatCount(write)} written to cache`}
      >
        {read > 0 && <span className="composition-cached bg-ch-count" style={{ flexGrow: read }} />}
        {fresh > 0 && <span className="composition-fresh bg-ch-count" style={{ flexGrow: fresh }} />}
        {write > 0 && <span className="bg-ch-model" style={{ flexGrow: write }} />}
      </div>
      <div className="composition-legend text-ink-muted">
        <span>
          <span className="composition-cached text-ch-count">■</span> cache read {formatCount(read)}
        </span>
        <span>
          <span className="text-ch-count">■</span> uncached {formatCount(fresh)}
        </span>
        <span>
          cache write 5m {t.cacheWrite5m === null ? "—" : formatCount(t.cacheWrite5m)} · 1h {t.cacheWrite1h === null ? "—" : formatCount(t.cacheWrite1h)}
        </span>
      </div>
    </div>
  );
}
