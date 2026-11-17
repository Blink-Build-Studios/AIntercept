import { act, fireEvent, render, screen } from "@testing-library/react";
import demo from "../../api/samples/person_requests/demo";
import { DataTable } from "../DataTable/DataTable";
import { RequestRow, requestColumns, requestMinWidth, type Request } from "../RequestRow/RequestRow";
import { NewRowsPill } from "./NewRowsPill";
import { useHeldRows } from "./useHeldRows";

function LiveRequests({ rows, following }: { rows: Request[]; following: boolean }) {
  const held = useHeldRows(rows, following);
  return (
    <div data-testid="scroller" style={{ height: 200, overflow: "auto" }}>
      <NewRowsPill count={held.pending} direction="down" onClick={held.reveal} />
      <DataTable columns={requestColumns(false)} minWidth={requestMinWidth(false)} label="Requests">
        {held.rows.map((r) => (
          <RequestRow key={r.requestId} row={r} onOpen={() => {}} />
        ))}
      </DataTable>
    </div>
  );
}

const all: Request[] = demo.data;
const firstRowName = () => screen.getAllByRole("row")[1]?.textContent;

describe("NewRowsPill (§6A.9)", () => {
  it("[6A.11-10] A live append while scrolled away shows NewRowsPill and doesn't move visible rows", () => {
    const base = all.slice(0, 20);
    const { rerender } = render(<LiveRequests rows={base} following={false} />);
    const scroller = screen.getByTestId("scroller");
    act(() => {
      scroller.scrollTop = 120;
    });
    const before = { top: scroller.scrollTop, first: firstRowName(), count: screen.getAllByRole("row").length };
    expect(screen.queryByText(/NEW/)).toBeNull();

    rerender(<LiveRequests rows={all.slice(0, 23)} following={false} />);

    expect(screen.getByRole("button", { name: /3 NEW ↓/ })).toBeTruthy();
    expect(scroller.scrollTop).toBe(before.top);
    expect(firstRowName()).toBe(before.first);
    expect(screen.getAllByRole("row")).toHaveLength(before.count);

    // Clicking the pill shows the held rows and clears the count.
    fireEvent.click(screen.getByRole("button", { name: /3 NEW ↓/ }));
    expect(screen.getAllByRole("row")).toHaveLength(before.count + 3);
    expect(screen.queryByText(/NEW/)).toBeNull();
  });

  it("inserts directly while following the edge", () => {
    const { rerender } = render(<LiveRequests rows={all.slice(0, 5)} following />);
    rerender(<LiveRequests rows={all.slice(0, 8)} following />);
    expect(screen.getAllByRole("row")).toHaveLength(1 + 8);
    expect(screen.queryByText(/NEW/)).toBeNull();
  });
});
