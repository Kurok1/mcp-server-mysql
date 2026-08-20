# Query Results MCP App — Design QA

Date: 2026-08-20

## Evidence

- Visual source: [selected second concept](ui/query-results/reference/query-results-v2.png)
- Desktop implementation: [1440 × 1024](docs/design-qa/query-results-1440x1024.png)
- Side-by-side review: [reference and implementation](docs/design-qa/query-results-comparison.png)
- Responsive drawer: [899px viewport](docs/design-qa/query-results-under-900.png)
- Responsive toolbar/table: [639px viewport](docs/design-qa/query-results-under-640.png)
- Dark theme: [desktop dark theme](docs/design-qa/query-results-dark.png)
- Protocol integration: [official Basic Host with a real MySQL result](docs/design-qa/basic-host-integration.png)

## Visual review

- The desktop composition matches the selected concept: approximately 25% query-history rail, SQL detail below the history, right-side result header and controls, fixed table header, dense data grid, and bottom selection actions.
- Typography, border weight, radii, spacing, light palette, selected-row treatment, and control hierarchy were checked in the combined comparison image.
- The implementation intentionally omits the concept's generated avatars and semantic money/status formatting. Arbitrary SELECT columns and values remain unchanged, as required by the product specification.
- At 899px the history rail becomes a 360px drawer with a backdrop. At 639px the toolbar wraps and the table retains horizontal scrolling.
- The dark theme uses the same component structure and hierarchy. English and Simplified Chinese copy were checked, including the document language attribute.

## Interaction review

- Verified global filtering, optional `status` filtering, natural numeric sorting, column visibility, row selection, TSV/CSV/JSON copy, and JSON's `{columns, rows}` representation.
- Verified standalone preview refresh, history insertion, selection reset, and result-ID de-duplication behavior.
- Verified loading, empty, no-row, truncated, cancelled/error, failed-refresh, and host-without-tool-calling states through the frontend test suite.
- Verified the official `ext-apps` Basic Host against `http://127.0.0.1:3001/mcp` and a temporary MySQL 8.0 database: initial SELECT, embedded `ui://` resource render, manual refresh, history switching, status filtering, sorting, column visibility, row selection, and clipboard JSON copy all passed.

## Defects

- P0: none
- P1: none
- P2: none

final result: passed
