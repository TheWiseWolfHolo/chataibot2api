# Admin Quota Dashboard Read-Only Design

Date: 2026-03-30
Status: Proposed and user-approved in conversation; awaiting written-spec review
Scope: `web/admin/*`, read-only admin handlers, read-only pool snapshot/probe support
Out of scope: auto-fill, prune behavior changes, import/migration behavior changes, persistence format changes

## 1. Goal

Upgrade the existing admin console so the first screen answers three questions immediately without mutating pool state:

1. How many accounts are currently in the pool?
2. How much quota remains in total?
3. Which accounts are close to running out?

The page should preserve the current strong visual identity (high contrast, heavy borders, hard shadows, bold type) while reducing noise and collapsing secondary modules.

## 2. Non-negotiable boundaries

The implementation must not:

- trigger fill automatically
- trigger prune automatically
- trigger import automatically
- trigger migration automatically
- alter pool target counts or low-watermark behavior
- change persistence file format
- write probe results back into the pool
- delete or downgrade accounts based on read-only probe results

Read-only runtime checks are allowed only when explicitly requested by the user through the UI.

## 3. User-approved product decisions

### 3.1 Page structure

The admin dashboard first screen becomes:

1. top toolbar
2. quota overview strip
3. quota detail table
4. collapsed secondary sections
   - model support
   - migration
   - danger actions
   - logs

Secondary sections must be collapsed by default.

### 3.2 Overview metrics

The first-row overview shows exactly four metrics:

- total account count
- total remaining quota
- low-quota account count
- near-empty account count

Definitions:

- low quota: `2 <= quota < 10`
- near empty: `quota < 5`

Note: the summary metrics are intentionally overlapping. `near empty` is a subset of `low quota`; they are not two disjoint buckets.

### 3.3 Detail table

Default columns:

- quota
- status
- jwt
- pool bucket
- last checked at

Default sort:

1. grouped by status
2. within each group, ascending quota

JWT display:

- masked by default
- expandable to full value on demand

### 3.4 Filter and actions

The primary toolbar above the table includes:

- status filter
- pool bucket filter
- keyword search
- sorting control
- refresh
- "probe current filter" action

The probe action applies only to the currently filtered rows.

### 3.5 Visual direction

Keep the current neo-brutalist identity, but do subtraction instead of re-theming:

- keep heavy borders, hard shadows, strong hierarchy, bold typography
- remove decorative clutter and duplicate signals
- remove the stray `LIVE` decoration
- reduce repeated badges/pills and long explanatory copy
- keep the first screen focused on overview + table

## 4. Data model and definitions

### 4.1 Status mapping

Each row exposes a derived display status from quota:

- `near-empty`: `quota < 5`
- `low`: `5 <= quota < 10`
- `healthy`: `quota >= 10`
- `probe-error`: only for client-side temporary display when live probe fails for a row

`probe-error` is presentation-only and must never be persisted.

### 4.2 Pool bucket mapping

Each row must identify its source bucket:

- `ready`
- `reusable`
- `borrowed`

If a future row source is unknown, surface `unknown` explicitly rather than guessing.

### 4.3 Checked timestamp

`last_checked_at` means the most recent confirmed timestamp for the quota value currently shown.

Rules:

- cached snapshot rows may have no timestamp and should display `—`
- live probe rows receive the probe completion timestamp
- mixed table states are allowed; each row carries its own timestamp
- overview totals remain based on cached snapshot data unless a future design explicitly introduces a separate recalculated summary

## 5. Backend design

### 5.1 Keep existing endpoints

These endpoints stay in place and continue serving their current purposes:

- `GET /v1/admin/pool`
- `GET /v1/admin/pool/export`
- `GET /v1/admin/meta`
- `GET /v1/admin/catalog`
- `GET /v1/admin/migration/status`

No admin write endpoint behavior changes are part of this work.

### 5.2 New read-only snapshot endpoint

Add a dedicated endpoint:

- `GET /v1/admin/quota/snapshot`

Purpose:

- provide one normalized payload for the overview + detail table
- avoid forcing the client to reconstruct bucket placement and derived counts from unrelated endpoints

Response shape:

```json
{
  "summary": {
    "total_count": 0,
    "total_quota": 0,
    "low_quota_count": 0,
    "near_empty_count": 0
  },
  "rows": [
    {
      "jwt": "token",
      "quota": 0,
      "status": "healthy",
      "pool_bucket": "ready",
      "last_checked_at": null
    }
  ]
}
```

Construction rules:

- enumerate `ready`, `reusable`, and `borrowed` separately from in-memory pool state
- deduplicate by JWT
- preserve pool bucket for each row
- derive `status` from `quota`
- compute summary totals from the same cached snapshot rows
- do not mutate pool state while building the snapshot
- do not persist anything while building the snapshot

