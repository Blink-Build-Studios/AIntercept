import { Tooltip, type TooltipContent } from "../Tooltip/Tooltip";
import "./PartialMark.css";

/** The partial-capture tooltip (§11G.D1): capture began at prompt `from`. */
export function partialCaptureTip(from: number): TooltipContent {
  const before = from > 2 ? `Prompts #1–#${from - 1} happened` : "Prompt #1 happened";
  return {
    title: "† Partial capture",
    body: `Capture began at prompt #${from}. ${before} before AIntercept was recording; tokens, calls and events start at #${from}.`,
  };
}

/**
 * The dagger after a session's prompt count when capture began mid-session
 * (handoff §11G.D1): `34†`. Muted, dotted underline, cursor help; the tooltip says
 * where capture began.
 */
export function PartialMark({ from, open }: { from: number; /** Forces the tooltip open (stories). */ open?: boolean }) {
  return (
    <Tooltip content={partialCaptureTip(from)} open={open}>
      <span className="partial-mark" role="img" aria-label={`partial capture: began at prompt #${from}`}>
        †
      </span>
    </Tooltip>
  );
}
