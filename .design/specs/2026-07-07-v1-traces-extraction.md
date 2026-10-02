# v1 `bronto traces` — technical extraction (from `bronto/commands/traces.py`, 1179 lines)

Source of truth for re-implementing `traces` in Go. All field names, formulas,
and literal strings below are copied verbatim from the v1 Python.

## 0. Dataset targeting

- `DEFAULT_TRACES_FROM_EXPR = "logset = '.traces'"` — every request sets
  `from_expr` to this literal string. No per-service dataset selection; the
  `.traces` logset spans all services and `$service.name` is used to
  distinguish them.
- All requests go to `POST /search` via `client.post("/search", json=body)`.
- Common request body builder `_run_search(...)`:
  ```
  {
    "from_expr": from_expr,           # default "logset = '.traces'"
    "time_range": time_range,         # e.g. "Last 15 minutes"
    "select": select,                 # list[str]
    "limit": limit,
    "most_recent_first": bool,
    "where": where,                   # omitted key if None
    "groups": groups,                 # omitted key if None/empty
  }
  ```
- Response rows are read from `payload.get("result")`, falling back to
  `payload.get("events")` (`list` commands), or via `_group_rows` for
  aggregates: first non-empty of `payload["result"]`, `payload["groups"]`,
  `payload["data"]`.

## 1. Span data model

### 1.1 Fields requested on every per-span query (`SPAN_FIELDS`)
```
$span.trace_id
$span.span_id
$span.parent_span_id
$span.name
$span.kind
$span.duration_nano
$span.start_time_unix_nano
$span.end_time_unix_nano
$span.status_code
$service.name
$service.namespace
```
`list` and `show` prepend `"@time"` to this list in `select`.

### 1.2 Row → `Span` dataclass mapping (`_row_to_span`)
```
trace_id        = row["$span.trace_id"]  or ""
span_id         = row["$span.span_id"]  or ""
parent_span_id  = row["$span.parent_span_id"]  or ""
name            = row["$span.name"]  or ""
kind            = row["$span.kind"].replace("SPAN_KIND_", "")   # e.g. SERVER, CLIENT, INTERNAL, PRODUCER, CONSUMER
service         = row["$service.name"]  or ""
start_ns        = _int(row["$span.start_time_unix_nano"])
end_ns          = _int(row["$span.end_time_unix_nano"])
duration_ns     = _int(row["$span.duration_nano"])
status          = row["$span.status_code"]  or ""   # e.g. "STATUS_CODE_ERROR", "STATUS_CODE_UNSET", "STATUS_CODE_OK"
```
`_int(v)`: `int(float(v))`, catches `TypeError`/`ValueError` → `0` (tolerant
numeric coercion; values may arrive as strings/floats from the API).

Derived/backfill rules (in this order):
1. If `duration_ns == 0` and `end_ns > start_ns > 0`: `duration_ns = end_ns - start_ns`.
2. If `end_ns == 0` and `start_ns > 0` and `duration_ns > 0`: `end_ns = start_ns + duration_ns`.

Derived properties:
- `duration_ms = duration_ns / 1_000_000.0`
- `is_error = status.upper().endswith("ERROR")` — matches any status ending in
  "ERROR" (i.e. `STATUS_CODE_ERROR`), case-insensitively.

Timestamps are **unix nanoseconds** (`start_time_unix_nano`,
`end_time_unix_nano`); duration is **nanoseconds** (`duration_nano`).

### 1.3 Duration formatting (`_fmt_duration_ns`, used everywhere)
```
ns <= 0                → "—" (em dash)
ms = ns / 1_000_000.0
ms < 1                 → f"{ns/1_000:.1f}µs"     # microseconds, 1 decimal
1 <= ms < 1000         → f"{ms:.2f}ms"           # milliseconds, 2 decimals
ms >= 1000             → f"{ms/1000:.2f}s"       # seconds, 2 decimals
```

### 1.4 Root/entry span predicates
- **Root-span clause** (used server-side in `where`):
  `ROOT_ONLY_CLAUSE = "NOT EXISTS $span.parent_span_id"`.
- **Root span, client-side** (used in `show`/`shape` when reconstructing a
  tree from a fetched batch): a span is a root **within the fetched batch**
  if `span.parent_span_id not in by_id` (its parent isn't present in the
  result set — NOT necessarily the same as "no parent" server-side).
