/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
import { useEffect, useMemo, useRef, useState } from "react";
import {
  ArrowsClockwise,
  CaretDown,
  CaretRight,
  CaretUp,
  CaretUpDown,
  Check,
  CheckCircle,
  Circle,
  Columns,
  Copy,
  Database,
  DotsThree,
  List,
  MagnifyingGlass,
  SpinnerGap,
  Warning,
  X,
} from "@phosphor-icons/react";
import {
  addHistory,
  cancelledHistoryEntry,
  historyEntryFromToolResult,
  previewResult,
  serializeRows,
  statusColumnIndex,
  titleForResult,
  visibleRows,
  type HistoryEntry,
  type QueryResultPayload,
  type SortState,
  type ToolResultLike,
} from "./model";
import { useMcpApp } from "./useMcpApp";

type Locale = "en" | "zh-CN";

const messages = {
  en: {
    appTitle: "Query result",
    readOnly: "read only",
    sql: "SQL",
    copy: "Copy",
    copySQL: "Copy SQL",
    copied: "Copied",
    rowsReturned: (rows: number, ms: number) => `${rows} rows returned in ${ms} ms`,
    failed: "Query failed",
    cancelled: "Query cancelled",
    filter: (rows: number) => `Filter ${rows} rows…`,
    statusAll: "Status: All",
    columns: "Columns",
    copyRows: "Copy rows",
    selected: (count: number) => `${count} row${count === 1 ? "" : "s"} selected`,
    copyJSON: "Copy JSON",
    copyCSV: "Copy CSV",
    copyTSV: "Copy TSV",
    refresh: "Refresh current query",
    refreshing: "Refreshing query…",
    executing: "Executing query…",
    connecting: "Connecting to host…",
    empty: "Run mysql_query to see results here.",
    noMatches: "No rows match the current filters.",
    noRows: "The query returned no rows.",
    truncated: "Result limited by the server row cap",
    hostNoTools: "This host does not support refresh from the app.",
    connectionFailed: "Could not connect to the MCP host.",
    menu: "More actions",
    history: "Query history",
    closeHistory: "Close query history",
  },
  "zh-CN": {
    appTitle: "查询结果",
    readOnly: "只读",
    sql: "SQL",
    copy: "复制",
    copySQL: "复制 SQL",
    copied: "已复制",
    rowsReturned: (rows: number, ms: number) => `返回 ${rows} 行，用时 ${ms} 毫秒`,
    failed: "查询失败",
    cancelled: "查询已取消",
    filter: (rows: number) => `筛选 ${rows} 行…`,
    statusAll: "状态：全部",
    columns: "列",
    copyRows: "复制行",
    selected: (count: number) => `已选择 ${count} 行`,
    copyJSON: "复制 JSON",
    copyCSV: "复制 CSV",
    copyTSV: "复制 TSV",
    refresh: "刷新当前查询",
    refreshing: "正在刷新查询…",
    executing: "正在执行查询…",
    connecting: "正在连接 Host…",
    empty: "调用 mysql_query 后，结果会显示在这里。",
    noMatches: "没有符合当前筛选条件的行。",
    noRows: "查询未返回数据行。",
    truncated: "结果已达到服务端行数上限",
    hostNoTools: "当前 Host 不支持从应用内刷新。",
    connectionFailed: "无法连接 MCP Host。",
    menu: "更多操作",
    history: "查询历史",
    closeHistory: "关闭查询历史",
  },
} as const;

function localeFrom(value?: string): Locale {
  return value?.toLocaleLowerCase().startsWith("zh") ? "zh-CN" : "en";
}

