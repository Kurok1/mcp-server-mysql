# mcp-server-mysql

> **English** | [简体中文](README.zh-CN.md)

[![Release](https://img.shields.io/github/v/release/Kurok1/mcp-server-mysql)](https://github.com/Kurok1/mcp-server-mysql/releases)
[![License](https://img.shields.io/github/license/Kurok1/mcp-server-mysql)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![Docker](https://img.shields.io/badge/ghcr.io-kurok1%2Fmcp--server--mysql-2496ED?logo=docker&logoColor=white)](https://github.com/Kurok1/mcp-server-mysql/pkgs/container/mcp-server-mysql)

**A security-first MySQL [MCP](https://modelcontextprotocol.io) server.** Every SQL statement must survive a full AST parse by an industrial-grade SQL parser (TiDB parser) before it can touch your database — backed by a read-only transaction fallback and a driver-level multi-statement lockout. Three independent layers of defense-in-depth: let AI query your database, without letting it walk off with your database.

## Why this one?

Most MySQL MCP servers enforce "read-only" with regex/keyword matching, or by wrapping queries in a read-only transaction. Both are broken:

- **Regex checks** are defeated by SQL comments and creative rewriting.
- **A read-only transaction alone** is defeated by `COMMIT; DROP TABLE ...` stacked-statement injection — the exact attack Datadog demonstrated against the official Postgres reference server, which has since been archived.

This project puts the security boundary on **real SQL semantic parsing** instead. Every statement is parsed into an AST by the [TiDB parser](https://github.com/pingcap/tidb) (MySQL 8.0-grammar compatible); anything the parser cannot understand is rejected — **fail-closed**, so incomplete grammar coverage can only over-block, never under-block. And because the parser sees real MySQL semantics, tricks like hiding a `JOIN mysql.user` inside a versioned comment `/*!80000 ... */` are extracted and checked like any other table reference.

## Highlights

- **Statement-class gating** — `SELECT` / `INSERT` / `UPDATE` / `DELETE` / DDL are individually switchable; the default is read-only. `SET`, `GRANT`, `CALL`, `USE`, `LOAD DATA`, `LOCK TABLES`, and transaction control (`BEGIN`/`COMMIT`/`ROLLBACK`) are rejected unconditionally — classification itself is an allowlist, so unknown statement types land on the deny side by construction.
- **Default-deny table whitelist** — nothing is visible until whitelisted; patterns like `db.*`, `db.table`, `app_*.logs` (glob per side, case-insensitive). Every table reference is extracted from the AST: JOINs, subqueries, derived tables, CTEs (scope-aware — a CTE name can't shadow a real table to smuggle it past the check), multi-table DML, `INSERT ... SELECT`, and versioned comments.
- **Execution guardrails** — hard row cap, per-query timeout, single-statement enforcement, and a tripwire for `UPDATE`/`DELETE` without `WHERE`.
- **Built-in observability** — per-query latency and row counts, slow-query flagging, and a `mysql_stats` tool so you can ask "which query was slowest?" right in the conversation.
- **Structured audit, opt-in** — JSONL with daily rotation; denied SQL is recorded with the exact rule that fired. Off by default: no log files unless you enable it.
- **Atomic scripts** — `mysql_script` runs a multi-statement script in a single transaction with every statement individually re-validated; any failure rolls back everything. DDL is banned inside scripts because MySQL's implicit commit would break atomicity.
- **Query-plan analysis** — `mysql_explain` with `traditional` / `json` / `tree` formats and `EXPLAIN ANALYZE` support.
- **Easy to run, small to trust** — a single static Go binary over stdio, built on the official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk); the Docker image is distroless and runs as a non-root user.

## Tools

| Tool | What it does |
|---|---|
| `mysql_query` | Run one read-only statement (`SELECT` / `SHOW` / `DESCRIBE` / `EXPLAIN`) |
| `mysql_execute` | Run one write statement (`INSERT` / `UPDATE` / `DELETE` / DDL — each type must be enabled in config); returns affected rows |
| `mysql_script` | Run a `;`-separated multi-statement script atomically in one transaction — all-or-nothing; DDL banned |
| `mysql_explain` | Execution plan for a single `SELECT` (`format`: `traditional` / `json` / `tree`; `analyze: true` runs `EXPLAIN ANALYZE`) |
| `mysql_list_tables` | List the tables visible through the whitelist |
| `mysql_describe_table` | Column structure of a whitelisted table |
| `mysql_stats` | Session stats: totals / denials, average & P95 latency, top-N slow queries, per-table access counts |

## Quick start

### 1. Get the binary

**Prebuilt** — download the tarball for your platform (`linux_amd64` / `linux_arm64` / `darwin_arm64`) from [Releases](https://github.com/Kurok1/mcp-server-mysql/releases) (checksums included), or install with Go:

```bash
go install github.com/Kurok1/mcp-server-mysql/cmd/mcp-server-mysql@latest
```

**Docker** — multi-arch images are published to GitHub Container Registry:

```bash
docker pull ghcr.io/kurok1/mcp-server-mysql:latest
```

### 2. Configure

Copy [config.example.yaml](config.example.yaml) and adjust:

```bash
mkdir -p ~/.mcp-server-mysql
cp config.example.yaml ~/.mcp-server-mysql/config.yaml
```

A minimal config:

```yaml
mysql:
  host: 127.0.0.1
  port: 3306
  user: mcp_dev                  # use a dedicated least-privilege account, not root
  password: ${MYSQL_MCP_PASSWORD}
  database: myapp

security:
  allowed_statements: [select]   # add insert/update/delete/ddl only if you need them
  table_whitelist:
    - "myapp.*"
```

### 3. Wire up your MCP client

**Claude Code:**

```bash
claude mcp add mysql --env MYSQL_MCP_PASSWORD=your-password -- \
  ~/go/bin/mcp-server-mysql --config ~/.mcp-server-mysql/config.yaml
```

**Claude Desktop or any JSON-configured client:**

```json
{
  "mcpServers": {
    "mysql": {
      "command": "/usr/local/bin/mcp-server-mysql",
      "args": ["--config", "/Users/me/.mcp-server-mysql/config.yaml"],
      "env": { "MYSQL_MCP_PASSWORD": "..." }
    }
  }
}
```

**Docker:**

```json
{
  "mcpServers": {
    "mysql": {
      "command": "docker",
      "args": [
        "run", "-i", "--rm",
        "-v", "/Users/me/.mcp-server-mysql:/data",
        "-e", "MYSQL_MCP_PASSWORD",
        "ghcr.io/kurok1/mcp-server-mysql:latest",
        "--config", "/data/config.yaml"
      ],
      "env": { "MYSQL_MCP_PASSWORD": "..." }
    }
  }
}
```

> **Docker note 1 — audit logs must live on a mounted volume.** The container is destroyed with the session; if you enable audit logging, point `audit.log_dir` at the mounted volume (e.g. `/data/logs`) or the logs vanish with the container.
>
> **Docker note 2 — reaching MySQL on the host.** On macOS/Windows set `mysql.host: host.docker.internal`; on Linux also append `"--add-host=host.docker.internal:host-gateway"` to `args`.

## Security model

```text
            MCP client  (Claude Code / Claude Desktop / …)
                               │  stdio
                               ▼
┌───────────────────────  mcp-server-mysql  ───────────────────────┐
│                                                                  │
│  mysql_query · mysql_execute · mysql_script · mysql_explain      │
│  mysql_list_tables · mysql_describe_table · mysql_stats          │
│                             │                                    │
│                             ▼                                    │
│  ┌ Layer 1 · AST main gate (TiDB parser) ───────────────────┐    │
│  │ unparseable ⇒ denied (fail-closed) → single statement    │    │
│  │ → statement-class allowlist + read/write tool check      │    │
│  │ → per-class switches (default: select only)              │    │
│  │ → dangerous constructs (INTO OUTFILE / LOAD_FILE)        │    │
│  │ → missing-WHERE tripwire                                 │    │
│  │ → default-deny table whitelist (JOIN / subquery /        │    │
│  │   CTE scope-aware / versioned comments)                  │    │
│  └────────────┬───────────────────────────────┬─────────────┘    │
│               │ allowed                       │ denied           │
│               ▼                               ▼                  │
│  ┌ Layer 2 · executor ─────────────┐    DENIED [rule]: reason    │
│  │ single-stmt reads: READ ONLY tx │    is returned to the model │
│  │ row cap · query timeout         │    with the rule name       │
│  └────────────┬────────────────────┘                             │
│               ▼                                                  │
│  ┌ Layer 3 · driver ───────────────┐                             │
│  │ multiStatements=false: stacked  │                             │
│  │ injection impossible            │                             │
│  └────────────┬────────────────────┘                             │
│               │   guard decisions — allowed & denied — go to     │
│               │   audit: ring buffer (+ optional JSONL files)    │
└───────────────┬──────────────────────────────────────────────────┘
                ▼
      MySQL  —  Layer 0: dedicated least-privilege account
```

**Layer 0 — your MySQL account (strongly recommended).** Run the server with a dedicated account that has only the privileges you intend to use (read-only workloads get `SELECT` only). Never root. This is the containment layer everything below reinforces.

**Layer 1 — the AST main gate.** Every statement is parsed by the TiDB parser (parse failure ⇒ denied), then must pass, in order: single-statement enforcement → statement-class allowlist (with a read/write tool cross-check: a write sent through `mysql_query` is denied even if writes are enabled) → per-class enable switches → dangerous-construct scan (`SELECT ... INTO OUTFILE`/`DUMPFILE`, `LOAD_FILE()` at any nesting depth) → missing-`WHERE` tripwire → full table-reference extraction checked against the default-deny whitelist.

**Layer 2 — read-only transaction fallback.** Reads executed through the single-statement read path (`mysql_query`, `mysql_explain`, `mysql_list_tables`, `mysql_describe_table`) run inside `START TRANSACTION READ ONLY` — if the parser ever misclassified a write as a read, MySQL itself rejects it. (Write statements you explicitly enabled, and everything inside `mysql_script` — reads included — run outside this backstop; there, Layer 1 and Layer 0 are the controls.)

**Layer 3 — driver-level lockout.** The connection sets `multiStatements=false`, so `COMMIT; DROP TABLE ...`-style stacked injection is impossible at the protocol level even if every layer above failed.

Every denial comes back as machine-readable text — `DENIED [rule_name]: reason` — and the rule names are stable:

| Rule | Fires when |
|---|---|
| `parse_error` | The SQL fails to parse (fail-closed — syntax errors and parser gaps alike) |
| `multi_statement` | More than one statement in a single call |
| `unsupported_statement` | `SET` / `GRANT` / `CALL` / `USE` / `LOAD DATA` / `LOCK TABLES` / transaction control |
| `wrong_tool` | Write statement via `mysql_query`, or read statement via `mysql_execute` |
| `statement_not_enabled` | Statement class not listed in `allowed_statements` |
| `table_whitelist` | Any referenced table falls outside the whitelist |
| `dangerous_construct` | `INTO OUTFILE` / `INTO DUMPFILE` / `LOAD_FILE()` |
| `unfiltered_write` | `UPDATE` / `DELETE` without a `WHERE` clause |
| `script_ddl` / `script_too_long` / `script_empty` | DDL inside a script / script over the statement cap / empty script |
| `invalid_query` / `not_select` / `invalid_format` / `invalid_identifier` | Parameter validation of `mysql_explain` / `mysql_describe_table` |

One format variant: `mysql_script` denials read `DENIED [rule] 第 N 条: reason`, where `第 N 条` ("statement N") locates the offending statement inside the script.

The guard is the test suite's center of gravity: ~100 table-driven cases cover stacked-statement injection, versioned-comment smuggling, CTE-shadowing whitelist bypasses, `INSERT ... SELECT` table extraction, and more; end-to-end tests — whitelist enforcement, the READ ONLY backstop rejecting writes, script rollback, EXPLAIN-tree denials — run against a real MySQL 8.0 in testcontainers.

### The fine print

Security documentation you can't verify is marketing. The precise boundaries:

- The read-only transaction backstop covers the **single-statement read path**. Write types you explicitly enable — and every statement inside `mysql_script`, reads included, since they share the script's read-write transaction — execute without it; there, the AST gate plus your database account privileges (Layer 0) are the controls.
- `unfiltered_write` is a **missing-`WHERE` tripwire**, not full-table-write prevention: `UPDATE t SET a=1 WHERE 1=1` passes it. It catches mistakes, not malice.
- Two utility paths execute fixed, non-user SQL by design: `mysql_list_tables` queries `information_schema` directly (results filtered row-by-row through the whitelist), and `EXPLAIN FORMAT=TREE` executes a hardcoded constant prefix + the inner `SELECT` — the inner statement passes the **full** guard pipeline first (the TiDB parser cannot parse `FORMAT=TREE` as a whole statement).
- Audit records cover SQL that reaches the guard pipeline, allowed **and** denied. Not audited: `mysql_stats` calls, `mysql_describe_table` pre-check denials (`invalid_identifier` and its `table_whitelist` name check), and `mysql_explain` parameter denials (`invalid_query`, `not_select`, `invalid_format`). Script auditing follows actual execution: a guard-denied script yields one record for the whole script, and statements after a failed one — validated but never executed — are not recorded.
- The MySQL connection is **plain TCP** — no TLS option and no Unix socket yet. Keep the server and the database on a trusted network, or tunnel the connection.

## Configuration

Full annotated example: [config.example.yaml](config.example.yaml). The governing principle is **secure by default**: omit `allowed_statements` and you're read-only; omit `table_whitelist` and everything is denied; leave `block_unfiltered_writes` unset and it's on.

And it **fails closed at startup**: an unreadable file, an unknown/misspelled key, an invalid duration, a malformed whitelist pattern, an unknown statement type, a negative script cap, or a missing `mysql.user`/`mysql.database` all abort the process — it refuses to run sick rather than degrade silently.

| Key | Default | Notes |
|---|---|---|
| `mysql.host` | `127.0.0.1` | Use `host.docker.internal` from inside Docker |
| `mysql.port` | `3306` | |
| `mysql.user` | — required | Dedicated least-privilege account recommended |
| `mysql.password` | `""` | Use `${MYSQL_MCP_PASSWORD}` — see below |
| `mysql.database` | — required | Also used to qualify unqualified table names |
| `mysql.pool.max_open` / `max_idle` | `5` / `2` | Connection pool |
| `security.allowed_statements` | `[select]` | Any of `select` / `insert` / `update` / `delete` / `ddl`; `SHOW`/`DESCRIBE`/`EXPLAIN` ride on `select` |
| `security.table_whitelist` | `[]` = deny all | `db.table` patterns, glob per side (`myapp.*`, `app_*.logs`), case-insensitive |
| `security.max_rows` | `1000` | Result sets truncated beyond this, with a marker |
| `security.query_timeout` | `30s` | Per-query context timeout |
| `security.block_unfiltered_writes` | `true` | Deny `UPDATE`/`DELETE` without `WHERE` |
| `security.max_script_statements` | `50` | Statement cap per `mysql_script` call |
| `audit.enabled` | `false` | JSONL disk logging; in-memory session stats work regardless |
| `audit.log_dir` | `~/.mcp-server-mysql/logs` | Must be a mounted volume under Docker |
| `audit.slow_query_threshold` | `1s` | Queries above this are flagged slow |
| `audit.ring_buffer_size` | `1000` | In-memory window backing `mysql_stats` |

Secrets never need to live in the file: the whole config is passed through environment-variable expansion before parsing, so `${ENV_VAR}` works in **any** field. The config path itself can come from the `MYSQL_MCP_CONFIG` environment variable instead of `--config`.

## Audit log

Disk logging is controlled by `audit.enabled` — **default `false`: no log files, no log directory created**. Session statistics (`mysql_stats`) are backed by an in-memory ring buffer and work either way (reset on restart).

When enabled, JSONL files rotate daily (`audit-2026-07-02.jsonl`), one JSON object per line:

| Field | Meaning |
|---|---|
| `ts` / `tool` / `sql` | Timestamp, tool name, original SQL |
| `decision` / `rule` | `allowed` or `denied`, and the rule that fired on denial |
| `class` / `tables` | Statement class, referenced tables |
| `duration_ms` / `rows` | Latency, rows returned or affected |
| `slow` / `truncated` / `error` | Slow-query flag, truncation flag, error message |

## Claude Code skill

[skills/mysql-mcp](skills/mysql-mcp/SKILL.md) is a companion skill that teaches Claude to use these tools well: pick the right tool, respect the security boundaries (single statement, whitelist, `WHERE` tripwire), and read `DENIED [rule]` messages correctly instead of blindly retrying. Install:

```bash
cp -r skills/mysql-mcp ~/.claude/skills/
```

## Compatibility

- **MySQL 8.x** — the E2E suite runs against MySQL 8.0 (8.0.45) via testcontainers. MySQL 5.7 and MariaDB are untested.
- **Transport** — stdio; server identity `mcp-server-mysql`. Exposes 7 tools (no MCP resources or prompts).
- **Language note** — tool descriptions and runtime messages (result labels, `DENIED` reasons) are currently in Chinese. The rule names and record fields are English and stable, and modern LLMs handle the mixed output without issue.

## Development

```bash
go test ./... -short           # unit tests (no Docker needed)
go test ./... -timeout 600s    # full suite incl. testcontainers integration/E2E (needs Docker)
```

Design docs live in [docs/superpowers](docs/superpowers/) — each feature ships with a spec and an implementation plan.

## License

[Apache-2.0](LICENSE)