- **Entry span** (`shape`'s `entry_only`/`--entry` default): `$span.kind ==
  'SPAN_KIND_SERVER'` — i.e. `kind == "SERVER"` after stripping the prefix.
  This is explicitly *not* the same concept as root span; entry = SERVER-kind
  regardless of whether it has a parent (e.g. it may be a downstream
  service's ingress point within a larger trace).

## 2. Per-subcommand request/response/output specs

### 2.1 `services`
- Options: `--time-range/-t` (default "Last 15 minutes"), `--errors`,
  `--limit/-n` (default 50), `--output/-o`.
- `where`: `"$span.status_code = 'STATUS_CODE_ERROR'"` if `--errors` else `None`.
- Group key: `["$service.name"]`.
- Three separate aggregate queries via `_group_aggregate` (each its own
  `/search` call with `select=[aggregate]`, `groups=group_keys`):
  - `count(*)`
  - `avg($span.duration_nano)`
  - `max($span.duration_nano)`
  All three share `where`, `time_range`, `limit`.
- `_group_aggregate` parses each response row's `"group"` field via
  `_parse_group` (handles list, dict, or bracketed-string `"[a, b]"` forms)
  into a tuple key, and the aggregate's own key (`row[aggregate]`) as the
  value, coerced via `float(...)` (0.0 on failure/None).
- Merge: union of keys across the three dicts (`all_keys = set(counts) |
  set(avgs) | set(maxes)`); missing entries default to `0`/`0.0` via `.get(key, 0)`.
- Row shape: `{service, spans:int, avg:str, max:str}` — `avg`/`max` formatted
  with `_fmt_duration_ns`.
- Sort: descending by `spans`.
- Output columns (table): `service, spans, avg, max`. Title:
  `f"Services — {time_range}" + (" (errors only)" if errors_only else "")`.
- JSON mode (`-o json`): dumps `table_rows` (post-sort, pre-render) as-is.

### 2.2 `operations`
- Options: `--service/-s` (filters `$service.name`), `--time-range/-t`,
  `--errors`, `--limit/-n` (default 25), `--output/-o`.
- `where` clauses AND'd: `$service.name = '<service>'` (if given),
  `$span.status_code = 'STATUS_CODE_ERROR'` (if `--errors`).
- Group key: `["$service.name", "$span.name"]`.
- Same 3-query pattern (`count(*)`, `avg($span.duration_nano)`,
  `max($span.duration_nano)`) with this group key.
- Row shape: `{service, operation, spans, avg, max}` — `key[0]`→service,
  `key[1]`→operation (guarded with `len(key)>=N` checks, default `""`).
- Sort: descending by `spans`.
- Columns: `service, operation, spans, avg, max`. Title includes
  `(service=<x>)` and/or `(errors only)` suffixes when applicable.

### 2.3 `aggregate` (root-span / all-span attribute breakdown)
- Options: `--by/-b` (repeatable, **required**), `--root-only/--all-spans`
  (default `root_only=True`), `--service/-s`, `--kind/-k`, `--errors`,
  `--where/-w` (raw extra clause, wrapped in parens and AND'd), `--time-range/-t`,
  `--limit/-n` (default 50), `--include-empty/--hide-empty` (default hide),
  `--output/-o`.
- `_normalise_attr(attr)`: strips whitespace; raises `BadParameter` on empty;
  prefixes with `$` unless already present (`http.route` → `$http.route`).
  Group keys = `[_normalise_attr(a) for a in by]`.
- `where` clause assembly, AND-joined, in this order:
  1. `ROOT_ONLY_CLAUSE` if `root_only`
  2. `$service.name = '<service>'` if `--service`
  3. `$span.kind = 'SPAN_KIND_<KIND>'` if `--kind` (kind is upper-cased;
     prefixed with `SPAN_KIND_` unless already present)
  4. `$span.status_code = 'STATUS_CODE_ERROR'` if `--errors`
  5. `(<raw --where>)` if given
- **Overfetch**: `fetch_limit = max(limit * 5, 200)` — because "Bronto
  returns one aggregate per query and each query has its own top-N ordering,"
  so results are merged then trimmed client-side.
- Four aggregate queries (all with `fetch_limit`):
  - `count(*)` → `counts`
  - `avg($span.duration_nano)` → `avgs`
  - `max($span.duration_nano)` → `maxes`
  - if **not** `errors_only`: a 4th `count(*)` query with `where` = same
    clauses **plus** `$span.status_code = 'STATUS_CODE_ERROR'` appended →
    `errors` dict (error count per group, run only when not already filtered
    to errors).
- **Ranking ground truth is `counts`** — iteration is `for key, count_val in
  counts.items()`, other dicts are looked up via `.get(key, 0)`.
- Missing-value handling per group key: `values = list(key) + [""] *
  max(0, len(group_keys)-len(key))` pads short tuples; `has_missing = any(v in
  ("", "null", "None") for v in values[:len(group_keys)])`; if missing and not
  `--include-empty`, row is dropped (counted in `dropped_empty`).
- Row assembly: for each `attr, val` pair (attr with leading `$` stripped as
  key name), value passed through `_label_group_value(v)` → `v` unless `v in
  ("", "null", "None")` then `"<missing>"`.
  ```
  row[attr] = label_or_value
  n = int(count_val)
  err_n = int(errors.get(key, 0)) if errors else 0
  row["spans"] = n
  row["errors"] = err_n
  row["err%"] = f"{(err_n/n*100):.1f}" if n>0 and errors else ""
  row["avg"] = _fmt_duration_ns(int(avgs.get(key,0)))
  row["max"] = _fmt_duration_ns(int(maxes.get(key,0)))
  ```
- Sort by `spans` desc, then truncate to `rows[:limit]` (post-merge trim —
  this is what makes the overfetch meaningful).
- Columns: `[group_key_names..., "spans", "errors", "err%", "avg", "max"]`;
  `errors`/`err%` columns dropped entirely if `errors_only` or no `errors`
  dict was computed.
- Title: `f"Traces by {group_names} — {scope}, {time_range}[, service=..][,
  kind=..][, errors only]"` where `scope` = `"root spans"` if `root_only` else
  `"all spans"`.
