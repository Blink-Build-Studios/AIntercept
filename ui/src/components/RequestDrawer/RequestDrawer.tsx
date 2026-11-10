import { useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { slowNotice, type Region } from "../../lib/region";
import { CopyButton } from "../CopyButton/CopyButton";
import { DetailsView } from "./DetailsView";
import { DrawerHeader } from "./DrawerHeader";
import { DrawerMeta } from "./DrawerMeta";
import { DrawerTabs, type DrawerTab } from "./DrawerTabs";
import { KeyStrip } from "./KeyStrip";
import { RawView } from "./RawView";
import { RegionState } from "../RegionState/RegionState";
import { SkeletonBar } from "../Skeleton/Skeleton";
import { SummaryView } from "./SummaryView";
import { channelOf, isCodexCall, keyCells, keyTiles, metaParts, rawKind, seedKind, statusLine, type Detail, type Details, type Raw, type Seed } from "./drawerData";
import { trapTab } from "../../lib/focusTrap";
import "./RequestDrawer.css";

export type RequestDrawerProps = {
  requestId: string;
  /** The clicked row: the header shows it at once, before the request resolves (§6A.4). */
  seed?: Seed;
  /** `/requests/:id`; while ↑ / ↓ load the next one it is `refetching` with the previous request. */
  detail: Region<Detail>;
  /** `/raw` and `/details`, loaded when their tab opens. */
  raw: Region<Raw>;
  details: Region<Details>;
  onRetry: (part: "detail" | "raw" | "details") => void;
  tab: DrawerTab;
  onTab: (t: DrawerTab) => void;
  onClose: () => void;
  /** ↑ / ↓ and `[` / `]`: the neighbouring request (the list row highlight follows). */
  onNavigate: (requestId: string) => void;
  /** Hovering the Raw tab prefetches `/raw`. */
  onRawHover?: () => void;
  /** The curl command (needs `/raw`, so it may load first: "Copying…"). */
  curl: () => Promise<string>;
  /** The link to this request (`?req=<id>&dtab=…`), for Copy link. */
  link?: string;
};

/** The tab body's first-load skeleton, at about a Summary's height. */
const BodySkeleton = () => (
  <div className="drawer-skeleton">
    <SkeletonBar width="40%" />
    <SkeletonBar width="70%" faint />
    <SkeletonBar width="55%" faint />
    <SkeletonBar width="62%" faint />
  </div>
);

/**
 * The request drawer (handoff §6.6, §6A.4): an 820px dialog over a scrim that
 * opens at once with the clicked row's header, then the request's meta line,
 * key strip, tabs and the tab body (each region through RegionState). ↑ / ↓
 * keep it open and dim the body until the next request lands. Esc closes;
 * focus starts on the heading and returns where it came from.
 */
export function RequestDrawer({ requestId, seed, detail, raw, details, onRetry, tab, onTab, onClose: close, onNavigate, onRawHover, curl, link }: RequestDrawerProps) {
  const d = detail.data;
  // Closing slides out (150 ms; a fade under reduced motion, §6A.7, §11G.B3) before the page unmounts it.
  const [closing, setClosing] = useState(false);
  const onClose = () => {
    if (closing) return;
    setClosing(true);
    const reduced = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
    setTimeout(close, reduced ? 80 : 150);
  };
  const fresh = d?.requestId === requestId;

  // Focus the heading on open; give focus back to where it came from on close.
  const opener = useRef<Element | null>(null);
  useLayoutEffect(() => {
    opener.current = document.activeElement;
    document.getElementById("drawer-heading")?.focus();
    return () => (opener.current instanceof HTMLElement ? opener.current.focus() : undefined);
  }, []);

  const prev = fresh && d.prevRequestId ? () => onNavigate(d.prevRequestId as string) : undefined;
  const next = fresh && d.nextRequestId ? () => onNavigate(d.nextRequestId as string) : undefined;
  const onKey = (e: KeyboardEvent<HTMLElement>) => {
    // A tooltip's Esc closes the tooltip first and stops there (§6B.3).
    if (e.key === "Escape") return onClose();
    if (!(e.target instanceof HTMLInputElement)) {
      if (e.key === "[") prev?.();
      if (e.key === "]") next?.();
    }
    trapTab(e);
  };

  // The header: the request once it's here, else the clicked row.
  const facts = fresh ? d : seed?.requestId === requestId ? { ...seed, kind: seedKind(seed), notice: null, retry: null } : null;
  const channel = seed?.requestId === requestId ? seed.kind : facts ? channelOf(facts.path) : null;

  const region = (r: Region<unknown>, part: "detail" | "raw" | "details", label: string, children: ReactNode) => (
    <RegionState
      status={r.status}
      slow={slowNotice(r, label)}
      hasData={r.data !== undefined}
      skeleton={<BodySkeleton />}
      error={r.error && { message: r.error.message, queryId: r.error.queryId, onRetry: () => onRetry(part) }}
      label={label}
    >
      {children}
    </RegionState>
  );
  let body: ReactNode;
  if (!d || tab === "summary") body = region(detail, "detail", "this request", d && <SummaryView detail={d} />);
  else if (tab === "raw") body = region(fresh ? raw : { status: "loading", data: undefined }, "raw", "the raw request", raw.data && <RawView raw={raw.data} />);
  else body = region(fresh ? details : { status: "loading", data: undefined }, "details", "all details", details.data && <DetailsView detail={d} details={details.data} />);

  return (
    <>
      <div className={`drawer-scrim ${closing ? "drawer-closing" : ""}`} onClick={onClose} aria-hidden />
      {/* The dialog owns its keys: Esc, [ / ], and the focus trap. */}
      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions */}
      <section role="dialog" aria-modal="true" aria-labelledby="drawer-heading" className={`request-drawer bg-surface ${closing ? "drawer-closing" : ""}`} onKeyDown={onKey}>
        <DrawerHeader
          channel={channel}
          method={facts?.method ?? null}
          path={facts?.path ?? requestId}
          status={facts ? statusLine(facts) : ""}
          kind={facts?.kind ?? "normal"}
          actions={
            <>
              <CopyButton label="Copy as curl" text={curl} />
              {link && <CopyButton label="Copy link" text={() => link} />}
            </>
          }
          onPrev={prev}
          onNext={next}
          onClose={onClose}
        />
        {fresh ? (
          <>
            <DrawerMeta parts={metaParts(d)} />
            {isCodexCall(d) ? <KeyStrip tiles={keyTiles(d)} /> : <KeyStrip cells={keyCells(d)} />}
          </>
        ) : (
          <div className="drawer-meta-skeleton">
            <SkeletonBar width="50%" faint />
          </div>
        )}
        <DrawerTabs value={tab} onChange={onTab} raw={fresh ? rawKind(d) : "full"} onRawHover={fresh ? onRawHover : undefined} />
        <div className="drawer-body">{body}</div>
      </section>
    </>
  );
}
