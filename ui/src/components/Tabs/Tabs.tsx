import { forwardRef, useLayoutEffect, useRef, useState } from "react";
import { formatCount } from "../../lib/format";
import "./Tabs.css";
import { Tooltip } from "../Tooltip/Tooltip";

export type TabItem = { id: string; label: string; count?: number | string; disabled?: boolean; disabledReason?: string };
export type TabsProps = {
  tabs: TabItem[];
  value: string;
  onChange: (id: string) => void;
  label: string;
  /** Inset the tabs to the 12px cell padding, at the top of a panel. */
  inset?: boolean;
  "data-force"?: "hover" | "focus";
};

export type TabProps = {
  tab: TabItem;
  selected: boolean;
  onSelect?: () => void;
  onKeyDown?: (e: React.KeyboardEvent) => void;
  "data-force"?: "hover" | "focus";
};

/** One tab: "Label · count" in a single run, ink with a 2px ink underline when selected. */
export const Tab = forwardRef<HTMLButtonElement, TabProps>(function Tab({ tab: t, selected, onSelect, onKeyDown, ...rest }, ref) {
  const count = t.count === undefined ? "" : ` · ${typeof t.count === "number" ? formatCount(t.count) : t.count}`;
  return (
    <Tooltip kind="disabled" content={t.disabled && t.disabledReason ? { body: t.disabledReason } : null}>
    <button
      ref={ref}
      type="button"
      role="tab"
      id={`tab-${t.id}`}
      aria-selected={selected}
      aria-disabled={t.disabled || undefined}
      tabIndex={selected ? 0 : -1}
      data-force={rest["data-force"]}
      className="tab type-label"
      onPointerDown={() => !t.disabled && onSelect?.()}
      onClick={() => !t.disabled && onSelect?.()}
      onKeyDown={onKeyDown}
    >
      {t.label}
      {count}
    </button>
    </Tooltip>
  );
});

/**
 * A tablist (handoff §6.1). Selection switches on pointer-down (§6A.1) and with
 * the arrow keys.
 */
export function Tabs({ tabs, value, onChange, label, inset, ...rest }: TabsProps) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const list = useRef<HTMLDivElement>(null);
  // Switching slides the underline from the old tab to the new over 150 ms (§11G.B3);
  // at rest the selected tab draws its own (the indicator only shows while moving).
  const prevValue = useRef(value);
  const [slide, setSlide] = useState<{ left: number; width: number } | null>(null);
  useLayoutEffect(() => {
    const from = refs.current[tabs.findIndex((t) => t.id === prevValue.current)];
    const to = refs.current[tabs.findIndex((t) => t.id === value)];
    prevValue.current = value;
    const box = list.current?.getBoundingClientRect();
    if (!from || !to || from === to || !box || matchMedia?.("(prefers-reduced-motion: reduce)").matches) return undefined;
    const rect = (el: HTMLElement) => ({ left: el.getBoundingClientRect().left - box.left, width: el.getBoundingClientRect().width });
    setSlide(rect(from));
    const raf = requestAnimationFrame(() => setSlide(rect(to)));
    const done = setTimeout(() => setSlide(null), 160);
    return () => {
      cancelAnimationFrame(raf);
      clearTimeout(done);
    };
  }, [value, tabs]);
  const enabled = tabs.filter((t) => !t.disabled);
  const move = (dir: 1 | -1) => {
    const i = enabled.findIndex((t) => t.id === value);
    const next = enabled[(i + dir + enabled.length) % enabled.length];
    if (next) {
      onChange(next.id);
      refs.current[tabs.indexOf(next)]?.focus();
    }
  };
  return (
    <div ref={list} role="tablist" aria-label={label} className={`tablist ${inset ? "tablist-inset" : ""}`} data-sliding={slide ? "" : undefined}>
      {slide && <span className="tab-indicator" aria-hidden style={{ left: slide.left, width: slide.width }} />}
      {tabs.map((t, i) => {
        const selected = t.id === value;
        return (
          <Tab
            key={t.id}
            ref={(el) => {
              refs.current[i] = el;
            }}
            tab={t}
            selected={selected}
            data-force={selected ? undefined : rest["data-force"]}
            onSelect={() => onChange(t.id)}
            onKeyDown={(e) => {
              if (e.key === "ArrowRight") move(1);
              if (e.key === "ArrowLeft") move(-1);
            }}
          />
        );
      })}
    </div>
  );
}
