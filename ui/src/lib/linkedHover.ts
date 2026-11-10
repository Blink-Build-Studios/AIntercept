// Linked highlighting between tables and the timeline (handoff §6B.5): one
// client store, written on hover and keyboard focus, cleared on leave. Hovering
// never scrolls; only rows on screen highlight.
import { createContext, createElement, useContext, useState, useSyncExternalStore, type ReactNode } from "react";

export type LinkedHover = {
  source: "table" | "timeline";
  /** An instant (request, event, step, prompt tick). */
  t?: string;
  /** A span (session band, bin). */
  span?: [string, string];
  /** What is pointed at, so the other side can find it (session ids, request ids, prompt ids…). */
  ids?: string[];
} | null;

export type LinkedHoverStore = {
  get: () => LinkedHover;
  set: (v: LinkedHover) => void;
  subscribe: (fn: () => void) => () => void;
};

export function createLinkedHoverStore(): LinkedHoverStore {
  let value: LinkedHover = null;
  const subs = new Set<() => void>();
  return {
    get: () => value,
    set: (v) => {
      value = v;
      subs.forEach((fn) => fn());
    },
    subscribe: (fn) => {
      subs.add(fn);
      return () => subs.delete(fn);
    },
  };
}

const fallback = createLinkedHoverStore();
const Ctx = createContext<LinkedHoverStore>(fallback);

/** One per page: the table and the timeline under it share it. */
export function LinkedHoverProvider({ children, store }: { children: ReactNode; store?: LinkedHoverStore }) {
  const [own] = useState(() => store ?? createLinkedHoverStore());
  return createElement(Ctx.Provider, { value: own }, children);
}

/** The current linked hover and its setter. Re-renders on every change: prefer
 * useLinkedSelect (what this component shows) and useLinkedSet (writing only). */
export function useLinkedHover(): [LinkedHover, (v: LinkedHover) => void] {
  const store = useContext(Ctx);
  const value = useSyncExternalStore(store.subscribe, store.get);
  return [value, store.set];
}

/** The setter alone: publishing a hover doesn't re-render the publisher. */
export function useLinkedSet(): (v: LinkedHover) => void {
  return useContext(Ctx).set;
}

/**
 * What this component shows of the linked hover (`select` returns a boolean or
 * other primitive): it re-renders only when that changes, not on every hover.
 * A timeline of thousands of marks and tables of rows each re-rendering on
 * every pointer move was the hover lag.
 */
export function useLinkedSelect<T extends boolean | string | number | null>(select: (v: LinkedHover) => T): T {
  const store = useContext(Ctx);
  return useSyncExternalStore(store.subscribe, () => select(store.get()));
}

/** True when the other side points at `id` (a row reacting to the timeline, or a mark to a row). */
export function isLinked(v: LinkedHover, from: "table" | "timeline", id: string): boolean {
  return !!v && v.source !== from && !!v.ids?.includes(id);
}

/** Pointer and focus handlers that publish `value` while the element is hovered or focused. */
export function linkHandlers(set: (v: LinkedHover) => void, value: Exclude<LinkedHover, null>) {
  const on = () => set(value);
  const off = () => set(null);
  return { onPointerEnter: on, onPointerLeave: off, onFocus: on, onBlur: off };
}

/**
 * A row reacts to the timeline (§6B.5): one of its ids is pointed at (a band,
 * a prompt tick), or, for an instant row, its time falls inside the pointed span (a bin).
 */
export function rowLinked(v: LinkedHover, row: { id: string; t?: string }): boolean {
  if (!v || v.source !== "timeline") return false;
  if (v.ids?.includes(row.id)) return true;
  if (row.t && v.span) {
    const t = Date.parse(row.t);
    return t >= Date.parse(v.span[0]) && t < Date.parse(v.span[1]);
  }
  return false;
}

/** A table row's side of linked hover: the handlers that publish it, and whether the timeline points at it. */
export function useRowLink(publish: Exclude<LinkedHover, null>, row: { id: string; t?: string }) {
  const linked = useLinkedSelect((v) => rowLinked(v, row));
  return { linked, handlers: linkHandlers(useLinkedSet(), publish) };
}
