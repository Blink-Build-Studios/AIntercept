import { useRef } from "react";
import { Tab } from "../Tabs/Tabs";
import "../Tabs/Tabs.css";
import "./DrawerTabs.css";

export type DrawerTab = "summary" | "raw" | "all";

export type DrawerTabsProps = {
  value: DrawerTab;
  onChange: (t: DrawerTab) => void;
  /** Raw relabels when there is no wire data: "Raw · none" (not captured), "Raw · request only" (failed). */
  raw?: "full" | "none" | "requestOnly";
  /** Hovering Raw prefetches `/raw` (§6A.4). */
  onRawHover?: () => void;
};

const RAW_LABEL = {
  full: "Raw",
  none: "Raw · none",
  requestOnly: "Raw · request only",
};
const ORDER: DrawerTab[] = ["summary", "raw", "all"];

/**
 * Summary · Raw · All details (handoff §6.6): the Tabs underline tabs, 20px
 * apart. Alone it is a bordered box (CompDrawer); in the drawer, a bottom rule.
 */
export function DrawerTabs({ value, onChange, raw = "full", onRawHover }: DrawerTabsProps) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const labels = {
    summary: "Summary",
    raw: RAW_LABEL[raw],
    all: "All details",
  };
  const move = (dir: 1 | -1) => {
    const i = (ORDER.indexOf(value) + dir + ORDER.length) % ORDER.length;
    onChange(ORDER[i] ?? "summary");
    refs.current[i]?.focus();
  };
  return (
    <div role="tablist" aria-label="Request detail" className="drawer-tabs border border-line bg-surface">
      {ORDER.map((id, i) => (
        <span key={id} className="contents" onPointerEnter={id === "raw" ? onRawHover : undefined}>
          <Tab
            ref={(el) => {
              refs.current[i] = el;
            }}
            tab={{ id: `drawer-${id}`, label: labels[id] }}
            selected={value === id}
            onSelect={() => onChange(id)}
            onKeyDown={(e) => {
              if (e.key === "ArrowRight") move(1);
              if (e.key === "ArrowLeft") move(-1);
            }}
          />
        </span>
      ))}
    </div>
  );
}
