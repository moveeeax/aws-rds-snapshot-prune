package prune

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
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
	// ResourceID is the immutable DbiResourceId of the source instance. It
	// survives instance renames, so it is preferred over InstanceID when
	// checking whether the source still exists.
	ResourceID string `json:"resource_id,omitempty"`
	// Tags holds the snapshot's tags as key/value pairs.
	Tags map[string]string `json:"tags,omitempty"`
	// SourceDeleted is true when no DB instance in the region matches the
	// snapshot's source (an "orphan", typically a final snapshot).
	SourceDeleted bool `json:"source_deleted"`
}

// TagMatch protects snapshots carrying a tag. An empty Value with AnyValue set
// matches the key regardless of its value; otherwise the value must be equal.
type TagMatch struct {
	Key      string
	Value    string
	AnyValue bool
}

// Matches reports whether the tag set carries this tag.
func (m TagMatch) Matches(tags map[string]string) bool {
	v, ok := tags[m.Key]
	return ok && (m.AnyValue || v == m.Value)
}

// ParseTagMatch parses "key" (any value) or "key=value" (exact value). The
// value may contain further "=" characters; only the first one splits.
func ParseTagMatch(s string) (TagMatch, error) {
	key, value, hasValue := strings.Cut(s, "=")
	if key == "" {
		return TagMatch{}, fmt.Errorf("invalid tag %q: empty key", s)
	}
	return TagMatch{Key: key, Value: value, AnyValue: !hasValue}, nil
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
	// KeepTags protects any snapshot carrying one of these tags.
	KeepTags []TagMatch
	// PruneOrphans lets snapshots whose source instance no longer exists be
	// deleted once they are older than OlderThan. KeepLast does not apply to
	// them. When false (the default) orphans are always kept.
	PruneOrphans bool
}

// Plan applies the policy to the snapshots and returns a deterministic,
// stably-sorted list of decisions (soonest-created deletions first, then kept).
//
// A snapshot is a delete candidate only when it is a manual, non-shared
// snapshot that is neither within the KeepLast newest of its instance nor
// younger than OlderThan, and whose instance is not protected by Allow/Deny.
// Snapshots carrying a KeepTags tag are never deleted. Orphans (source
// instance deleted) are kept unless PruneOrphans is set, in which case only the
// age rule applies to them.
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
		case protectedByTag(s, p.KeepTags):
			decisions = append(decisions, Decision{s, false, "kept: protected tag"})
		case s.SourceDeleted:
			decisions = append(decisions, decideOrphan(s, p, now))
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

func protectedByTag(s Snapshot, keep []TagMatch) bool {
	for _, m := range keep {
		if m.Matches(s.Tags) {
			return true
		}
	}
	return false
}

// decideOrphan handles a snapshot whose source instance is gone. KeepLast is
// not applied: there is no live instance whose recent history needs keeping.
func decideOrphan(s Snapshot, p Policy, now time.Time) Decision {
	switch {
	case !p.PruneOrphans:
		return Decision{s, false, "kept: source deleted"}
	case p.OlderThan > 0 && s.CreateTime.After(now.Add(-p.OlderThan)):
		return Decision{s, false, "kept: source deleted, newer than age threshold"}
	default:
		return Decision{s, true, "delete: source deleted"}
	}
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
