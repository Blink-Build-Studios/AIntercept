// Shared URL state (handoff §7): every page keeps its range, tab, filters, page and
// open drawer in the search params, so reload and Back / Forward restore them.
import { useCallback, useMemo } from "react";
import { useSearchParams } from "react-router-dom";

export type UrlState = {
  /** The brush range, ISO. */
  from?: string;
  to?: string;
  /** Window preset: 1h | 24h | 7d | 14d | all. */
  win?: string;
  tab?: string;
  q?: string;
  /** Repeatable filters. */
  kind: string[];
  agent: string[];
  page: number;
  /** Events toggles: show client internals (default off), collapse ticks (default on). */
  internal: boolean;
  ticks: boolean;
  /** The drawer: request id and tab. */
  req?: string;
  dtab?: string;
  /** "About this data" is open (`about=1`). */
  about: boolean;
  /** Requests include hidden calls, Codex's prewarm and title calls (`hidden=1`, §11J gap 2). */
  hidden: boolean;
  /** All requests' coverage filter: "false" = only calls the proxy didn't capture. */
  captured?: string;
  /** The prompt a timeline click jumped to (#N): the Prompts tab opens on its page. */
  prompt?: string;
};

const LISTS = ["kind", "agent"] as const;
const FLAGS = ["ticks"] as const; // on unless "0"
const OPT_IN = ["about", "internal", "hidden"] as const; // off unless "1"
const RANGE = ["from", "to", "win"] as const;

/** Parse the search params (pure, for tests). */
export function parseUrlState(p: URLSearchParams): UrlState {
  const str = (k: string) => p.get(k) ?? undefined;
  return {
    from: str("from"),
    to: str("to"),
    win: str("win"),
    tab: str("tab"),
    q: str("q"),
    kind: p.getAll("kind"),
    agent: p.getAll("agent"),
    page: Math.max(1, Number(p.get("page") ?? 1) || 1),
    internal: p.get("internal") === "1",
    ticks: p.get("ticks") !== "0",
    req: str("req"),
    dtab: str("dtab"),
    about: p.get("about") === "1",
    hidden: p.get("hidden") === "1",
    captured: str("captured"),
    prompt: str("prompt"),
  };
}

/** Apply a patch to the search params (pure, for tests). A new range, filter or tab resets `page`. */
export function patchUrlState(p: URLSearchParams, patch: Partial<UrlState>): URLSearchParams {
  const next = new URLSearchParams(p);
  for (const [k, v] of Object.entries(patch)) {
    next.delete(k);
    if (v === undefined || v === null || v === "") continue;
    if ((LISTS as readonly string[]).includes(k)) for (const x of v as string[]) next.append(k, x);
    else if ((FLAGS as readonly string[]).includes(k)) {
      if (v === false) next.set(k, "0");
    } else if ((OPT_IN as readonly string[]).includes(k)) {
      if (v === true) next.set(k, "1");
    } else if (k === "page") {
      if (Number(v) > 1) next.set(k, String(v));
    } else next.set(k, String(v));
  }
  const resets = Object.keys(patch).some((k) => (RANGE as readonly string[]).includes(k) || (LISTS as readonly string[]).includes(k) || k === "q" || k === "tab" || k === "captured" || k === "hidden");
  if (resets && !("page" in patch)) next.delete("page");
  // A jump holds only until the list moves: another page, range, filter or tab.
  if ((resets || "page" in patch) && !("prompt" in patch)) next.delete("prompt");
  return next;
}

/**
 * The page's URL state and a setter. Each change is a history entry (Back undoes
 * it); `replace` for changes that shouldn't be (a live range following now).
 */
export function useUrlState() {
  const [params, setParams] = useSearchParams();
  const state = useMemo(() => parseUrlState(params), [params]);
  const set = useCallback(
    (patch: Partial<UrlState>, opts: { replace?: boolean } = {}) => setParams((p) => patchUrlState(p, patch), { replace: opts.replace }),
    [setParams],
  );
  return [state, set] as const;
}
