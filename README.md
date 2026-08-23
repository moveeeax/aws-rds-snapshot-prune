# aws-rds-snapshot-prune

[![ci](https://github.com/moveeeax/aws-rds-snapshot-prune/actions/workflows/ci.yml/badge.svg)](https://github.com/moveeeax/aws-rds-snapshot-prune/actions/workflows/ci.yml)
![go](https://img.shields.io/badge/go-1.22-00ADD8?logo=go&logoColor=white)
[![license](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Prune old Amazon RDS **manual** snapshots by age and retained count — safely,
with a dry-run first and a CI gate. Keep the *N* most-recent snapshots per
instance and anything younger than a cutoff; delete the rest. Automated and
shared snapshots are never touched.

## Why

Manual RDS/Aurora snapshots pile up for years. Nobody deletes them and they
quietly bill you every month. This tool applies one clear retention rule
across your snapshots, shows exactly what it *would* delete, and only removes
anything when you pass `--apply`.

## How it works

A snapshot is a delete candidate only when **all** of these hold:

- it is a **manual** snapshot (automated system snapshots are skipped),
- it is **not shared** from another account,
- it is **not** within the `--keep-last` newest of its instance, **and**
- it is **older than** `--older-than`,
- and its instance is not excluded by `--allow` / `--deny`.

`--keep-last` is evaluated **per DB instance**, so a busy instance and a quiet
one each keep their own recent history. The retention decision is pure and
deterministic — the same input always yields the same plan — which is what
makes it safe to gate CI on.

## Install

```shell
go install github.com/moveeeax/aws-rds-snapshot-prune/cmd/aws-rds-snapshot-prune@latest
```

Or build from source: `go build ./cmd/aws-rds-snapshot-prune`.

## Usage

Dry-run (default) — prints a plan and exits non-zero if anything is pending,
so it doubles as a CI drift check:

```shell
aws-rds-snapshot-prune --keep-last 5 --older-than 90d --region eu-west-1
```

Actually delete:

```shell
aws-rds-snapshot-prune --keep-last 5 --older-than 90d --apply
```

JSON for pipelines:

```shell
aws-rds-snapshot-prune --keep-last 5 --older-than 90d --json
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--keep-last` | `5` | Keep the N most-recent snapshots per instance |
| `--older-than` | `90d` | Keep snapshots younger than this (`90d`, `24h`, …) |
| `--region` | env/profile | AWS region |
| `--allow` | — | Only prune instances whose id matches this regex |
| `--deny` | — | Never prune instances whose id matches this regex |
| `--apply` | `false` | Actually delete (default is dry-run) |
| `--json` | `false` | Emit JSON instead of a table |

### Example (no AWS needed)

`go run ./examples/demo` runs the planner on synthetic snapshots:

```text
ACTION  INSTANCE   SNAPSHOT              CREATED     REASON
delete  payments   payments-2025-11-01   2025-11-01  delete: exceeds retention
delete  payments   payments-2026-05-01   2026-05-01  delete: exceeds retention
keep    analytics  analytics-2026-01-10  2026-01-10  kept: within keep-last
keep    analytics  rds:analytics-auto    2026-08-20  skipped: automated snapshot
keep    payments   payments-2026-08-22   2026-08-22  kept: within keep-last

2 snapshot(s) pending deletion
```

## Credentials

Standard AWS SDK resolution: environment variables, shared config/credentials,
or an instance/role profile. The IAM principal needs
`rds:DescribeDBSnapshots` and, for `--apply`, `rds:DeleteDBSnapshot`.

## Exit codes

- `0` — nothing to delete, or `--apply` succeeded.
- `1` — dry-run found snapshots pending deletion (CI gate), or a delete failed.
- `2` — bad flags or an AWS error.

## License

MIT — see [LICENSE](LICENSE).
