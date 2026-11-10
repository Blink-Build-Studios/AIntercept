import { useEffect, useRef, useState } from "react";
import { Button, type ButtonProps } from "../Button/Button";
import { InlineError } from "../InlineError/InlineError";

export type CopyState = "idle" | "copying" | "copied" | "failed";

export type CopyButtonProps = {
  label: string;
  /** The text to copy; may load it first (curl needs `/raw`), which shows "Copying…". */
  text: () => string | Promise<string>;
  variant?: ButtonProps["variant"];
  /** Pins a state, for stories. */
  force?: CopyState;
};

const COPIED_MS = 1500;
const FAILED_MS = 3000;

/**
 * Copies an id, a link or a curl command (handoff §6B.6, §6A.4): "Copying…"
 * while the text loads, "Copied ✓" for 1.5 s, or "Couldn't copy" beside it for 3 s.
 */
export function CopyButton({ label, text, variant = "icon", force }: CopyButtonProps) {
  const [state, setState] = useState<CopyState>("idle");
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const shown = force ?? state;
  const settle = (s: CopyState, ms: number) => {
    setState(s);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setState("idle"), ms);
  };
  const copy = async () => {
    setState("copying");
    try {
      await navigator.clipboard.writeText(await text());
      settle("copied", COPIED_MS);
    } catch {
      settle("failed", FAILED_MS);
    }
  };
  return (
    <span className="inline-flex items-center gap-2">
      <Button variant={variant} loading={shown === "copying"} loadingLabel="Copying…" onClick={copy}>
        {shown === "copied" ? "Copied ✓" : label}
      </Button>
      {shown === "failed" && <InlineError variant="beside" message="Couldn't copy" />}
    </span>
  );
}
