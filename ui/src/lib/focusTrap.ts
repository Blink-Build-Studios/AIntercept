import type { KeyboardEvent } from "react";

/** Tab cycles inside a dialog (§3.4: drawers trap focus). Call from the dialog's onKeyDown. */
export function trapTab(e: KeyboardEvent<HTMLElement>) {
  if (e.key !== "Tab") return;
  const all = [...e.currentTarget.querySelectorAll<HTMLElement>('a[href], button, input, [tabindex="0"]')];
  const first = all[0];
  const last = all[all.length - 1];
  if (!first || !last) return;
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault();
    first.focus();
  }
}
