// The §12 contract test: unknown values (null in the API) render as a dash
// with a reason, never as 0.
import { render, screen } from "@testing-library/react";
import { HeaderStat } from "./HeaderStat";
import { StatTile } from "../StatTile/StatTile";

describe("unknown values", () => {
  it("[12-null] StatTile and HeaderStat render null as a dash with a reason, never 0", () => {
    const { container } = render(
      <>
        <StatTile label="Tool calls" value={null} unknownReason="not extracted yet" />
        <HeaderStat label="Role" value={null} unknownReason="not extracted yet" />
      </>,
    );
    const dashes = screen.getAllByLabelText("unknown: not extracted yet");
    expect(dashes).toHaveLength(2);
    dashes.forEach((d) => expect(d).toHaveTextContent("—"));
    expect(container.textContent).not.toMatch(/\b0\b/);
  });

  it("a real zero still renders as 0", () => {
    render(<StatTile label="Account calls" value={0} />);
    expect(screen.getByText("0")).toBeInTheDocument();
  });
});
