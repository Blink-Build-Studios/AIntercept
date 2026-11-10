import {
  FloatingPortal,
  autoUpdate,
  flip,
  offset,
  shift,
  useFloating,
  useMergeRefs,
  type ReferenceType,
} from "@floating-ui/react";
import {
  cloneElement,
  createContext,
  forwardRef,
  useCallback,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type CSSProperties,
  type ReactElement,
  type ReactNode,
  type Ref,
} from "react";
import "./Tooltip.css";

export type TooltipRow = { key: string; value: ReactNode; tone?: "danger" | "live" | "muted" };
/**
 * What a tooltip says. `lead`: a line of plain text first, with no wrapper (a full label,
 * `Organization 1b9f…e02`); `note`: a muted paragraph after it (CompProviders `.tipb`).
 */
export type TooltipContent = { lead?: string; title?: string; rows?: TooltipRow[]; body?: ReactNode; note?: ReactNode; hint?: string };
export type TooltipKind = "mark" | "cell" | "truncate" | "disabled";

/** Delays per kind (handoff §6B.2). `skip`: opens at once when another tooltip closed < SKIP_MS ago. */
export const TIMING: Record<TooltipKind, { open: number; close: number; hoverable: boolean; skip: boolean }> = {
  mark: { open: 120, close: 80, hoverable: false, skip: true },
  cell: { open: 400, close: 100, hoverable: true, skip: true },
  truncate: { open: 400, close: 100, hoverable: true, skip: true },
  disabled: { open: 400, close: 100, hoverable: true, skip: false },
};
export const SKIP_MS = 300;
const LONG_PRESS_MS = 500;

/** "—  reason": an unknown value in a tooltip row (§6B.1 Unknown). */
export const unknownValue = (reason: string) => (
  <>
    {"—  "}
    <span className="tip-reason">{reason}</span>
  </>
);

// --- the surface ------------------------------------------------------------------------------

type SurfaceProps = {
  content: TooltipContent;
  kind?: TooltipKind;
  id?: string;
  style?: CSSProperties;
  className?: string;
  onPointerEnter?: () => void;
  onPointerLeave?: () => void;
};

/** The tooltip box itself (CompTooltip `.tip`), with no behaviour. Stories render it open. */
export const TooltipSurface = forwardRef(function TooltipSurface(
  { content, kind = "cell", id, style, className = "", onPointerEnter, onPointerLeave }: SurfaceProps,
  ref: Ref<HTMLDivElement>,
) {
  const { lead, title, rows, body, note, hint } = content;
  return (
    <div
      ref={ref}
      id={id}
      role="tooltip"
      className={`tip ${kind === "truncate" ? "tip-truncate" : ""} ${className}`}
      style={style}
      onPointerEnter={onPointerEnter}
      onPointerLeave={onPointerLeave}
    >
      {lead}
      {title && <div className="tip-title">{title}</div>}
      {rows && rows.length > 0 && (
        <div className="tip-rows">
          {rows.map((r, i) => (
            <TooltipRowView key={`${r.key}-${i}`} row={r} />
          ))}
        </div>
      )}
      {body !== undefined && <div>{body}</div>}
      {note !== undefined && <div className="tip-note">{note}</div>}
      {hint && <div className="tip-hint">{hint}</div>}
    </div>
  );
});

function TooltipRowView({ row }: { row: TooltipRow }) {
  return (
    <>
      <span className="tip-key">{row.key}</span>
      <span className={row.tone ? `tip-${row.tone}` : undefined}>{row.value}</span>
    </>
  );
}

// --- one open tooltip at a time, with the neighbour skip-delay ---------------------------------

export type TooltipStore = {
  subscribe: (fn: () => void) => () => void;
  openId: () => string | null;
  open: (id: string) => void;
  close: (id: string) => void;
  /** Another tooltip is open, or one closed less than SKIP_MS ago. */
  warm: () => boolean;
};

export function createTooltipStore(now: () => number = () => performance.now()): TooltipStore {
  let current: string | null = null;
  let lastClose = -Infinity;
  const subs = new Set<() => void>();
  const emit = () => subs.forEach((fn) => fn());
  return {
    subscribe: (fn) => {
      subs.add(fn);
      return () => subs.delete(fn);
    },
    openId: () => current,
    open: (id) => {
      if (current === id) return;
      current = id;
      emit();
    },
    close: (id) => {
      if (current !== id) return;
      current = null;
      lastClose = now();
      emit();
    },
    warm: () => current !== null || now() - lastClose < SKIP_MS,
  };
}

const fallbackStore = createTooltipStore();
const StoreContext = createContext<TooltipStore>(fallbackStore);

/** One per app (handoff §6B.2): owns the skip-delay timer, so only one tooltip is open at a time. */
export function TooltipProvider({ children, store }: { children: ReactNode; store?: TooltipStore }) {
  const [own] = useState(() => store ?? createTooltipStore());
  return <StoreContext.Provider value={own}>{children}</StoreContext.Provider>;
}

