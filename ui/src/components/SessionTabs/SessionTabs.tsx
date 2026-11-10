import { useRef } from "react";
import { TabHint } from "./TabHint";
import "./SessionTabs.css";

/** A session tab: its label and the count with its unit ("1 · 203 steps", "203 calls · 412 other"). */
export type SessionTab = { id: string; label: string; count?: string | null };

export type SessionTabsProps = {
  tabs: SessionTab[];
  value: string;
  onChange: (id: string) => void;
  /** The active tab's TabHint (`tabHints`). */
  hint: string;
  label?: string;
};

/**
 * The session page's tabs (handoff §11L.7): each label carries its count and unit, so the
 * shared number links Prompts and Requests (`Prompts 1 · 203 steps`, `Requests 203 calls ·
 * 412 other`); the active tab is 600 with a 2px ink underline. One TabHint line under the
 * bar says what the active tab holds. Selection switches on pointer-down and with ←/→.
 */
export function SessionTabs({ tabs, value, onChange, hint, label = "Session" }: SessionTabsProps) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const move = (dir: 1 | -1) => {
    const i = tabs.findIndex((t) => t.id === value);
    const next = (i + dir + tabs.length) % tabs.length;
    const t = tabs[next];
    if (t) {
      onChange(t.id);
      refs.current[next]?.focus();
    }
  };
  return (
    <div className="session-tabset">
      <div role="tablist" aria-label={label} className="session-tablist">
        {tabs.map((t, i) => {
          const selected = t.id === value;
          return (
            <button
              key={t.id}
              ref={(el) => {
                refs.current[i] = el;
              }}
              type="button"
              role="tab"
              id={`session-tab-${t.id}`}
              aria-selected={selected}
              aria-describedby={selected ? "session-tab-hint" : undefined}
              tabIndex={selected ? 0 : -1}
              className="session-tab"
              onPointerDown={() => onChange(t.id)}
              onClick={() => onChange(t.id)}
              onKeyDown={(e) => {
                if (e.key === "ArrowRight") move(1);
                if (e.key === "ArrowLeft") move(-1);
              }}
            >
              <span className="session-tab-label">{t.label}</span>
              {t.count && (
                <>
                  {" "}
                  <span className="session-tab-count text-ink-muted">{t.count}</span>
                </>
              )}
            </button>
          );
        })}
      </div>
      <TabHint id="session-tab-hint">{hint}</TabHint>
    </div>
  );
}
