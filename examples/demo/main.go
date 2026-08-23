// Command demo shows the retention planner on synthetic snapshots, with no AWS
// calls, so you can see exactly what aws-rds-snapshot-prune would delete.
//
//	go run ./examples/demo
package main

import (
	"os"
	"time"

	"github.com/moveeeax/aws-rds-snapshot-prune/internal/prune"
)

func main() {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	mk := func(id, inst string, ageDays int, manual bool) prune.Snapshot {
		return prune.Snapshot{ID: id, InstanceID: inst, Manual: manual,
			CreateTime: now.AddDate(0, 0, -ageDays)}
	}
	snaps := []prune.Snapshot{
		mk("payments-2026-08-22", "payments", 1, true),
		mk("payments-2026-05-01", "payments", 114, true),
		mk("payments-2025-11-01", "payments", 295, true),
		mk("analytics-2026-01-10", "analytics", 225, true),
		mk("rds:analytics-auto", "analytics", 3, false), // automated, never touched
	}
	res := prune.Result{Decisions: prune.Plan(snaps,
		prune.Policy{KeepLast: 1, OlderThan: 90 * 24 * time.Hour}, now)}
	_ = res.Write(os.Stdout, false)
}
