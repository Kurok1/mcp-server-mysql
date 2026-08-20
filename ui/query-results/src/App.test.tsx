/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

describe("query results app", () => {
  const writeText = vi.fn().mockResolvedValue(undefined);

  beforeEach(() => {
    writeText.mockClear();
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    window.history.replaceState({}, "", "/?locale=en&theme=light");
  });

  afterEach(cleanup);

  it("filters, sorts, hides columns and selects rows", () => {
    render(<App />);
    expect(screen.getByRole("heading", { name: "accounts" })).toBeInTheDocument();

    fireEvent.change(screen.getByPlaceholderText("Filter 10 rows…"), { target: { value: "Kite" } });
    expect(screen.getByText("Kite Robotics")).toBeInTheDocument();
    expect(screen.queryByText("Acme Labs")).not.toBeInTheDocument();

    fireEvent.change(screen.getByRole("combobox"), { target: { value: "Past due" } });
    expect(screen.getByText("Kite Robotics")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Columns" }));
    const owner = screen.getByText("owner", { selector: "label" });
    fireEvent.click(within(owner).getByRole("checkbox"));
    expect(screen.queryByRole("columnheader", { name: /owner/i })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Select row 7/i }));
    expect(screen.getByText("1 row selected")).toBeInTheDocument();
  });

  it("copies selected rows as JSON", async () => {
    render(<App />);
    fireEvent.click(screen.getByRole("button", { name: /^Select row 1$/i }));
    fireEvent.click(screen.getByRole("button", { name: "Copy JSON" }));
    expect(writeText).toHaveBeenCalledOnce();
    expect(writeText.mock.calls[0]?.[0]).toContain('"Acme Labs"');
  });

  it("renders Chinese copy from preview locale", () => {
    window.history.replaceState({}, "", "/?locale=zh-CN");
    render(<App />);
    expect(screen.getByRole("button", { name: "列" })).toBeInTheDocument();
    expect(screen.getByText("已选择 0 行")).toBeInTheDocument();
  });

  it("applies the standalone dark theme", () => {
    window.history.replaceState({}, "", "/?theme=dark");
    render(<App />);
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
  });
});
