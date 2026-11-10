import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { Button } from "./Button";

function Retry({ onRetry }: { onRetry: () => void }) {
  const [busy, setBusy] = useState(false);
  return (
    <Button
      loading={busy}
      loadingLabel="Retrying…"
      onClick={() => {
        setBusy(true);
        onRetry();
      }}
    >
      Retry now
    </Button>
  );
}

describe("Button", () => {
  it("[6A.11-1] Retry now is aria-busy in the same frame; a second click is ignored", () => {
    const onRetry = vi.fn();
    render(<Retry onRetry={onRetry} />);
    const button = screen.getByRole("button", { name: /retry now/i });
    fireEvent.click(button);
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(button).toHaveTextContent("Retrying…");
    fireEvent.click(button);
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("explains why it's disabled in a tooltip, not a title", () => {
    render(
      <Button disabled disabledReason="Loading page 3…">
        Next
      </Button>,
    );
    const button = screen.getByRole("button");
    expect(button).not.toHaveAttribute("title");
    act(() => button.focus());
    expect(screen.getByRole("tooltip")).toHaveTextContent("Loading page 3…");
  });
});