// --- behaviour ----------------------------------------------------------------------------------

export type TooltipControls = {
  id: string;
  open: boolean;
  /** Pointer arrived: open after the kind's delay (at once when warm). */
  hoverStart: () => void;
  /** Pointer left: close after the kind's delay. */
  hoverEnd: () => void;
  /** Keyboard focus or a tap: open at once. */
  show: () => void;
  hide: () => void;
  /** For the surface: a hoverable tooltip stays open while the pointer is in it. */
  surfaceHandlers: { onPointerEnter?: () => void; onPointerLeave?: () => void };
};

/**
 * The open/close state of one tooltip (§6B.2 timing). `Tooltip` uses it for a
 * single trigger; charts use it directly with one tooltip per track whose
 * anchor moves mark to mark (`FloatingTooltip`'s `reference`).
 */
export function useTooltipControls(kind: TooltipKind, forced?: boolean): TooltipControls {
  const store = useContext(StoreContext);
  const id = useId();
  const isOpen = useSyncExternalStore(store.subscribe, () => store.openId() === id);
  const openTimer = useRef<ReturnType<typeof setTimeout>>();
  const closeTimer = useRef<ReturnType<typeof setTimeout>>();
  const t = TIMING[kind];

  const clear = () => {
    clearTimeout(openTimer.current);
    clearTimeout(closeTimer.current);
  };
  const show = useCallback(() => {
    clear();
    store.open(id);
  }, [store, id]);
  const hide = useCallback(() => {
    clear();
    store.close(id);
  }, [store, id]);
  const hoverStart = useCallback(() => {
    clearTimeout(closeTimer.current);
    if (store.openId() === id) return;
    if (t.skip && store.warm()) return show();
    clearTimeout(openTimer.current);
    openTimer.current = setTimeout(() => store.open(id), t.open);
  }, [store, id, t, show]);
  const hoverEnd = useCallback(() => {
    clearTimeout(openTimer.current);
    clearTimeout(closeTimer.current);
    closeTimer.current = setTimeout(() => store.close(id), t.close);
  }, [store, id, t]);

  useEffect(() => () => {
    clear();
    store.close(id);
  }, [store, id]);

  return {
    id,
    open: forced ?? isOpen,
    hoverStart,
    hoverEnd,
    show,
    hide,
    surfaceHandlers: t.hoverable ? { onPointerEnter: () => clearTimeout(closeTimer.current), onPointerLeave: hoverEnd } : {},
  };
}

type FloatingProps = {
  controls: TooltipControls;
  content: TooltipContent;
  kind: TooltipKind;
  placement?: "top" | "bottom";
  /** The anchor: an element, or a virtual box (a chart mark). */
  reference: ReferenceType | null;
  /** Where the portal mounts (default: the body). */
  root?: HTMLElement | null;
};

/** The positioned, portalled surface: 8px off the anchor, centred, flips and shifts (§6B.1 Placement). */
export function FloatingTooltip({ controls, content, kind, placement = "top", reference, root }: FloatingProps) {
  const { refs, floatingStyles } = useFloating({
    open: controls.open,
    placement,
    middleware: [offset(8), flip(), shift({ padding: 8 })],
    whileElementsMounted: autoUpdate,
  });
  useLayoutEffect(() => {
    if (reference instanceof Element) refs.setReference(reference);
    else refs.setPositionReference(reference);
  }, [refs, reference]);
  if (!controls.open || !reference) return null;
  return (
    <FloatingPortal root={root ?? undefined}>
      <TooltipSurface
        ref={refs.setFloating}
        id={`${controls.id}-tip`}
        content={content}
        kind={kind}
        style={floatingStyles}
        {...controls.surfaceHandlers}
      />
    </FloatingPortal>
  );
}

// --- the usual case: one trigger --------------------------------------------------------------

type TriggerProps = {
  onPointerEnter?: (e: React.PointerEvent) => void;
  onPointerLeave?: (e: React.PointerEvent) => void;
  onPointerDown?: (e: React.PointerEvent) => void;
  onPointerUp?: (e: React.PointerEvent) => void;
  onClick?: (e: React.MouseEvent) => void;
  onFocus?: (e: React.FocusEvent) => void;
  onBlur?: (e: React.FocusEvent) => void;
  onKeyDown?: (e: React.KeyboardEvent) => void;
  "aria-describedby"?: string;
};

export type TooltipProps = {
  /** null: no tooltip (the trigger renders as is). */
  content: TooltipContent | null;
  /** Sets the delays (§6B.2) and max width. */
  kind?: TooltipKind;
  /** Default top; flips and shifts automatically. */
  placement?: "top" | "bottom";
  /**
   * `mark`: a chart mark. The box centres on the mark but sits above the whole
   * timeline (§6B.1 Placement), so it moves mark to mark, never with the pointer.
   */
  anchor?: "element" | "mark";
  /** Forces it open (stories). */
  open?: boolean;
  /** The trigger: focusable, or inside a roving group. */
  children: ReactElement<TriggerProps & { tabIndex?: number }>;
};

