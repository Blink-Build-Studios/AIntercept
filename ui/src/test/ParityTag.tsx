// Story-only: marks an element rendered deep inside a component as a parity
// specimen (`data-parity-self`), found by a selector under `within`'s first
// child. For specimens the story can't wrap: a hovered band, the axis readout.
import { useLayoutEffect, useRef, type ReactNode } from "react";

export type ParityTarget = { spec: string; selector: string };

export function ParityTags({ targets, children }: { targets: ParityTarget[]; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const root = ref.current;
    if (!root) return undefined;
    const tag = () => {
      for (const { spec, selector } of targets) root.querySelector(selector)?.setAttribute("data-parity-self", spec);
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