function timeLabel(value: string, locale: Locale): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(locale, {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
}

async function copyText(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.append(textarea);
  textarea.select();
  document.execCommand("copy");
  textarea.remove();
}

function HistorySidebar({
  history,
  activeID,
  database,
  locale,
  open,
  onSelect,
  onClose,
}: {
  history: HistoryEntry[];
  activeID: string;
  database: string;
  locale: Locale;
  open: boolean;
  onSelect: (id: string) => void;
  onClose: () => void;
}) {
  const t = messages[locale];
  const active = history.find((item) => item.id === activeID);
  return (
    <aside className={`history-panel ${open ? "history-panel--open" : ""}`} aria-label={t.history}>
      <div className="history-heading">
        <div>
          <h1>{t.appTitle}</h1>
          <p><Database size={17} /> <span>{database || "mysql"}</span><span className="dot">·</span><span>{t.readOnly}</span></p>
        </div>
        <button className="icon-button history-close" type="button" onClick={onClose} aria-label={t.closeHistory}>
          <X size={18} />
        </button>
      </div>
      <nav className="history-list">
        {history.map((item) => {
          const rows = item.result?.rowCount;
          return (
            <button
              className={`history-item ${item.id === activeID ? "history-item--active" : ""} ${item.status !== "success" ? "history-item--error" : ""}`}
              key={item.id}
              type="button"
              onClick={() => onSelect(item.id)}
            >
              <span className="history-rail" aria-hidden="true">
                <span className={`history-node history-node--${item.status}`} />
              </span>
              <span className="history-time">{timeLabel(item.recordedAt, locale)}</span>
              <span className="history-tool">mysql_query</span>
              <span className="history-count">{rows === undefined ? (item.status === "cancelled" ? t.cancelled : t.failed) : `${rows} rows`}</span>
              <span className={`history-state history-state--${item.status}`} aria-hidden="true" />
            </button>
          );
        })}
      </nav>
      <section className="sql-panel">
        <h2><CaretDown size={15} /> {t.sql}</h2>
        <pre>{active?.sql || "—"}</pre>
        <button type="button" className="text-button" onClick={() => void copyText(active?.sql || "")} disabled={!active?.sql}>
          <Copy size={18} /> {t.copySQL}
        </button>
      </section>
    </aside>
  );
}

function SortIcon({ state, column }: { state: SortState; column: number }) {
  if (state?.column !== column) return <CaretUpDown size={14} />;
  return state.direction === "asc" ? <CaretUp size={14} /> : <CaretDown size={14} />;
}

export function App() {
  const previewLocale = localeFrom(new URLSearchParams(window.location.search).get("locale") || undefined);
  const [history, setHistory] = useState<HistoryEntry[]>(() =>
    window.parent === window
      ? [
          { id: previewResult.resultId, status: "success", sql: previewResult.sql, result: previewResult, recordedAt: previewResult.executedAt },
          { id: "preview-history-2", status: "success", sql: "SELECT customer, status FROM accounts WHERE status = 'Past due'", result: { ...previewResult, resultId: "preview-history-2", executedAt: "2026-08-20T06:27:00Z" }, recordedAt: "2026-08-20T06:27:00Z" },
          { id: "preview-history-3", status: "success", sql: "SELECT customer, plan FROM accounts ORDER BY customer", result: { ...previewResult, resultId: "preview-history-3", executedAt: "2026-08-20T06:20:00Z" }, recordedAt: "2026-08-20T06:20:00Z" },
        ]
      : [],
  );
  const [activeID, setActiveID] = useState(() => (window.parent === window ? previewResult.resultId : ""));
  const [pendingSQL, setPendingSQL] = useState("");
  const [executing, setExecuting] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [filter, setFilter] = useState("");
  const [status, setStatus] = useState("");
  const [sort, setSort] = useState<SortState>(null);
  const [hiddenColumns, setHiddenColumns] = useState<Set<number>>(() => new Set());
  const [selectedRows, setSelectedRows] = useState<Set<number>>(() => new Set());
  const [columnsOpen, setColumnsOpen] = useState(false);
  const [copyOpen, setCopyOpen] = useState(false);
  const [moreOpen, setMoreOpen] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [copyStatus, setCopyStatus] = useState("");
  const pendingSQLRef = useRef("");

  const pushEntry = (entry: HistoryEntry) => {
    setHistory((current) => addHistory(current, entry));
    setActiveID(entry.id);
    setExecuting(false);
    setFilter("");
    setStatus("");
    setSort(null);
    setHiddenColumns(new Set());
    setSelectedRows(new Set());
  };

  const mcp = useMcpApp({
    onToolInput: (sql) => {
      pendingSQLRef.current = sql;
      setPendingSQL(sql);
      setExecuting(true);
    },
    onToolResult: (result) => pushEntry(historyEntryFromToolResult(result, pendingSQLRef.current)),
    onToolCancelled: (reason) => pushEntry(cancelledHistoryEntry(pendingSQLRef.current, reason)),
  });

  const locale = mcp.preview ? previewLocale : localeFrom(mcp.hostContext?.locale);
  const t = messages[locale];
  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);
  const active = history.find((entry) => entry.id === activeID) || history[0];
  const result = active?.result;
  const statusIndex = statusColumnIndex(result?.columns || []);
  const rows = useMemo(
    () => visibleRows(result?.rows || [], filter, statusIndex, status, sort, locale),
    [result, filter, statusIndex, status, sort, locale],
  );
  const statusOptions = useMemo(
    () => statusIndex < 0 ? [] : Array.from(new Set((result?.rows || []).map((row) => row[statusIndex]))).sort(),
    [result, statusIndex],
  );
  const visibleColumnIndexes = (result?.columns || []).map((_, index) => index).filter((index) => !hiddenColumns.has(index));
  const selectedVisibleRows = rows.filter(({ index }) => selectedRows.has(index)).map(({ row }) => row);
  const rowsToCopy = selectedVisibleRows.length > 0 ? selectedVisibleRows : rows.map(({ row }) => row);
  const columnsToCopy = visibleColumnIndexes.map((index) => result?.columns[index] || "");
  const projectedRows = rowsToCopy.map((row) => visibleColumnIndexes.map((index) => row[index] || ""));

  const toggleSort = (column: number) => {
    setSort((current) => {
      if (current?.column !== column) return { column, direction: "asc" };
      if (current.direction === "asc") return { column, direction: "desc" };
      return null;
    });
  };

  const toggleSelected = (index: number) => {
    setSelectedRows((current) => {
      const next = new Set(current);
      if (next.has(index)) next.delete(index);
      else next.add(index);
      return next;
    });
  };

  const copyRows = async (format: "tsv" | "csv" | "json") => {
    await copyText(serializeRows(format, columnsToCopy, projectedRows));
    setCopyStatus(t.copied);
    setCopyOpen(false);
    window.setTimeout(() => setCopyStatus(""), 1400);
  };

  const refresh = async () => {
    const app = mcp.app;
    if (!result || refreshing || (!mcp.preview && (!app || !mcp.canCallTools))) return;
    setMoreOpen(false);
    setRefreshing(true);
    try {
      if (mcp.preview) {
        await new Promise((resolve) => window.setTimeout(resolve, 250));
        const refreshed = {
          ...result,
          resultId: `preview-${Date.now()}`,
          durationMs: Math.max(1, result.durationMs + 2),
          executedAt: new Date().toISOString(),
        };
        pushEntry(historyEntryFromToolResult({ structuredContent: refreshed }, result.sql));
        return;
      }
      if (!app) return;
      const refreshed = (await app.callServerTool({
        name: "mysql_query",
        arguments: { sql: result.sql },
      })) as ToolResultLike;
      pushEntry(historyEntryFromToolResult(refreshed, result.sql));
    } catch (error) {
      pushEntry(
        historyEntryFromToolResult(
          { isError: true, content: [{ type: "text", text: error instanceof Error ? error.message : String(error) }] },
          result.sql,
        ),
      );
    } finally {
      setRefreshing(false);
    }
  };

  const selectHistory = (id: string) => {
    setActiveID(id);
    setHistoryOpen(false);
    setFilter("");
    setStatus("");
    setSort(null);
    setSelectedRows(new Set());
  };

  const renderBody = () => {
    if (mcp.connecting) return <div className="state-panel"><SpinnerGap className="spin" size={25} /><strong>{t.connecting}</strong></div>;
    if (mcp.connectionError) return <div className="state-panel state-panel--error"><Warning size={25} /><strong>{t.connectionFailed}</strong><span>{mcp.connectionError}</span></div>;
    if (executing && history.length === 0) return <div className="state-panel"><SpinnerGap className="spin" size={25} /><strong>{t.executing}</strong><code>{pendingSQL}</code></div>;
    if (!active) return <div className="state-panel"><Database size={27} /><strong>{t.empty}</strong></div>;
    if (active.status !== "success") return <div className="state-panel state-panel--error"><Warning size={25} /><strong>{active.status === "cancelled" ? t.cancelled : t.failed}</strong><span>{active.message}</span><code>{active.sql}</code></div>;
    if (!result) return null;

    return (
      <>
        <div className="table-shell">
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th className="select-column">
                    <button
                      type="button"
                      className="checkbox"
                      aria-label="Select visible rows"
                      aria-pressed={rows.length > 0 && rows.every(({ index }) => selectedRows.has(index))}
                      onClick={() => {
                        const allSelected = rows.length > 0 && rows.every(({ index }) => selectedRows.has(index));
                        setSelectedRows((current) => {
                          const next = new Set(current);
                          rows.forEach(({ index }) => allSelected ? next.delete(index) : next.add(index));
                          return next;
                        });
                      }}
                    >
                      {rows.length > 0 && rows.every(({ index }) => selectedRows.has(index)) && <Check size={13} weight="bold" />}
                    </button>
                  </th>
                  {visibleColumnIndexes.map((columnIndex) => (
                    <th key={`${result.columns[columnIndex]}-${columnIndex}`}>
                      <button type="button" className="sort-button" onClick={() => toggleSort(columnIndex)}>
                        <span>{result.columns[columnIndex]}</span><SortIcon state={sort} column={columnIndex} />
                      </button>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map(({ row, index }) => (
                  <tr key={index} className={selectedRows.has(index) ? "row-selected" : ""}>
                    <td className="select-column">
                      <button type="button" className="checkbox" aria-label={`Select row ${index + 1}`} aria-pressed={selectedRows.has(index)} onClick={() => toggleSelected(index)}>
                        {selectedRows.has(index) && <Check size={13} weight="bold" />}
                      </button>
                    </td>
                    {visibleColumnIndexes.map((columnIndex) => <td key={columnIndex}>{row[columnIndex]}</td>)}
                  </tr>
                ))}
              </tbody>
            </table>
            {result.rows.length === 0 && <div className="inline-empty">{t.noRows}</div>}
            {result.rows.length > 0 && rows.length === 0 && <div className="inline-empty">{t.noMatches}</div>}
          </div>
          <footer className="selection-bar">
            <span>{t.selected(selectedRows.size)}</span>
            <div>
              <button type="button" aria-label={t.copyJSON} onClick={() => void copyRows("json")}><span className="braces" aria-hidden="true">{'{ }'}</span>{t.copyJSON}</button>
              <button type="button" onClick={() => void copyRows("csv")}><Copy size={18} />{t.copyCSV}</button>
            </div>
          </footer>
        </div>
        {result.truncated && <div className="truncated-note"><Warning size={17} />{t.truncated}</div>}
      </>
    );
  };

  return (
    <div className="app-frame">
      <HistorySidebar history={history} activeID={active?.id || ""} database={result?.database || previewResult.database} locale={locale} open={historyOpen} onSelect={selectHistory} onClose={() => setHistoryOpen(false)} />
      {historyOpen && <button type="button" className="drawer-backdrop" aria-label={t.closeHistory} onClick={() => setHistoryOpen(false)} />}
      <main className="results-panel">
        <header className="result-header">
          <button type="button" className="icon-button history-trigger" aria-label={t.history} onClick={() => setHistoryOpen(true)}><List size={21} /></button>
          <div className="result-title">
            <h2>{titleForResult(result)}</h2>
            {result ? <p>{t.rowsReturned(result.rowCount, result.durationMs)}</p> : <p>{executing ? t.executing : t.empty}</p>}
          </div>
          <div className="sync-actions">
            <span className={`sync-pill ${refreshing || executing ? "sync-pill--busy" : ""}`}>
              {refreshing || executing ? <SpinnerGap className="spin" size={16} /> : <span className="sync-rings"><span /></span>}
              {refreshing ? t.refreshing : executing ? t.executing : result ? `Synced ${timeLabel(result.executedAt, locale)}` : t.connecting}
            </span>
            <div className="menu-wrap">
              <button type="button" className="icon-button" aria-label={t.menu} aria-expanded={moreOpen} onClick={() => setMoreOpen((value) => !value)}><DotsThree size={22} weight="bold" /></button>
              {moreOpen && <div className="popover popover--right">
                <button type="button" onClick={() => void refresh()} disabled={!result || (!mcp.preview && !mcp.canCallTools) || refreshing}><ArrowsClockwise size={17} />{t.refresh}</button>
                {!mcp.canCallTools && <p>{t.hostNoTools}</p>}
              </div>}
            </div>
          </div>
        </header>
        <section className="toolbar" aria-label="Result controls">
          <label className="search-field"><MagnifyingGlass size={19} /><input value={filter} onChange={(event) => setFilter(event.target.value)} placeholder={t.filter(result?.rowCount || 0)} /></label>
          {statusIndex >= 0 && <label className="status-field"><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="">{t.statusAll}</option>{statusOptions.map((option) => <option key={option} value={option}>{option}</option>)}</select><CaretDown size={16} /></label>}
          <span className="toolbar-spacer" />
          <div className="menu-wrap">
            <button type="button" className="control-button" aria-expanded={columnsOpen} onClick={() => setColumnsOpen((value) => !value)}><Columns size={20} />{t.columns}</button>
            {columnsOpen && <div className="popover popover--right columns-menu">{result?.columns.map((column, index) => <label key={`${column}-${index}`}><input type="checkbox" checked={!hiddenColumns.has(index)} onChange={() => setHiddenColumns((current) => { const next = new Set(current); if (next.has(index)) next.delete(index); else if (next.size < result.columns.length - 1) next.add(index); return next; })} /><span className="menu-checkbox">{!hiddenColumns.has(index) && <Check size={12} weight="bold" />}</span>{column}</label>)}</div>}
          </div>
          <div className="menu-wrap">
            <button type="button" className="control-button" aria-expanded={copyOpen} onClick={() => setCopyOpen((value) => !value)}><Copy size={20} />{copyStatus || t.copyRows}<CaretDown size={14} /></button>
            {copyOpen && <div className="popover popover--right"><button type="button" onClick={() => void copyRows("tsv")}><Copy size={17} />{t.copyTSV}</button><button type="button" onClick={() => void copyRows("csv")}><Copy size={17} />{t.copyCSV}</button><button type="button" aria-label={t.copyJSON} onClick={() => void copyRows("json")}><span className="braces" aria-hidden="true">{'{ }'}</span>{t.copyJSON}</button></div>}
          </div>
        </section>
        {renderBody()}
      </main>
    </div>
  );
}