// Plain elements become programmatically focusable (roving groups focus them; Tab still skips them).
const PLAIN = new Set(["span", "div", "dd", "dt", "li", "p", "td", "th"]);

// Keyboard focus shows the tooltip; a mouse click that focuses doesn't.
const focusVisible = (el: HTMLElement) => {
  try {
    return el.matches(":focus-visible");
  } catch {
    return true;
  }
};
const isTruncated = (el: HTMLElement) => el.scrollWidth > el.clientWidth || el.scrollHeight > el.clientHeight;

/**
 * Explains its trigger (handoff §6B.1–6B.3). Replaces every native `title`.
 * Shows on hover after the kind's delay, at once on keyboard focus; Esc closes
 * it and keeps focus (a second Esc reaches the trigger's own handler). Touch:
 * a mark opens on the first tap and clicks on the second; a cell opens on
 * long-press. `truncate` only opens when the trigger's text is cut off.
 */
export function Tooltip(props: TooltipProps) {
  return props.content ? <TooltipTrigger {...props} content={props.content} /> : props.children;
}

// A chart mark's anchor: its own x extent, and the whole timeline's height, so
// the box sits above the timeline (or below it when flipped), never over the lanes.
export const markAnchor = (el: HTMLElement) => ({
  contextElement: el,
  getBoundingClientRect: () => {
    const r = el.getBoundingClientRect();
    const tl = (el.closest(".tl") ?? el).getBoundingClientRect();
    return { x: r.x, y: tl.top, left: r.left, right: r.right, top: tl.top, bottom: tl.bottom, width: r.width, height: tl.height };
  },
});

function TooltipTrigger({ content, kind = "cell", placement = "top", anchor = "element", open, children }: TooltipProps & { content: TooltipContent }) {
  const controls = useTooltipControls(kind, open);
  const [el, setEl] = useState<HTMLElement | null>(null);
  const ref = useMergeRefs([setEl, (children as unknown as { ref?: Ref<HTMLElement> }).ref]);
  const touch = useRef({ tapOpened: false, press: undefined as ReturnType<typeof setTimeout> | undefined });
  const p = children.props;
  const reference = useMemo(() => (el && anchor === "mark" ? markAnchor(el) : el), [el, anchor]);

  const props: TriggerProps & { ref: Ref<HTMLElement>; tabIndex?: number; "data-tip": string } = {
    ref,
    "data-tip": kind,
    tabIndex: p.tabIndex ?? (typeof children.type === "string" && PLAIN.has(children.type) ? -1 : undefined),
    "aria-describedby": controls.open ? `${controls.id}-tip` : p["aria-describedby"],
    onPointerEnter: (e) => {
      p.onPointerEnter?.(e);
      if (e.pointerType === "touch") return;
      if (kind === "truncate" && !isTruncated(e.currentTarget as HTMLElement)) return;
      controls.hoverStart();
    },
    onPointerLeave: (e) => {
      p.onPointerLeave?.(e);
      clearTimeout(touch.current.press);
      if (e.pointerType !== "touch") controls.hoverEnd();
    },
    onPointerDown: (e) => {
      p.onPointerDown?.(e);
      if (e.pointerType !== "touch") return;
      if (kind === "mark") touch.current.tapOpened = !controls.open;
      else touch.current.press = setTimeout(controls.show, LONG_PRESS_MS);
    },
    onPointerUp: (e) => {
      p.onPointerUp?.(e);
      clearTimeout(touch.current.press);
    },
    onClick: (e) => {
      if (touch.current.tapOpened) {
        touch.current.tapOpened = false;
        controls.show();
        return;
      }
      p.onClick?.(e);
    },
    onFocus: (e) => {
      p.onFocus?.(e);
      const t = e.currentTarget as HTMLElement;
      if (!focusVisible(t)) return;
      if (kind === "truncate" && !isTruncated(t)) return;
      controls.show();
    },
    onBlur: (e) => {
      p.onBlur?.(e);
      controls.hide();
    },
    onKeyDown: (e) => {
      if (e.key === "Escape" && controls.open) {
        e.preventDefault();
        e.stopPropagation();
        controls.hide();
        return;
      }
      p.onKeyDown?.(e);
    },
  };

  return (
    <>
      {cloneElement(children, props)}
      <FloatingTooltip
        controls={controls}
        content={content}
        kind={kind}
        placement={placement}
        reference={reference}
        // Forced open (stories): mount inside the chart, where the canvas draws it.
        root={open && anchor === "mark" ? el?.closest("section") : undefined}
      />
    </>
  );
}
