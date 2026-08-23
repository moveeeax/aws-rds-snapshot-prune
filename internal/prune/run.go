package prune

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

// Options bundles everything a single Run needs.
type Options struct {
	Policy Policy
	// Apply actually deletes; when false the run is a dry-run and never calls Delete.
	Apply bool
	// JSON switches output from a table to machine-readable JSON.
	JSON bool
	// Now is injectable for deterministic tests; zero means time.Now().
	Now time.Time
}

// Result is the outcome of a run.
type Result struct {
	Decisions []Decision `json:"decisions"`
	Deleted   []string   `json:"deleted"`
	Applied   bool       `json:"applied"`
	// Errors holds per-snapshot delete failures (apply mode only).
	Errors map[string]string `json:"errors,omitempty"`
}

// PendingDeletions is the number of snapshots the plan wants to remove.
func (r Result) PendingDeletions() int {
	n := 0
	for _, d := range r.Decisions {
		if d.Delete {
			n++
		}
	}
	return n
}

// Run lists snapshots, plans deletions, and (when Apply is set) deletes them.
func Run(ctx context.Context, api RDSAPI, opt Options) (Result, error) {
	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}
	snaps, err := List(ctx, api)
	if err != nil {
		return Result{}, fmt.Errorf("list snapshots: %w", err)
	}
	decisions := Plan(snaps, opt.Policy, now)
	res := Result{Decisions: decisions, Applied: opt.Apply}

	if opt.Apply {
		for _, s := range ToDelete(decisions) {
			if err := Delete(ctx, api, s.ID); err != nil {
				if res.Errors == nil {
					res.Errors = map[string]string{}
				}
				res.Errors[s.ID] = err.Error()
				continue
			}
			res.Deleted = append(res.Deleted, s.ID)
		}
	}
	return res, nil
}

// Write renders the result to w as a table or JSON.
func (r Result) Write(w io.Writer, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ACTION\tINSTANCE\tSNAPSHOT\tCREATED\tREASON")
	for _, d := range r.Decisions {
		action := "keep"
		if d.Delete {
			action = "delete"
			if r.Applied {
				action = "deleted"
				if _, bad := r.Errors[d.Snapshot.ID]; bad {
					action = "FAILED"
				}
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			action, d.Snapshot.InstanceID, d.Snapshot.ID,
			d.Snapshot.CreateTime.UTC().Format("2006-01-02"), d.Reason)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%d snapshot(s) pending deletion", r.PendingDeletions())
	if r.Applied {
		fmt.Fprintf(w, ", %d deleted", len(r.Deleted))
	}
	fmt.Fprintln(w)
	return nil
}
