# Archive validation and benchmark procedure

The archive regression has two layers. `TestArchiveDashboardParitySynthetic`
runs with `go test` and uses a fixed Asia/Shanghai clock of 2026-09-17
10:00 (+08:00), giving a cutoff of 2026-08-17T16:00:00Z. It creates both
Claude and Codex sessions that cross that cutoff, includes late retained raw
rows, and checks known summary totals before comparing all public dashboard
responses before and after compaction. It then runs a second sweep to verify
idempotence.

Run the standard regression with:

```bash
go test ./internal/dashboard -run '^TestArchiveDashboardParitySynthetic$' -count=1
```

The production-shaped backup check is opt-in. Set the source path in the
environment; the test opens it only for reading, copies it with Go `io.Copy`
into `t.TempDir()`, then runs migrations and archival writes only on that
copy. It emits aggregate row counts only and never logs sessions, identities,
prompts, or `attrs` values.

```bash
MONITOR_ARCHIVE_TEST_DB=/path/to/monitor.duckdb.bak \
  go test ./internal/dashboard -run '^TestArchiveDashboardParityBackup$' -count=1 -v
```

The benchmark uses the same temporary-copy rule. It runs the full public
dashboard suite for raw data, after archiving, and after adding a temporary
70,080-row historical `archive.usage_hourly` expansion plus 1,000 distinct
archive sessions. The generated primary keys are unique and the source backup
is not modified.

```bash
MONITOR_ARCHIVE_TEST_DB=/path/to/monitor.duckdb.bak \
  go test ./internal/dashboard -run '^$' -bench '^BenchmarkArchiveDashboardBackup$' -benchmem -benchtime=1x -count=1
```

Each benchmark result reports Go's `ns/op`, allocation metrics, and 20 measured
end-to-end samples (`p50_ms`, `p95_ms`) for snapshot all/month, heatmap all,
rankings all/all, rates codex/month, sessions all/limit 100, and one selected
cross-boundary session detail. It also reports `components/*` p50/p95 probes
for historical-summary SQL, retained-raw SQL, a representative Go bucket
merge, and a read transaction. The SQL probes include execution and row scan;
the merge probe uses already-loaded typed bucket rows. These component probes
need no production timing hooks and should not be added together to estimate
endpoint latency. Do not interpret a raw-versus-archived result as proof of a
speedup over a prior SQL design.

Record the embedded DuckDB runtime used on a machine with the following
read-only query against the temporary test database, or from an archive test
log if it prints the value:

```sql
SELECT version();
```

## Recorded run

Results are recorded after the integration and opt-in benchmark commands run
against the frozen 2026-09-17 cutoff. The backup baseline expected by the
validation fixture contains 424,559 rows, of which 310,985 are older than the
cutoff; expected archive summary cardinality is about 6,736 rows and there
are 838 visible sessions. These are aggregate checks only. The six Codex
sessions crossing the boundary are exercised without printing their IDs.

| Check | Result |
| --- | --- |
| Synthetic parity, late expired data, Top-N, and read snapshot | Pass (2026-09-17) |
| Backup parity and idempotence | Pass: 424,559 → 113,574 raw rows; 6,736 archive summary rows; 310,985 deleted across 71 days |
| DuckDB runtime | v1.4.3 (go-duckdb v2.4.3, Apple M1 Pro) |
| Backup source checksum | Unchanged before/after validation (`1a504c0c4ae95bce32871673c573e230c0a0931de0b314c1930ed7095f8caf00`) |

The following command used `-benchmem -benchtime=1x`; each p50/p95 value is
from 20 individual public API builder/query/merge/JSON-encoding calls on the
same new implementation before and after data compaction. HTTP and network
transport are excluded. `allocs/api` and `bytes/api` are per call. Times are
milliseconds.

| API | Raw p50/p95 | Archived p50/p95 | Expanded archive p50/p95 |
| --- | ---: | ---: | ---: |
| Snapshot all/month | 52.07 / 57.65 | 41.08 / 43.24 | 43.08 / 43.99 |
| Heatmap all | 17.58 / 19.35 | 15.81 / 17.53 | 17.72 / 18.82 |
| Rankings all/all | 6.39 / 6.92 | 7.69 / 9.79 | 9.14 / 14.60 |
| Rates codex/month | 9.67 / 10.51 | 9.89 / 11.46 | 13.80 / 19.45 |
| Sessions all, limit 100 | 43.79 / 53.61 | 34.52 / 37.11 | 35.69 / 38.09 |
| Cross-boundary session detail | 6.29 / 6.92 | 4.83 / 5.10 | 4.96 / 5.16 |

Allocation counts (raw / archived / expanded) were: snapshot 4,542 / 5,205 /
6,169; heatmap 3,655 / 4,369 / 10,139; rankings 2,171 / 3,678 / 3,788; rates
10,960 / 10,930 / 10,803; session list 5,448 / 7,870 / 7,869; detail 862 /
942 / 942 allocs per API call. Allocation bytes were respectively: snapshot
199,701 / 214,866 / 243,285; heatmap 188,920 / 203,874 / 342,635; rankings
84,264 / 113,163 / 112,563; rates 416,934 / 415,019 / 439,462; session list
286,574 / 476,341 / 476,461; detail 35,366 / 36,760 / 37,571 bytes per call.

Archived component probes, which include SQL execution and row scanning, were
historical-summary SQL 925.8 / 1,188 µs, retained-raw SQL 402.1 / 459.7 µs,
representative Go bucket merge 4.375 / 9.875 µs, and a representative read
transaction 1,692 / 2,169 µs (p50 / p95). These are component measurements,
not an additive latency model.

The backup is a single local sample, so its timing does not represent every
deployment. The expanded-history data is synthetic and intentionally stresses
hour/model/session cardinality; it does not reproduce a real user's event
distribution or cache locality.
