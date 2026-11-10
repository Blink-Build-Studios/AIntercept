import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import normal from "../../api/samples/request/normal";
import rows from "../../api/samples/session_requests/many";
import { readyRegion } from "../../lib/region";
import { TooltipProvider } from "../Tooltip/Tooltip";
import { RequestDrawer } from "./RequestDrawer";



// The stylesheet as text, read from disk: Vitest stubs CSS imports, raw ones too. Tests run from ui/.
const fs = (await import(/* @vite-ignore */ `node:${"fs"}`)) as { readFileSync: (path: string, encoding: "utf8") => string };
const css = fs.readFileSync("src/components/RequestDrawer/RequestDrawer.css", "utf8");

const loading = { status: "loading", data: undefined } as const;

function renderDrawer(props: Partial<Parameters<typeof RequestDrawer>[0]> = {}) {
  const onClose = vi.fn();
  render(
    <TooltipProvider>
      <RequestDrawer
        requestId={normal.data.requestId}
        detail={readyRegion(normal.data)}
        raw={loading}
        details={loading}
        onRetry={() => {}}
        tab="summary"
        onTab={() => {}}
        onClose={onClose}
        onNavigate={() => {}}
        curl={async () => ""}
        {...props}
      />
    </TooltipProvider>,
  );
  return { onClose };
}

describe("RequestDrawer (handoff §6.6, §6A.4)", () => {
  it("[6A.11-6] The drawer opens with its header populated from row data before the request resolves", () => {
    const row = rows.data.find((r) => r.method && r.status === 200);
    if (!row) throw new Error("no 200 row in the sample");
    renderDrawer({ requestId: row.requestId, seed: row, detail: loading });
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent(`${row.method} ${row.path}`);
    expect(screen.getByRole("dialog")).toHaveTextContent(/^.*200/);
    expect(screen.getByRole("dialog").querySelector("[aria-busy]")).not.toBeNull();
  });

  it("[6A.11-9] Under reduced motion the drawer has no transform transition", () => {
    const reduced = /@media \(prefers-reduced-motion: reduce\)\s*\{([^}]*\{[^}]*\})+/.exec(css)?.[0] ?? "";
    // Reduced motion fades (80 ms, §11G.B3) with the scrim's opacity keyframes: no slide.
    expect(reduced).toMatch(/\.request-drawer[\s\S]*animation:\s*drawer-scrim-in 80ms/);
    expect(reduced).not.toMatch(/drawer-in |drawer-out /);
    // The slide's transforms live only in the drawer-in and drawer-out keyframes.
    expect(css.match(/transform:/g)).toHaveLength(2);
  });

  it("focuses the heading on open, and Esc closes it after the slide-out", () => {
    vi.useFakeTimers();
    try {
      const { onClose } = renderDrawer();
      expect(document.activeElement?.id).toBe("drawer-heading");
      fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
      expect(onClose).not.toHaveBeenCalled(); // the 150 ms slide-out runs first (§6A.7)
      vi.advanceTimersByTime(160);
      expect(onClose).toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});