- **Empty-result hint**: if no rows, `root_only` is true, and
  `dropped_empty > 0`, prints an info message suggesting root spans on
  ingress/proxy services often lack app attributes, recommending
  `--all-spans --kind server --service <name>`.

### 2.4 `list`
- Options: `--service/-s`, `--operation` (filters `$span.name`),
  `--min-duration-ms`, `--errors`, `--time-range/-t`, `--limit/-n` (default
  50), `--output/-o`.
- `where` AND-joined clauses:
  - `$service.name = '<service>'`
  - `$span.name = '<operation>'`
  - `$span.duration_nano > <int(min_duration_ms * 1_000_000)>` (ms→ns)
  - `$span.status_code = 'STATUS_CODE_ERROR'`
- Single query: `select=["@time", *SPAN_FIELDS]`, `most_recent_first=True`,
  no `groups`.
- JSON mode dumps the **raw payload**, not the transformed rows (`print_json(payload)`).
- Table rows: `{"@time": r["@time"], "service": span.service, "operation":
  span.name, "duration": _fmt_duration_ns(span.duration_ns), "status":
  span.status.replace("STATUS_CODE_", ""), "trace_id": span.trace_id,
  "span_id": span.span_id}`.
- Columns shown: `@time, service, operation, duration, status, trace_id`
  (`span_id` computed but not displayed). Title:
  `f"Spans ({len(rows)}) — {time_range}"`.

## 3. `show <trace_id>` — waterfall for one trace

