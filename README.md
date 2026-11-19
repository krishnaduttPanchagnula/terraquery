# terraquery

Cross-state dependency resolver for Terraform state files. Ingest statefiles,
infer which app depends on which other app, explore the architecture visually,
and query everything with read-only SQL. Everything ships as **one Go binary**
with the web UI, charts and icons embedded — no CDN, no external services.

## Install

### Homebrew

```sh
brew install krishnaduttPanchagnula/tap/terraquery
```

### Binary download

Grab the archive for your platform from the
[latest release](https://github.com/krishnaduttPanchagnula/terraquery/releases/latest)
and put `terraquery` on your `PATH`.

### From source

```sh
go install github.com/krishnaduttPanchagnula/terraquery@latest
# or
git clone https://github.com/krishnaduttPanchagnula/terraquery.git
cd terraquery
go build -o terraquery .
```

Requires Go 1.22+. The build is pure Go (CGO not required); SQLite is embedded.

## Quick start

```sh
# Explore the bundled multi-cloud demo in the browser
terraquery -serve -addr :8080 -db terraquery.db
# open http://localhost:8080 → "+ Add statefiles" → "Bundled samples"

# Or ingest statefiles from the CLI
terraquery -v -db terraquery.db \
  network=path/to/network.tfstate app=path/to/app.tfstate

# Query the stored architecture
terraquery -db terraquery.db -query \
  "SELECT from_state, to_state, confidence FROM dependencies"
```

Use `name=path` to set a state's name explicitly (also used to match
`terraform_remote_state` keys); otherwise it defaults to the file stem.

## CLI modes

```
terraquery [-min 0.5] [-v] [-json out.json] [-db terraquery.db] [name=]state.tfstate ...
terraquery -serve [-addr :8080] [-db terraquery.db]
terraquery -query "SELECT ..." [-db terraquery.db]
```

`-db` is optional for the CLI (omit it for a one-shot analysis); the server
requires it. An empty string disables persistence.

## Web UI

Open `http://localhost:8080/`. The layout fills the viewport: a left nav rail,
a top bar, and a split workspace — **left column**: overview, services,
dependencies, evidence and SQL; **right column**: a tall sticky architecture
canvas. A theme toggle in the top bar switches between dark and light (the
default follows your OS preference and the choice is remembered). Below 1100px
the columns stack; below 900px the nav rail collapses to icons.

- **Add statefiles** drawer with three sources: **S3 browser** (list buckets →
  browse prefixes → tick statefiles → add), **Bundled samples** (six multi-cloud
  demo states with cross-references), and **Upload files** (drag/drop or pick).
- **Overview** stat cards plus a donut chart of evidence by kind.
- **State dropdown with checkboxes** shows/hides statefiles across the graph,
  tables and charts. Each row has a button to inspect that state.
- **Free-flowing canvas**: drag to pan, scroll or the zoom buttons to zoom,
  reset with the fit button, and use the back icon in the canvas corner to
  return to all states.
- **Dependency graph** between statefiles (overview): arrow = "depends on".
  Click a state box to drill into its architecture.
- **Architecture diagram** (drill-down): a container with dashed component
  panels (Messaging, Storage, Data, Compute, Networking, …), official AWS
  service icons with labels, and arrows for connections. Services in other
  statefiles appear in dashed panels on the right. Click a service for the
  matched identifier. The **full-screen icon** enlarges the *live* canvas, so
  drilling in and out still works while enlarged.
- **Services** panel: resource types grouped by statefile. Clicking a group
  drills into that state.
- **SQL box** for arbitrary read-only queries.

### S3 configuration

Uses the standard AWS credential chain (env vars, `~/.aws`, IMDS). Set
`AWS_REGION` (or `AWS_DEFAULT_REGION`). For S3-compatible emulators set
`AWS_ENDPOINT_URL_S3` (path-style addressing is used automatically). Required
IAM actions: `s3:ListAllMyBuckets`, `s3:ListBucket`, `s3:GetObject`. Without
credentials the API returns a clear message and the UI offers bundled samples
or file upload instead.

