/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MCPAppState, MCPHandlers } from "./useMcpApp";
import { previewResult } from "./model";

const mockUseMcpApp = vi.hoisted(() => vi.fn());

vi.mock("./useMcpApp", () => ({ useMcpApp: mockUseMcpApp }));

import { App } from "./App";

const embeddedState = (overrides: Partial<MCPAppState> = {}): MCPAppState => ({
  app: null,
  connected: true,
  connecting: false,
  connectionError: "",
  canCallTools: false,
  preview: false,
  ...overrides,
});

function handlers(): MCPHandlers {
  return mockUseMcpApp.mock.calls.at(-1)?.[0] as MCPHandlers;
}

describe("query results host states", () => {
  beforeEach(() => {
    mockUseMcpApp.mockReset();
    mockUseMcpApp.mockReturnValue(embeddedState());
    Object.defineProperty(window, "parent", { configurable: true, value: {} });
  });

  afterEach(() => {
    cleanup();
    Object.defineProperty(window, "parent", { configurable: true, value: window });
  });

  it("renders connecting, empty, executing and failed states", () => {
    mockUseMcpApp.mockReturnValue(embeddedState({ connected: false, connecting: true }));
    const view = render(<App />);
    expect(screen.getAllByText("Connecting to host…")).toHaveLength(2);

    mockUseMcpApp.mockReturnValue(embeddedState());
    view.rerender(<App />);
    expect(screen.getAllByText("Run mysql_query to see results here.")).toHaveLength(2);

    act(() => handlers().onToolInput({ profile: "reporting", sql: "SELECT 1" }));
    expect(screen.getAllByText("Executing query…").length).toBeGreaterThanOrEqual(2);

    act(() => handlers().onToolResult({ isError: true, content: [{ type: "text", text: "database unavailable" }] }));
    expect(screen.getAllByText("Query failed").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("database unavailable")).toBeInTheDocument();
    expect(screen.getAllByText("reporting").length).toBeGreaterThanOrEqual(1);
  });

  it("uses a neutral database label before the embedded host returns a result", () => {
    render(<App />);

    expect(screen.getByText("mysql")).toBeInTheDocument();
    expect(screen.queryByText("analytics")).not.toBeInTheDocument();
  });

  it("resets hidden columns when switching result history", () => {
    const { container } = render(<App />);
    act(() => handlers().onToolInput({ profile: previewResult.profile, sql: previewResult.sql }));
    act(() => handlers().onToolResult({
      structuredContent: {
        ...previewResult,
        resultId: "older",
        columns: ["id", "legacy"],
        rows: [["1", "kept"]],
        rowCount: 1,
      },
    }));
    act(() => handlers().onToolInput({ profile: previewResult.profile, sql: previewResult.sql }));
    act(() => handlers().onToolResult({
      structuredContent: {
        ...previewResult,
        resultId: "newer",
        columns: ["customer", "status"],
        rows: [["Acme Labs", "Active"]],
        rowCount: 1,
      },
    }));

    fireEvent.click(screen.getByRole("button", { name: "Columns" }));
    fireEvent.click(within(screen.getByText("status", { selector: "label" })).getByRole("checkbox"));
    expect(screen.queryByRole("columnheader", { name: "status" })).not.toBeInTheDocument();

    const historyItems = container.querySelectorAll<HTMLButtonElement>(".history-item");
    fireEvent.click(historyItems[1]);
    expect(screen.getByRole("columnheader", { name: "legacy" })).toBeInTheDocument();
  });

  it("renders empty rows, truncation and the no-refresh capability message", () => {
    render(<App />);
    act(() => handlers().onToolInput({ profile: previewResult.profile, sql: previewResult.sql }));
    act(() => handlers().onToolResult({ structuredContent: { ...previewResult, resultId: "empty", rows: [], rowCount: 0, truncated: true } }));

    expect(screen.getByText("The query returned no rows.")).toBeInTheDocument();
    expect(screen.getByText("Result limited by the server row cap")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    expect(screen.getByText("This host does not support refresh from the app.")).toBeInTheDocument();
  });

  it("preserves the original profile for invalid payloads and cancellations", () => {
    render(<App />);

    act(() => handlers().onToolInput({ profile: "warehouse", sql: "SELECT 1" }));
    act(() => handlers().onToolResult({ structuredContent: { ...previewResult, profile: "" } }));
    expect(screen.getByText("The server returned an invalid query result.")).toBeInTheDocument();
    expect(screen.getAllByText("warehouse").length).toBeGreaterThanOrEqual(1);

    act(() => handlers().onToolInput({ profile: "billing", sql: "SELECT 2" }));
    act(() => handlers().onToolCancelled("cancelled by host"));
    expect(screen.getByText("cancelled by host")).toBeInTheDocument();
    expect(screen.getAllByText("billing").length).toBeGreaterThanOrEqual(1);
  });

  it("matches a contextual error to its profile while another request is pending", () => {
    render(<App />);
    act(() => handlers().onToolInput({ profile: "reporting", sql: "SELECT 1" }));
    act(() => handlers().onToolInput({ profile: "analytics", sql: "SELECT 1" }));
    act(() => handlers().onToolResult({
      isError: true,
      content: [{ type: "text", text: "analytics failed" }],
      _meta: {
        "io.github.kurok1.mcp-server-mysql/query-input": { profile: "analytics", sql: "SELECT 1" },
      },
    }));

    expect(screen.getByText("analytics failed")).toBeInTheDocument();
    expect(screen.getAllByText("analytics").length).toBeGreaterThanOrEqual(1);
    act(() => handlers().onToolResult({
      structuredContent: { ...previewResult, resultId: "reporting-success", profile: "reporting", sql: "SELECT 1" },
    }));
    expect(screen.getByText("Acme Labs")).toBeInTheDocument();
    expect(screen.getAllByText("reporting").length).toBeGreaterThanOrEqual(1);
  });

  it("uses error metadata instead of stale structured content for provenance", () => {
    const { container } = render(<App />);
    act(() => handlers().onToolInput({ profile: "reporting", sql: "SELECT 1" }));
    act(() => handlers().onToolResult({
      isError: true,
      content: [{ type: "text", text: "reporting denied" }],
      structuredContent: { ...previewResult, resultId: "stale-success", profile: "analytics", sql: "SELECT stale" },
      _meta: {
        "io.github.kurok1.mcp-server-mysql/query-input": { profile: "reporting", sql: "SELECT 1" },
      },
    }));

    expect(screen.getByText("reporting denied")).toBeInTheDocument();
    expect(container.querySelector(".history-profile")?.textContent).toBe("reporting");
  });

  it("keeps an anonymous cancellation undetermined until contextual results settle", () => {
    const { container } = render(<App />);
    act(() => handlers().onToolInput({ profile: "reporting", sql: "SELECT 1" }));
    act(() => handlers().onToolInput({ profile: "analytics", sql: "SELECT 1" }));
    act(() => handlers().onToolCancelled("host cancelled one request"));
    expect(screen.getByText("host cancelled one request")).toBeInTheDocument();
    expect(screen.getAllByText("Source undetermined").length).toBeGreaterThanOrEqual(1);

    act(() => handlers().onToolResult({
      structuredContent: { ...previewResult, resultId: "reporting-after-cancel", profile: "reporting", sql: "SELECT 1" },
    }));
    expect(screen.getByText("Acme Labs")).toBeInTheDocument();
    expect(screen.queryByText("Executing query…")).not.toBeInTheDocument();
    expect(container.querySelectorAll(".history-item")).toHaveLength(2);

    act(() => handlers().onToolInput({ profile: "warehouse", sql: "SELECT 2" }));
    act(() => handlers().onToolCancelled("warehouse cancelled"));
    expect(screen.getByText("warehouse cancelled")).toBeInTheDocument();
    expect(screen.getAllByText("warehouse").length).toBeGreaterThanOrEqual(1);
  });

  it("clears a batch after multiple anonymous terminal events so a later cancellation is exact", () => {
    render(<App />);
    act(() => handlers().onToolInput({ profile: "reporting", sql: "SELECT 1" }));
    act(() => handlers().onToolInput({ profile: "analytics", sql: "SELECT 1" }));
    act(() => handlers().onToolResult({ isError: true, content: [{ type: "text", text: "unattributed failure one" }] }));
    act(() => handlers().onToolResult({ isError: true, content: [{ type: "text", text: "unattributed failure two" }] }));
    expect(screen.getByText("unattributed failure two")).toBeInTheDocument();
    expect(screen.getAllByText("Source undetermined").length).toBeGreaterThanOrEqual(1);

    act(() => handlers().onToolInput({ profile: "warehouse", sql: "SELECT 3" }));
    act(() => handlers().onToolCancelled("warehouse cancelled"));
    expect(screen.getByText("warehouse cancelled")).toBeInTheDocument();
    expect(screen.getAllByText("warehouse").length).toBeGreaterThanOrEqual(1);
  });

  it("refreshes through the host and keeps the previous snapshot", async () => {
    const callServerTool = vi.fn().mockResolvedValue({
      structuredContent: { ...previewResult, resultId: "refreshed", executedAt: "2026-08-20T06:33:00Z" },
    });
    mockUseMcpApp.mockReturnValue(embeddedState({
      app: { callServerTool } as unknown as MCPAppState["app"],
      canCallTools: true,
    }));
    const { container } = render(<App />);
    act(() => handlers().onToolInput({ profile: previewResult.profile, sql: previewResult.sql }));
    act(() => handlers().onToolResult({ structuredContent: previewResult }));

    fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    fireEvent.click(screen.getByRole("button", { name: "Refresh current query" }));

    await waitFor(() => expect(callServerTool).toHaveBeenCalledWith({
      name: "mysql_query",
      arguments: { profile: previewResult.profile, sql: previewResult.sql },
    }));
    await waitFor(() => expect(container.querySelectorAll(".history-item")).toHaveLength(2));
  });

  it("records a failed refresh without dropping the successful snapshot", async () => {
    const callServerTool = vi.fn().mockRejectedValue(new Error("refresh offline"));
    mockUseMcpApp.mockReturnValue(embeddedState({
      app: { callServerTool } as unknown as MCPAppState["app"],
      canCallTools: true,
    }));
    const { container } = render(<App />);
    act(() => handlers().onToolInput({ profile: previewResult.profile, sql: previewResult.sql }));
    act(() => handlers().onToolResult({ structuredContent: previewResult }));

    fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    fireEvent.click(screen.getByRole("button", { name: "Refresh current query" }));

    await waitFor(() => expect(screen.getAllByText("Query failed").length).toBeGreaterThanOrEqual(1));
    expect(screen.getByText("refresh offline")).toBeInTheDocument();
    expect(container.querySelectorAll(".history-item")).toHaveLength(2);
    expect(container.querySelectorAll(".history-item--error")).toHaveLength(1);
    expect(screen.getAllByText(previewResult.profile).length).toBeGreaterThanOrEqual(1);
  });

  it("refreshes the selected history snapshot with its original profile and SQL", async () => {
    const callServerTool = vi.fn().mockResolvedValue({
      structuredContent: { ...previewResult, resultId: "refreshed-reporting", profile: "reporting", database: "analytics" },
    });
    mockUseMcpApp.mockReturnValue(embeddedState({
      app: { callServerTool } as unknown as MCPAppState["app"],
      canCallTools: true,
    }));
    const { container } = render(<App />);
    const older = { ...previewResult, resultId: "older-reporting", profile: "reporting", sql: "SELECT 1", executedAt: "2026-08-20T06:31:00Z" };
    const newer = { ...previewResult, resultId: "newer-analytics", profile: "analytics-readonly", sql: "SELECT 1", executedAt: "2026-08-20T06:32:00Z" };
    act(() => handlers().onToolInput({ profile: older.profile, sql: older.sql }));
    act(() => handlers().onToolResult({ structuredContent: older }));
    act(() => handlers().onToolInput({ profile: newer.profile, sql: newer.sql }));
    act(() => handlers().onToolResult({ structuredContent: newer }));

    const historyItems = container.querySelectorAll<HTMLButtonElement>(".history-item");
    fireEvent.click(historyItems[1]!);
    fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    fireEvent.click(screen.getByRole("button", { name: "Refresh current query" }));

    await waitFor(() => expect(callServerTool).toHaveBeenCalledWith({
      name: "mysql_query",
      arguments: { profile: "reporting", sql: "SELECT 1" },
    }));
  });
});
