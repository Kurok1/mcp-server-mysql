/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
import { describe, expect, it } from "vitest";
import {
  MAX_HISTORY,
  addHistory,
  historyEntryFromToolResult,
  parseQueryPayload,
  previewResult,
  serializeRows,
  titleForResult,
  visibleRows,
  type HistoryEntry,
} from "./model";

describe("query result model", () => {
  it("validates structured query results", () => {
    expect(parseQueryPayload(previewResult)).toEqual(previewResult);
    expect(parseQueryPayload({ ...previewResult, rows: [[1]] })).toBeNull();
  });

  it("keeps text errors and de-duplicates successful result ids", () => {
    const success = historyEntryFromToolResult({ structuredContent: previewResult });
    const duplicate = addHistory([success], success);
    expect(duplicate).toHaveLength(1);

    const failed = historyEntryFromToolResult({ isError: true, content: [{ type: "text", text: "denied" }] }, "SELECT 1");
    expect(failed.status).toBe("error");
    expect(failed.message).toBe("denied");
  });

  it("evicts history beyond the view-local limit", () => {
    let history: HistoryEntry[] = [];
    for (let index = 0; index < MAX_HISTORY + 3; index += 1) {
      history = addHistory(history, { id: String(index), status: "error", sql: "", message: "x", recordedAt: new Date(index).toISOString() });
    }
    expect(history).toHaveLength(MAX_HISTORY);
    expect(history[0]?.id).toBe(String(MAX_HISTORY + 2));
  });

  it("filters, status-filters and naturally sorts rows", () => {
    const rows = [["item 10", "open"], ["item 2", "closed"], ["item 1", "open"]];
    const result = visibleRows(rows, "item", 1, "open", { column: 0, direction: "asc" }, "en");
    expect(result.map(({ row }) => row[0])).toEqual(["item 1", "item 10"]);
  });

  it("serializes duplicate columns without losing data", () => {
    expect(serializeRows("json", ["id", "id"], [["1", "2"]])).toContain('"columns": [');
    expect(serializeRows("csv", ["name"], [['a,"b"']])).toBe('name\n"a,""b"""');
    expect(serializeRows("tsv", ["name"], [["a\tb"]])).toBe('name\n"a\tb"');
  });

  it("builds a table title without exposing a database prefix", () => {
    expect(titleForResult(previewResult)).toBe("accounts");
    expect(titleForResult({ ...previewResult, tables: ["db.a", "db.b"] })).toBe("a + 1");
    expect(titleForResult({ ...previewResult, tables: [] })).toBe("Query results");
  });
});