## HTTP API

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/health` | status |
| POST | `/api/states` | ingest states |
| GET | `/api/states` | list ingested states |
| GET | `/api/resources?state=` | resource instances (optionally one state) |
| POST | `/api/resolve` | recompute edges from stored states |
| GET | `/api/deps?min=0.5` | state-level dependencies |
| GET | `/api/edges?min=0.5&limit=100` | raw evidence edges |
| GET | `/api/graph?min=0.5` | nodes + edges for visualization |
| GET/POST | `/api/query` | read-only SQL |
| GET | `/api/s3/status` | S3 configuration status |
| GET | `/api/s3/buckets` | list buckets |
| GET | `/api/s3/objects?bucket=&prefix=&token=` | browse a bucket (folders + files) |
| POST | `/api/s3/ingest` | `{bucket, keys:[...]}` → download and ingest |
| GET | `/api/samples` | list bundled demo statefiles |
| POST | `/api/samples/ingest` | `{samples:[names]}` (empty = all) |

Upload multipart files under field `states` (optional comma-separated
`names`), or JSON:

```sh
curl -F states=@network.tfstate -F states=@app.tfstate \
  http://localhost:8080/api/states

curl -s -H 'Content-Type: application/json' \
  -d '{"sql":"SELECT from_state, to_state, confidence FROM dependencies"}' \
  http://localhost:8080/api/query
```

Ingestion is keyed by state name: re-uploading a name replaces that state, then
all edges are rebuilt from the full state set in one transaction.

## Schema

- `states(id, name UNIQUE, filename, version, serial, lineage, raw, ingested_at)`
- `resources(id, state_id, address, module, mode, type, name, provider, attributes)`
- `edges(id, batch, from_state, from_addr, from_attr, to_state, to_addr, kind, matched, confidence)`
- view `dependencies` — state-level rollup at the default 0.5 threshold

`raw` is the **sanitized** state: secret-looking keys (password, token,
access_key, …) and sensitive outputs are stripped before anything is stored.
SQL access is read-only by construction (a separate `mode=ro` connection plus
a single-statement guard).

## How resolution works

1. Index identifiers exported by every managed resource: ARN, hostnames
   (`dns_name`, `fqdn`, derived API Gateway host, Azure `default_hostname`, …),
   ids and names.
2. Scan every string in other states (resource attributes, data sources,
   outputs, JSON-in-strings such as IAM policies) for those identifiers.
3. Score each hit: ARN 0.95, host 0.9, id 0.7, name 0.3 (exact), lower for
   substring hits; divided by the number of states owning the same value.
   `terraform_remote_state` keys matching a state name score 0.9.
4. Roll up to state-level dependencies (max confidence, evidence count, kinds).

## Development

```sh
go test ./...                 # unit + API + integration (real TCP + binary E2E)
go test -short ./...          # skip the build-the-binary E2E test
go test -race ./...
goreleaser release --snapshot --clean   # verify release config locally
```

Layout: `main.go` (CLI/server), `state/` (parser + sanitizer), `resolver/`
(inference), `db/` (SQLite), `api/` (HTTP + embedded `web/` UI), `s3source/`
(S3 browser), `samples/` (demo states), `integration_test.go`.

## Releases

[GoReleaser](https://goreleaser.com/) builds Linux, macOS and Windows archives
and a Homebrew cask. Pushing a `v*` tag triggers a release; the cask is
published to `krishnaduttPanchagnula/homebrew-tap` and a copy is kept in
`Casks/terraquery.rb` in this repo.

## Icon attribution

AWS service icons come from the
[AWS Labs architecture icon set](https://github.com/awslabs/aws-icons-for-plantuml)
(CC BY-ND 2.0); the license is at `api/web/icons/aws/LICENSE`. GCP/Azure nodes
use provider brand marks. All icons are embedded in the binary. These assets
are not covered by the repository's MIT license.

## License

MIT — see [LICENSE](LICENSE). Third-party icon licenses apply as noted above.

## Known gaps / next steps

- Remote-state matching uses the state *name*, not the real backend bucket/key.
- No SSM/Secrets Manager parameter-name edges, no live cloud reconciliation.
- Confidence numbers still need calibrating against real repositories.
- The substring scan is O(strings × identifiers); swap in Aho-Corasick if the
  inputs get large.