### 5.3 New read-only probe endpoint

Add:

- `POST /v1/admin/quota/probe`

Purpose:

- fetch live upstream quota for the rows currently selected in the UI
- return probe-only results without mutating the pool

Request shape:

```json
{
  "jwts": ["token-a", "token-b"]
}
```

Response shape:

```json
{
  "checked_at": "2026-03-30T00:00:00Z",
  "results": [
    {
      "jwt": "token-a",
      "quota": 12,
      "status": "healthy",
      "ok": true,
      "error": ""
    },
    {
      "jwt": "token-b",
      "ok": false,
      "error": "upstream quota request failed"
    }
  ]
}
```

Probe rules:

- accept only explicit JWT input from the client
- do not call prune/fill/import/migrate
- do not add/remove/move accounts in the pool
- do not call persistence save
- do not update persisted counts
- do not overwrite cached row values in backend memory
- failures are returned as row-level errors

The client may temporarily overlay these results for presentation within the current page session only.

### 5.4 Pool read helper support

The pool layer should expose a read-only snapshot helper that returns rows with bucket information without leaking mutable internals.

Expected helper behavior:

- gather rows under lock
- copy values into a detached slice
- include bucket label
- never return pointers to mutable account structs

## 6. Frontend design

### 6.1 Overview strip

Replace the current bulky summary block with a tighter overview strip containing the four approved metrics.

Constraints:

- one row on common desktop widths
- wraps cleanly on narrow widths
- no duplicate copy of the same state elsewhere on the first screen

### 6.2 Main table section

The table becomes the dominant first-screen section.

Toolbar responsibilities:

- status filter
- bucket filter
- keyword search against JWT
- sort control
- refresh action
- live probe action for current filter result

Table row behavior:

- masked JWT by default
- expand/collapse full JWT inline
- clear status styling based on row state
- show row-local timestamp or `—`
- on probe error, keep cached quota visible and label the row as probe failed

### 6.3 Collapsible secondary sections

Convert these modules to collapsible sections, collapsed by default:

- models
- migration
- danger actions
- logs

Behavior:

- heading always visible
- content rendered only when expanded or rendered hidden without affecting first-screen density
- no large explanatory blocks at top level

### 6.4 Style adjustments

Keep the existing aesthetic, but simplify:

- remove unnecessary floating decoration, including `LIVE`
- reduce decorative labels to the minimum useful set
- tighten spacing in the summary area
- preserve contrast, border language, and typography weight
- avoid introducing a new visual system

## 7. Error handling

### 7.1 Snapshot load failure

If snapshot fetch fails:

- keep the last successful snapshot in UI memory
- surface a clear top-level error
- do not replace the screen with a blank success-looking state

### 7.2 Probe partial failure

If some rows fail during probe:

- mark only those rows as probe failed
- keep cached quota visible as fallback context
- do not treat probe failure as account deletion or invalidation

### 7.3 Mixed cached/live state

After a probe, the table may contain both cached and live rows.

Rules:

- row-level display may use live data where available
- overview summary remains snapshot-based for consistency
- UI should label the probe scope clearly, e.g. current filter probed N rows

## 8. Verification strategy

### 8.1 Automated verification

Add or update tests to verify:

1. snapshot summary totals are correct
2. low/near-empty counts follow the approved thresholds
3. rows preserve bucket labels
4. probe handler is read-only and does not trigger persistence or mutation
5. probe errors remain row-local

### 8.2 Runtime verification

Before and after UI interactions, compare:

- `GET /v1/admin/pool`
- `GET /v1/admin/pool/export`

The following must remain unchanged after page refreshes and live probes alone:

- `total_count`
- `persisted_count`
- `prune_removed`
- exported account count

### 8.3 Safety precondition

Before implementation or deployment, export a read-only pool backup from the live service and keep the artifact outside the repo.

## 9. Implementation boundaries

This spec authorizes only:

- read-only backend snapshot/probe support
- admin UI restructuring and simplification
- frontend state management for cached vs probed display
- tests for the new read-only behavior

This spec does not authorize:

- account pool recovery work
- persistence redesign
- automatic replenishment logic changes
- migration automation changes
- background quota synchronization jobs

## 10. Open questions intentionally resolved in this spec

The following choices are fixed here to avoid ambiguity during implementation:

- near-empty threshold is `< 5`
- low-quota summary remains `2 <= quota < 10`
- probe scope is current filter only
- default sort is status group then ascending quota
- JWT is masked by default and manually expandable
- overview remains snapshot-based even after partial live probe
