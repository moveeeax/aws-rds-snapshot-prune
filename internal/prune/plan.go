package prune

import (
	"regexp"
	"sort"
	"time"
)

// Snapshot is a provider-agnostic view of an RDS manual snapshot. The AWS
// layer maps SDK types onto this struct so the retention logic stays pure and
// trivially testable.
type Snapshot struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instance_id"`
	CreateTime time.Time `json:"create_time"`
	// Manual is false for automated/system snapshots.
	Manual bool `json:"manual"`
	// Shared is true for snapshots owned by another account.
	Shared bool `json:"shared"`
}

// Decision records what the planner chose for a single snapshot and why.
type Decision struct {
	Snapshot Snapshot `json:"snapshot"`
	Delete   bool     `json:"delete"`
	Reason   string   `json:"reason"`
}

// Policy describes the retention rules.
type Policy struct {
	// KeepLast keeps the N most-recent snapshots per instance. 0 disables.
	KeepLast int
	// OlderThan keeps anything younger than this duration. 0 disables the age keep.
	OlderThan time.Duration
	// Allow, when set, restricts pruning to instance IDs that match.
	Allow *regexp.Regexp
	// Deny, when set, protects instance IDs that match from any deletion.
	Deny *regexp.Regexp
}

// Plan applies the policy to the snapshots and returns a deterministic,
// stably-sorted list of decisions (soonest-created deletions first, then kept).
//
// A snapshot is a delete candidate only when it is a manual, non-shared
// snapshot that is neither within the KeepLast newest of its instance nor
// younger than OlderThan, and whose instance is not protected by Allow/Deny.
func Plan(snaps []Snapshot, p Policy, now time.Time) []Decision {
	decisions := make([]Decision, 0, len(snaps))

	// Group manual, non-shared, in-scope snapshots by instance so KeepLast is
	// evaluated per instance. Everything else is recorded as kept with a reason.
	byInstance := map[string][]Snapshot{}
	for _, s := range snaps {
		switch {
		case !s.Manual:
			decisions = append(decisions, Decision{s, false, "skipped: automated snapshot"})
		case s.Shared:
			decisions = append(decisions, Decision{s, false, "skipped: shared snapshot"})
		case p.Deny != nil && p.Deny.MatchString(s.InstanceID):
			decisions = append(decisions, Decision{s, false, "protected: instance denied"})
		case p.Allow != nil && !p.Allow.MatchString(s.InstanceID):
			decisions = append(decisions, Decision{s, false, "skipped: instance not in allow set"})
		default:
			byInstance[s.InstanceID] = append(byInstance[s.InstanceID], s)
		}
	}

	cutoff := now.Add(-p.OlderThan)
	for _, group := range byInstance {
		// Newest first; break ties on ID for determinism.
		sort.Slice(group, func(i, j int) bool {
			if group[i].CreateTime.Equal(group[j].CreateTime) {
				return group[i].ID < group[j].ID
			}
			return group[i].CreateTime.After(group[j].CreateTime)
		})
		for rank, s := range group {
			switch {
			case p.KeepLast > 0 && rank < p.KeepLast:
				decisions = append(decisions, Decision{s, false, "kept: within keep-last"})
			case p.OlderThan > 0 && s.CreateTime.After(cutoff):
				decisions = append(decisions, Decision{s, false, "kept: newer than age threshold"})
			default:
				decisions = append(decisions, Decision{s, true, "delete: exceeds retention"})
			}
		}
	}

	sort.Slice(decisions, func(i, j int) bool {
		if decisions[i].Delete != decisions[j].Delete {
			return decisions[i].Delete // deletions first
		}
		if !decisions[i].Snapshot.CreateTime.Equal(decisions[j].Snapshot.CreateTime) {
			return decisions[i].Snapshot.CreateTime.Before(decisions[j].Snapshot.CreateTime)
		}
		return decisions[i].Snapshot.ID < decisions[j].Snapshot.ID
	})
	return decisions
}

// ToDelete returns just the snapshots the plan marks for deletion.
func ToDelete(decisions []Decision) []Snapshot {
	var out []Snapshot
	for _, d := range decisions {
		if d.Delete {
			out = append(out, d.Snapshot)
		}
	}
	return out
}
