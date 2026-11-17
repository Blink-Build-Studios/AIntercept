// Story-only: marks an element rendered deep inside a component as a parity
// specimen (`data-parity-self`), found by a selector under `within`'s first
// child. For specimens the story can't wrap: a hovered band, the axis readout.
import { useLayoutEffect, useRef, type ReactNode } from "react";

/** `illustrative`: the sheet marks this nested specimen illustrative, so it is left out of the measure on both sides. */
export type ParityTarget = { spec: string; selector: string; illustrative?: boolean };

export function ParityTags({ targets, children }: { targets: ParityTarget[]; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const root = ref.current;
    if (!root) return undefined;
    const tag = () => {
      for (const { spec, selector, illustrative } of targets) {
        const el = root.querySelector(selector);
        if (illustrative) el?.setAttribute("data-spec-illustrative", "true");
        else el?.setAttribute("data-parity-self", spec);
      }
    };
    tag();
    // Floating and late-mounted pieces (portals, measured states) appear after the first layout.
    const mo = new MutationObserver(tag);
    mo.observe(root, { childList: true, subtree: true });
    return () => mo.disconnect();
  }, [targets]);
  return (
    <div ref={ref} className="contents">
      {children}
    </div>
  );
}