- Options: `trace_id` (positional), `--time-range/-t` (default **"Last 1
  hour"**, wider than other commands), `--limit/-n` (default **500**),
  `--bar-width` (default 40), `--output/-o`.
- Query: `where="$span.trace_id = '<trace_id>'"`, `select=["@time",
  *SPAN_FIELDS]`, `most_recent_first=False` (chronological), `limit=500`.
- JSON mode dumps raw payload.
- If no rows: `print_error(...)` + `raise typer.Exit(code=1)`.

### 3.1 Tree construction
1. Parse rows → `spans: List[Span]` via `_row_to_span`.
2. `by_id = {s.span_id: s for s in spans if s.span_id}`.
3. `children: Dict[parent_span_id, List[Span]]` built via
   `children.setdefault(s.parent_span_id, []).append(s)` for every span
   (including roots, keyed by `""` or a missing parent id).
4. Each children list sorted by `(start_ns, duration_ns)` ascending.
5. **Orphan/root handling**: `roots = [s for s in spans if s.parent_span_id
   not in by_id]` — i.e. any span whose parent isn't in the fetched batch is
   treated as a root (this includes true roots with `parent_span_id == ""`
   and orphans whose parent fell outside the time window/limit).
   **Fallback**: if `roots` is empty (shouldn't normally happen, but e.g. a
   cyclic/self-referencing dataset), use `[min(spans, key=start_ns)]` — the
   single earliest span becomes the sole root.

### 3.2 Trace time bounds
```
trace_start = min(s.start_ns for s in spans if s.start_ns) or 0
trace_end   = max(s.end_ns for s in spans if s.end_ns) or trace_start
total_ns    = max(trace_end - trace_start, max(s.duration_ns for s in spans, default=1))
```

### 3.3 Header
`console.rule(f"trace {trace_id}")`, then info line:
`f"{len(spans)} span(s) across {len(distinct services)} service(s), total
{_fmt_duration_ns(total_ns)}"`.

### 3.4 Rendering — iterative DFS (explicitly avoids recursion limits)
- Stack of `(span, depth, is_last)` tuples; roots pushed in `reversed(roots)`
  order at `depth=0` so pop order is `roots[0], roots[1], ...`.
- `max_name_len = max(len(service)+len(name) for all spans) + 4`;
  `name_col_width = min(max_name_len, 70)`.
- Per node popped:
  - `indent = "  " * depth` (2 spaces per depth level).
  - `label = f"{service}/{name}"` (styled, `/` dimmed); `label_plain` is the
    unstyled version for length math.
  - `pad = max(1, name_col_width - len(indent) - len(label_plain))` — spaces
    between label and bar.
  - `bar = _render_bar(span, trace_start, trace_end, width)` (see below).
  - `status_part`: `" ERROR"` in red if `span.is_error`; else if
    `span.status` set and not ending in `"UNSET"`, dim-styled
    `status.replace("STATUS_CODE_", "")`; else empty.
  - Print: `f"{indent}{label}{pad spaces}{bar} {duration_fmt}{status_part}"`.
  - Children of this span (`children.get(span.span_id, [])`) pushed in
    `reversed(kids)` order at `depth+1` so first child pops first (preserves
    chronological sibling order).

### 3.5 Bar rendering (`_render_bar`, width default 40, reused conceptually by `show`)
```
total   = max(trace_end_ns - trace_start_ns, 1)
offset  = max(span.start_ns - trace_start_ns, 0) if span.start_ns else 0
length  = max(span.duration_ns, 1)
left_pad = int(offset * width / total)
bar_len  = max(1, int(length * width / total))
right_pad = max(0, width - left_pad - bar_len)
colour = "red" if span.is_error else "green"
```
Output string: `left_pad` × `·` (dim) + `bar_len` × `█` (colour) + `right_pad`
× `·` (dim). No clamping of `left_pad+bar_len` to `width` beyond `right_pad`
floor at 0 (so a bar can overflow past `width` if offset is large — not
explicitly clamped in v1).

## 4. `shape` — aggregated waterfall across many similar traces

Options: `--service/-s`, `--operation`, `--where/-w`, `--errors`,
`--min-duration-ms`, `--entry/--any-span` (default `entry_only=True`),
`--sample/-N` (default 30), `--time-range/-t` (default "Last 1 hour"),
`--bar-width` (default 50), `--min-traces` (default 1), `--output/-o`.

### 4.1 Step 1 — find sample trace IDs (`_find_sample_trace_ids`)
- Builds `where` from entry/service/operation/errors/min-duration/extra
  clauses, AND-joined:
  - `$span.kind = 'SPAN_KIND_SERVER'` if `entry_only`
  - `$service.name = '<service>'`
  - `$span.name = '<operation>'`
  - `$span.status_code = 'STATUS_CODE_ERROR'` if `errors_only`
  - `$span.duration_nano > <ms*1_000_000>` if `min_duration_ms`
  - `(<where>)` extra raw clause
- Query: `select=["$span.trace_id"]`, `limit=max(sample*3, 30)`,
  `most_recent_first=True` (i.e. **most recent spans matching filter**, not
  random sampling).
- Dedup trace_ids preserving order, **stop as soon as `len(result) >=
  sample`** (early break while iterating `payload["result"]`).
- If empty → error + exit 1.

### 4.2 Step 2 — fetch all spans of sampled traces (`_fetch_spans_for_traces`)
- Bronto's `/search` doesn't support `IN (...)` (returns 500 per code
  comment), so trace_ids are **batched into OR-chains**: `batch_size = 15`,
  `where = " OR ".join(f"$span.trace_id = '{t}'" for t in batch)`.
- Each batch query: `select=SPAN_FIELDS` (no `@time`), `limit=5000` (ceiling
  per batch), `most_recent_first=False`.
- All rows across batches parsed into a flat `List[Span]`.
- If empty after fetch → error + exit 1 ("Matched traces had no spans in the
  time window").

### 4.3 Step 3 — group spans by trace, re-root each at the matched entry span
- `by_trace: Dict[trace_id, List[Span]]`.
- Per trace:
  1. `by_id = {span_id: span}` for this trace's spans only.
  2. **Anchor selection** — `any_filter_active = bool(service or operation or
     errors_only or min_duration_ms is not None or entry_only)` (note:
     `entry_only` defaults `True`, so this is almost always true unless
     `--any-span` and no other filter given).
     - If filters active: `matches = [s for s in spans if _matches_entry(s)]`;
       anchor = earliest (`min` by `start_ns`) match, if any.
     - `_matches_entry(sp)` re-checks (client-side, redundant with/looser
       than the server `where`): kind==SERVER (if entry_only),
       service match, operation match, errors_only→`sp.is_error`,
       duration floor.
     - Fallback if no anchor yet: true root of this trace
       (`parent_span_id not in by_id`) or, absent any root, earliest span
       overall (`min(roots or spans, key=start_ns)`).
  3. **Subtree extraction**: BFS/stack (`queue.pop()` — actually LIFO, so
     DFS in practice) starting from `anchor`; a span is added to `subtree` if
     unvisited by `span_id`; children found each iteration by scanning
     **all** spans in the trace for `child.parent_span_id == cur.span_id`
     (O(n²) per trace — no children-index precomputed here, unlike `show`).
  4. `t0 = anchor.start_ns` — the reference zero point for this trace's offsets.
  5. **Identity computation relative to anchor**: temporarily sets
     `anchor.parent_span_id = ""` (mutates then restores in `finally`) so
     that `_span_identity` treats anchor as a root when walking up parent
     chains within `local_by_id = {s.span_id: s for s in subtree}`.
  6. `_span_identity(span, by_id, cache)`: recursively builds a tuple
     `((service, name), (service, name), ...)` from root to `span`, memoized
     per `span_id` in `cache`. Base case: `parent is None` or
     self-referencing parent → `((span.service, span.name),)`. Otherwise
     `parent_identity + ((span.service, span.name),)`.
  7. For each span in subtree: `ident = _span_identity(...)`; `parent_ident =
     ident[:-1] if len(ident) > 1 else None`; get-or-create a `ShapeBucket`
     keyed by `ident` (global `buckets` dict, shared **across all traces** —
     this is the grouping mechanism: identical (service,name) paths from
     different traces collapse into the same bucket); `offset = s.start_ns -
     t0 if s.start_ns else 0`; `bucket.add(trace_id, offset, s.duration_ns,
     s.is_error)`.

Note the anchor mutate/restore of `parent_span_id` is a side effect on the
shared `Span` objects guarded by `try/finally` — safe because it's restored
before moving to the next trace, but relevant if reimplementing without
mutation (just special-case anchor's identity directly).

### 4.4 `ShapeBucket` — aggregated stats per identity
Fields: `identity` (tuple path), `parent` (identity minus last hop, or
`None` for a root-of-subtree), `service`, `name`, `offsets_ns: List[int]`,
`durations_ns: List[int]`, `trace_ids: set`, `errors: int`.
- `add(trace_id, offset_ns, duration_ns, is_error)`: appends
  `max(offset_ns,0)` and `max(duration_ns,0)` (negative clamped to 0), adds
  trace_id to set, increments `errors` if `is_error`.
- `n_samples = len(offsets_ns)` (span occurrences, can exceed `n_traces` if a
  path repeats within one trace — e.g. loops/fan-out).
- `n_traces = len(trace_ids)` (distinct traces containing this identity).
- `avg_offset = int(mean(offsets_ns))`, `avg_duration = int(mean(durations_ns))`.
- `min_offset = min(offsets_ns)`.
- `max_end = max(o+d for o,d in zip(offsets_ns, durations_ns))` — max of
  (offset+duration) pairs, i.e. latest end time seen across samples for this
  identity (NOT max(offset)+max(duration) independently).

### 4.5 Filtering by `--min-traces`
`visible = {ident: b for ident,b in buckets.items() if b.n_traces >=
min_traces}` (default `min_traces=1`, so effectively no-op by default unless
raised). If `visible` empty → error + exit 1.

### 4.6 JSON output
Per visible bucket, sorted by `(len(identity), avg_offset)`:
```
{service, name, depth: len(identity)-1, parent: list(parent[-1]) or None,
 samples: n_samples, traces: n_traces, avg_offset_ns, avg_duration_ns,
 min_offset_ns, max_end_ns, errors}
```

### 4.7 Table rendering
1. Build `children: Dict[Optional[Identity], List[ShapeBucket]]` from
   `visible` keyed by `.parent`; sort each sibling group by `(avg_offset,
   -avg_duration)` (earlier first; longer duration wins ties).
2. `axis_end = max(avg_offset+avg_duration across visible)`, then
   `max(axis_end, max(max_end across visible))`, then `max(axis_end, 1)` —
   union of avg-based and worst-case-based extents, floor 1ns.
3. `total_samples = len(all_spans)` (raw span count fetched, pre-grouping);
   `n_traces_used = len(by_trace)` (traces that yielded ≥1 span, may be ≤
   `sample` if some sampled traces had no spans... though that case would've
   errored earlier if *all* had none).
4. Header info line: `f"{n_traces_used} trace(s), {total_samples} span(s) ·
   axis 0 → {fmt(axis_end)} · {time_range} · sample={n_traces_used} [·
   service=.. · op=.. · errors only]"`.
5. Legend line: `"legend: █ avg position · ▒ min/max spread · · before/after"`.
6. **Root list for display**: `children.get(None, [])`; if empty (all
   buckets ended up with a `parent` set — shouldn't normally happen since at
   least the anchor bucket has `parent=None`), fallback to all buckets at
   `min(len(identity) for b in visible)` depth, sorted by `avg_offset`.
7. **Flat DFS row list**: stack-based, `stack = [(b,0) for b in
   reversed(roots_list)]`; pop, append to `display_rows`, then push children
   (`children.get(bucket.identity, [])`, reversed) at `depth+1`. Order
   mirrors `show`'s DFS.
8. Column width budget: `any_errors = any(b.errors for b in visible)`;
   `reserved = width + 9 + 9 + 7 + (4 if any_errors else 0) + 10` (bar +
   avg-col + offset-col + n-col + optional err-col + padding fudge);
   `name_col_width = max(16, min(60, console.width - reserved))`.
9. Table columns: `span` (name, ellipsis-truncated, width=name_col_width),
   `waterfall` (width=`width`, default 50), `avg` (right, width 9), `@offset`
   (right, width 9, dim), `n` (right, width 7, dim), optional `err` (right,
   width 4) if `any_errors`.
10. Per row: `label = f"{indent}{service}/{name}"` (indent = `"  "*depth`);
    `bar = _render_shape_bar(bucket, axis_end, width)`;
    `presence = f"{n_traces}/{n_traces_used}" if n_traces < n_traces_used else
    f"{n_traces}"` (the "k/N" column — shows fraction only when the node
    didn't appear in every sampled trace, otherwise bare count); `avg` cell =
    bold `_fmt_duration_ns(avg_duration)`; `@offset` cell =
    `_fmt_duration_ns(avg_offset)`; `err` cell = red count or empty string.

### 4.8 Aggregated bar glyphs (`_render_shape_bar`)
```
trace_span_ns = max(axis_end, 1)  # guard against 0
avg_left  = int(avg_offset * width / trace_span_ns)
avg_len   = max(1, int(avg_duration * width / trace_span_ns))
avg_right = avg_left + avg_len

min_off  = max(bucket.min_offset, 0)
max_end  = max(bucket.max_end, min_off + 1)
band_left  = int(min_off * width / trace_span_ns)
band_right = max(band_left + 1, int(max_end * width / trace_span_ns))
band_left  = min(band_left, width - 1)   # clamp band into [0, width]
band_right = min(band_right, width)

colour = "red" if bucket.errors > 0 else "green"
```
Per cell `i` in `range(width)`:
- `avg_left <= i < avg_right` → `█` in `colour` — **average position/duration
  bar** (takes priority).
- else `band_left <= i < band_right` → `▒` dim-cyan — **min/max spread band**
  (min_offset to max_end across all samples in this bucket).
- else → `·` dim — outside both ranges.

Note: unlike `show`'s `_render_bar`, this one **clamps** `band_left`/`band_right`
into `[0, width]`, but does **not** clamp `avg_left`/`avg_right` (an outlier
average could theoretically run past `width`, producing an out-of-range
slice — Python slicing/range would just not iterate past `width-1` since the
loop is `for i in range(width)`, so in practice avg segments beyond `width`
are silently truncated by the loop bound, not an explicit clamp).

## 5. Gotchas / precise semantics to preserve

1. **Root span ≠ entry span.**
   - Root (server-side, `aggregate`'s `--root-only`): `NOT EXISTS
     $span.parent_span_id` — true absence of a parent id in the span record.
   - Root (client-side, `show`/`shape` subtree building): parent id not
     present in the **currently fetched batch** (`parent_span_id not in
     by_id`) — an artifact of pagination/time-range, can misclassify a
     genuinely non-root span as a root if its parent fell outside the query
     window/limit.
   - Entry (`shape` default): `$span.kind == 'SPAN_KIND_SERVER'`, regardless
     of parent — the "APM endpoint" mental model, deliberately different from
     root because ingress/proxy root spans often lack app-level attributes.
2. **`--root-only/--all-spans`** (aggregate command): default **root_only =
   True**. **`--entry/--any-span`** (shape command): default **entry_only =
   True**. Both invert the naive "show everything" default — callers must
   opt out explicitly.
3. **Kind values**: raw API values are `SPAN_KIND_SERVER`, `SPAN_KIND_CLIENT`,
   `SPAN_KIND_INTERNAL`, `SPAN_KIND_PRODUCER`, `SPAN_KIND_CONSUMER`. CLI
   accepts bare `SERVER`/`CLIENT`/etc via `--kind` and auto-prefixes
   `SPAN_KIND_` if missing, upper-cased. Internally after fetch, `Span.kind`
   has the `SPAN_KIND_` prefix stripped (`_row_to_span`), so comparisons
   against parsed spans use bare `"SERVER"` etc., while comparisons in
   **where clauses** sent to the API use the full `SPAN_KIND_*` form. Do not
   conflate the two representations when porting.
4. **Status values**: raw API values like `STATUS_CODE_ERROR`,
   `STATUS_CODE_UNSET`, `STATUS_CODE_OK`. `is_error` matches
   `status.upper().endswith("ERROR")` (endswith, not equals — tolerant of
   case/prefix variance). Display strips `STATUS_CODE_` prefix.
5. **Missing/empty attribute handling** (`aggregate`): group values equal to
   `""`, `"null"`, or `"None"` (as strings) are treated as "missing" and
   labeled `<missing>` (`_label_group_value`); rows with any missing group
   value are dropped unless `--include-empty`. This 3-way string check
   (empty/`"null"`/`"None"`) appears in two places (`aggregate`'s
   `has_missing` check and `_label_group_value`) — must match exactly, since
   Bronto's API apparently returns literal string `"null"`/`"None"` for
   absent attributes rather than JSON null in some cases.
6. **Aggregate merge-then-trim pattern**: because each aggregate query
   (`count`/`avg`/`max`) has independent top-N ordering server-side,
   `aggregate` overfetches (`limit*5` or 200, whichever bigger) on every
   query, unions keys, then re-sorts and trims client-side to the requested
   `--limit`. `services`/`operations` do NOT overfetch — they just union the
   three dicts at the requested `limit` directly (potential inconsistency
   inherited from v1: those two commands could show mismatched top-N vs.
   `aggregate`, which self-corrects via overfetch).
7. **`IN (...)` unsupported**: `/search` returns 500 for `IN`, so
   multi-trace-id fetches must use OR-chains, batched at **15 trace_ids per
   request** (`_fetch_spans_for_traces`), each with `limit=5000` (a ceiling
   that could silently drop spans for very high-fan-out sampled traces —
   no warning is emitted if a batch hits the 5000 cap).
8. **`_find_sample_trace_ids` sampling is not random** — it's "most recent
   matching spans, deduplicated by trace_id, first N distinct" (
   `most_recent_first=True`, early-break at `sample` distinct ids, overfetch
   factor `sample*3` or 30 minimum). This biases toward traces with entry
   spans recently observed, not a uniform sample over the time range.
9. **Pagination/limit ceilings observed**: `services`/`operations` default
   limits 50/25; `aggregate` default 50 (with 5x/200 floor overfetch);
   `list` default 50; `show` default 500 (single trace, all its spans);
   `shape`'s trace-id lookup floor `max(sample*3, 30)`; `shape`'s per-batch
   span fetch hard limit 5000. None of these are configurable beyond what's
   exposed as `--limit`/`--sample`; there is no cursor/offset-based
   pagination anywhere — a single `limit` per request, no follow-up pages.
10. **`shape` anchor re-rooting mutates and restores `parent_span_id`** on
    the shared `Span` object (`anchor.parent_span_id = ""` in a
    `try/finally`) purely to make `_span_identity`'s upward walk stop at the
    anchor. A Go port should instead special-case "anchor has no parent for
    identity purposes" without needing a mutate/restore dance.
11. **Identity/grouping key is a full path**, not just `(service, name)` —
    `Identity = Tuple[Tuple[str,str], ...]` from anchor down to the span.
    Two spans with the same `(service, name)` but different ancestor chains
    (e.g. same downstream call made from two different parent operations)
    become **separate** buckets/rows. Memoization is per-trace
    (`cache: Dict[str, Identity]` is local to each trace's identity pass),
    but the **bucket dict `buckets` is global across all sampled traces** —
    identical full paths from different traces merge into one bucket.
12. **`n_samples` vs `n_traces`**: a bucket can have more samples than traces
    if the same (service,name) path occurs multiple times within one trace
    (loops, retries, fan-out) — `n_traces` (distinct trace_ids) is what
    `--min-traces` filters on and what's shown in the `n` column as `k/N`,
    not `n_samples`.
13. **Subtree BFS in `shape` is actually DFS** (`queue.pop()` = LIFO) and is
    O(n²) per trace (scans all trace spans per queue-popped node to find
    children) — no precomputed children-by-parent index, unlike `show` which
    does build one. Worth optimizing in the Go port but must preserve
    equivalent traversal *results* (dedup via `seen_ids`, so traversal order
    doesn't affect the final `subtree` set, only doesn't matter for
    correctness — only matters if there's a performance rewrite).
14. **`show`'s bar can overflow width** on the right without explicit clamp
    beyond `right_pad = max(0, width - left_pad - bar_len)` (right_pad floors
    at 0 but left_pad+bar_len is never capped to `width`); `shape`'s band
    clamp IS explicit (`min(band_left, width-1)`, `min(band_right, width)`)
    but its avg segment is not explicitly clamped (relies on the `range(width)`
    loop bound to truncate). Both quirks should be deliberately decided
    (clamp vs. not) rather than silently replicated, but note them as
    existing v1 behavior.
15. **Two-decimal/one-decimal duration formatting is exact**: µs uses 1
    decimal (`.1f`), ms and s both use 2 decimals (`.2f`). Threshold is on
    **milliseconds value**, not nanoseconds: `< 1ms` → µs branch; `< 1000ms`
    (i.e. < 1s) → ms branch; else → s branch. Zero or negative ns → em dash
    `"—"` (not "0ms" or "0µs").
