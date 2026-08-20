/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */

export const MAX_HISTORY = 20;

export type QueryResultPayload = {
  resultId: string;
  tool: "mysql_query";
  database: string;
  sql: string;
  tables: string[];
  columns: string[];
  rows: string[][];
  rowCount: number;
  truncated: boolean;
  durationMs: number;
  executedAt: string;
};

export type HistoryEntry = {
  id: string;
  status: "success" | "error" | "cancelled";
  sql: string;
  result?: QueryResultPayload;
  message?: string;
  recordedAt: string;
};

export type ToolResultLike = {
  content?: Array<{ type?: string; text?: string }>;
  structuredContent?: unknown;
  isError?: boolean;
};

export type SortState = {
  column: number;
  direction: "asc" | "desc";
} | null;

let transientSequence = 0;

function transientId(prefix: string): string {
  transientSequence += 1;
  return `${prefix}-${Date.now()}-${transientSequence}`;
}

export function extractText(result: ToolResultLike): string {
  return (
    result.content
      ?.filter((item) => item.type === "text" && typeof item.text === "string")
      .map((item) => item.text)
      .join("\n") || "Query failed"
  );
}

export function parseQueryPayload(value: unknown): QueryResultPayload | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const data = value as Record<string, unknown>;
  const columns = data.columns;
  const rows = data.rows;
  if (
    typeof data.resultId !== "string" ||
    data.tool !== "mysql_query" ||
    typeof data.database !== "string" ||
    typeof data.sql !== "string" ||
    !Array.isArray(data.tables) ||
    !data.tables.every((item) => typeof item === "string") ||
    !Array.isArray(columns) ||
    !columns.every((item) => typeof item === "string") ||
    !Array.isArray(rows) ||
    !rows.every(
      (row) =>
        Array.isArray(row) &&
        row.length === columns.length &&
        row.every((item) => typeof item === "string"),
    ) ||
    typeof data.rowCount !== "number" ||
    !Number.isSafeInteger(data.rowCount) ||
    data.rowCount < 0 ||
    typeof data.truncated !== "boolean" ||
    typeof data.durationMs !== "number" ||
    !Number.isFinite(data.durationMs) ||
    typeof data.executedAt !== "string"
  ) {
    return null;
  }

  return data as QueryResultPayload;
}

export function historyEntryFromToolResult(
  result: ToolResultLike,
  fallbackSQL = "",
): HistoryEntry {
  const recordedAt = new Date().toISOString();
  if (result.isError) {
    return {
      id: transientId("error"),
      status: "error",
      sql: fallbackSQL,
      message: extractText(result),
      recordedAt,
    };
  }

  const payload = parseQueryPayload(result.structuredContent);
  if (!payload) {
    return {
      id: transientId("error"),
      status: "error",
      sql: fallbackSQL,
      message: "The server returned an invalid query result.",
      recordedAt,
    };
  }

  return {
    id: payload.resultId,
    status: "success",
    sql: payload.sql,
    result: payload,
    recordedAt: payload.executedAt,
  };
}

export function cancelledHistoryEntry(sql: string, reason?: string): HistoryEntry {
  return {
    id: transientId("cancelled"),
    status: "cancelled",
    sql,
    message: reason || "Query cancelled",
    recordedAt: new Date().toISOString(),
  };
}

export function addHistory(history: HistoryEntry[], entry: HistoryEntry): HistoryEntry[] {
  if (entry.status === "success" && history.some((item) => item.id === entry.id)) {
    return history;
  }
  return [entry, ...history].slice(0, MAX_HISTORY);
}

export function titleForResult(result?: QueryResultPayload): string {
  if (!result || result.tables.length === 0) return "Query results";
  const first = result.tables[0]?.split(".").at(-1) || result.tables[0];
  return result.tables.length === 1 ? first : `${first} + ${result.tables.length - 1}`;
}

export function statusColumnIndex(columns: string[]): number {
  return columns.findIndex((column) => column.toLocaleLowerCase() === "status");
}

export function visibleRows(
  rows: string[][],
  query: string,
  statusIndex: number,
  status: string,
  sort: SortState,
  locale: string,
): Array<{ row: string[]; index: number }> {
  const needle = query.trim().toLocaleLowerCase(locale);
  const filtered = rows
    .map((row, index) => ({ row, index }))
    .filter(({ row }) => {
      const matchesQuery =
        needle.length === 0 ||
        row.some((value) => value.toLocaleLowerCase(locale).includes(needle));
      const matchesStatus =
        !status || statusIndex < 0 || row[statusIndex] === status;
      return matchesQuery && matchesStatus;
    });

  if (!sort) return filtered;
  const collator = new Intl.Collator(locale, {
    numeric: true,
    sensitivity: "base",
  });
  return filtered.sort((a, b) => {
    const compared = collator.compare(a.row[sort.column] ?? "", b.row[sort.column] ?? "");
    return sort.direction === "asc" ? compared : -compared;
  });
}

function encodeDelimited(value: string, delimiter: string): string {
  if (!value.includes(delimiter) && !/["\r\n]/.test(value)) return value;
  return `"${value.replaceAll('"', '""')}"`;
}

export function serializeRows(
  format: "tsv" | "csv" | "json",
  columns: string[],
  rows: string[][],
): string {
  if (format === "json") return JSON.stringify({ columns, rows }, null, 2);
  const delimiter = format === "tsv" ? "\t" : ",";
  return [columns, ...rows]
    .map((row) => row.map((value) => encodeDelimited(value, delimiter)).join(delimiter))
    .join("\n");
}

export const previewResult: QueryResultPayload = {
  resultId: "preview-query-results-v2",
  tool: "mysql_query",
  database: "analytics",
  sql: "SELECT customer, plan, mrr, status, owner, updated FROM accounts ORDER BY updated DESC LIMIT 100",
  tables: ["analytics.accounts"],
  columns: ["customer", "plan", "mrr", "status", "owner", "updated"],
  rows: [
    ["Acme Labs", "Enterprise", "$24,500.00", "Active", "Sarah Chen", "Aug 20, 14:31"],
    ["Northstar Health", "Growth", "$12,000.00", "Active", "Mike Patel", "Aug 20, 14:30"],
    ["Vercel Systems", "Pro", "$8,750.00", "Active", "Jordan Lee", "Aug 20, 14:29"],
    ["Lumen Works", "Growth", "$6,300.00", "Past due", "Alex Morgan", "Aug 20, 14:28"],
    ["Harbor Finance", "Enterprise", "$19,800.00", "Active", "Priya Shah", "Aug 20, 14:27"],
    ["Juniper Studio", "Pro", "$4,250.00", "Active", "Taylor Kim", "Aug 20, 14:26"],
    ["Kite Robotics", "Growth", "$7,900.00", "Past due", "Chris Allen", "Aug 20, 14:25"],
    ["Atlas Retail", "Enterprise", "$16,400.00", "Active", "Jamie Wu", "Aug 20, 14:24"],
    ["Summit Education", "Pro", "$5,150.00", "Active", "Morgan Ruiz", "Aug 20, 14:23"],
    ["Brightline Logistics", "Growth", "$6,750.00", "Active", "Riley Thompson", "Aug 20, 14:22"],
  ],
  rowCount: 10,
  truncated: false,
  durationMs: 128,
  executedAt: "2026-08-20T06:32:00Z",
};
