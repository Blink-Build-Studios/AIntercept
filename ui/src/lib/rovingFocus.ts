// Roving focus (handoff §6B.8): a group (header stats, timeline, table) is one
// tab stop; arrow keys move inside it. Items are the group's descendants marked
// `data-roving` (their order in the DOM is their order in the group); exactly
// one has tabIndex 0, the rest -1.
import { useCallback, useEffect, useRef, type KeyboardEvent, type RefObject } from "react";

export type RovingGroup = "timeline" | "table" | "stats";

export type RovingOptions = {
  group: RovingGroup;
  orientation: "horizontal" | "vertical";
  /**
   * Handles a key before the defaults; return the index to focus, `null` to
   * mark the key handled without moving, or `undefined` to fall through.
   */
  onKey?: (e: KeyboardEvent, index: number, items: HTMLElement[]) => number | null | undefined;
  /** The item that takes the tab stop when the group has no focused item yet (default 0). */
  initial?: (items: HTMLElement[]) => number;
};

export type Roving = {
  ref: RefObject<HTMLDivElement>;
  props: { "data-roving-group": RovingGroup; onKeyDown: (e: KeyboardEvent) => void; onFocus: (e: React.FocusEvent) => void };
  /** Focus the item at `index` (clamped). */
  focusAt: (index: number) => void;
};

const itemsOf = (root: HTMLElement | null): HTMLElement[] =>
  root ? [...root.querySelectorAll<HTMLElement>("[data-roving]")].filter((el) => el.closest("[data-roving-group]") === root) : [];

export function useRovingFocus({ group, orientation, onKey, initial }: RovingOptions): Roving {
  const ref = useRef<HTMLDivElement>(null);
  const current = useRef<HTMLElement | null>(null);

  const sync = useCallback(() => {
    const items = itemsOf(ref.current);
    if (!items.length) return;
    const active = current.current && items.includes(current.current) ? current.current : items[initial?.(items) ?? 0];
    for (const el of items) el.tabIndex = el === active ? 0 : -1;
  }, [initial]);

  // Items come and go with the data (paging, live appends): keep exactly one tab stop.
  useEffect(() => {
    sync();
    const root = ref.current;
    if (!root) return undefined;
    // One rescan per frame, however many nodes changed (a timeline has thousands of items).
    let frame = 0;
    const mo = new MutationObserver(() => {
      if (!frame) frame = requestAnimationFrame(() => ((frame = 0), sync()));
    });
    mo.observe(root, { childList: true, subtree: true });
    return () => {
      mo.disconnect();
      cancelAnimationFrame(frame);
    };
  }, [sync]);

  const focusAt = useCallback((index: number) => {
    const items = itemsOf(ref.current);
    const el = items[Math.max(0, Math.min(items.length - 1, index))];
    if (!el) return;
    current.current = el;
    for (const it of items) it.tabIndex = it === el ? 0 : -1;
    el.focus();
  }, []);

  const onKeyDown = (e: KeyboardEvent) => {
    const items = itemsOf(ref.current);
    const index = items.indexOf(e.target as HTMLElement);
    if (index < 0) return;
    const custom = onKey?.(e, index, items);
    if (custom !== undefined) {
      e.preventDefault();
      if (custom !== null) focusAt(custom);
      return;
    }
    const [prev, next] = orientation === "horizontal" ? ["ArrowLeft", "ArrowRight"] : ["ArrowUp", "ArrowDown"];
    const to =
      e.key === prev ? index - 1 : e.key === next ? index + 1 : e.key === "Home" ? 0 : e.key === "End" ? items.length - 1 : undefined;
    if (to === undefined) {
      // Enter on an item that isn't natively clickable runs its click result.
      if (e.key === "Enter" && !(e.target instanceof HTMLButtonElement || e.target instanceof HTMLAnchorElement)) {
        e.preventDefault();
        (e.target as HTMLElement).click();
      }
      return;
    }
    e.preventDefault();
    focusAt(to);
  };

  const onFocus = (e: React.FocusEvent) => {
    const items = itemsOf(ref.current);
    if (items.includes(e.target as HTMLElement)) current.current = e.target as HTMLElement;
  };

  return { ref, props: { "data-roving-group": group, onKeyDown, onFocus }, focusAt };
}
