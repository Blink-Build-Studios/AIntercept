import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { Pagination } from "./Pagination";

/** A table that pages optimistically and rolls back on failure (§6A.4). */
function Paged({ fail }: { fail: boolean }) {
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  return (
    <>
      <Pagination
        page={page}
        pageCount={5}
        from={(page - 1) * 20 + 1}
        to={page * 20}
        total={100}
        loading={loading}
        onPage={(p) => {
          const prev = page;
          setPage(p);
          setLoading(true);
          setTimeout(() => {
            setLoading(false);
            if (fail) setPage(prev);
          }, 100);
        }}
      />
    </>
  );
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("Pagination", () => {
  it("[6A.11-7] disables with a reason while loading; on failure aria-current returns to the previous page", async () => {
    render(<Paged fail />);
    fireEvent.click(screen.getByRole("button", { name: "Page 2" }));
    expect(screen.getByText("2").closest("[aria-current]")).toHaveAttribute("aria-current", "page");
    const next = screen.getByRole("button", { name: "Next ›" });
    expect(next).toHaveAttribute("aria-disabled", "true");
    expect(next).not.toHaveAttribute("title");
    act(() => next.focus());
    expect(screen.getByRole("tooltip")).toHaveTextContent("Loading page 2…");
    act(() => next.blur());
    await vi.advanceTimersByTimeAsync(100);
    expect(screen.getByText("1").closest("[aria-current]")).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("button", { name: "Next ›" })).not.toHaveAttribute("aria-disabled");
  });
});
