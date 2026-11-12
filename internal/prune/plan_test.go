package prune

import (
	"regexp"
	"testing"
	"time"
)

var base = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

func day(n int) time.Time { return base.AddDate(0, 0, -n) }

func manual(id, inst string, ageDays int) Snapshot {
	return Snapshot{ID: id, InstanceID: inst, CreateTime: day(ageDays), Manual: true}
}

func ids(snaps []Snapshot) map[string]bool {
	m := map[string]bool{}
	for _, s := range snaps {
		m[s.ID] = true
	}
	return m
}

func TestPlanKeepLastAndAge(t *testing.T) {
	snaps := []Snapshot{
		manual("a1", "db1", 1),   // newest, kept by keep-last
		manual("a2", "db1", 10),  // kept by keep-last (N=2)
		manual("a3", "db1", 100), // rank 2, older than 90d -> delete
		manual("a4", "db1", 200), // rank 3, older than 90d -> delete
		manual("a5", "db1", 60),  // rank ... younger than 90d -> kept by age
	}
	got := ToDelete(Plan(snaps, Policy{KeepLast: 2, OlderThan: 90 * 24 * time.Hour}, base))
	del := ids(got)
	if !del["a3"] || !del["a4"] {
		t.Fatalf("expected a3,a4 deleted, got %v", del)
	}
	if del["a1"] || del["a2"] || del["a5"] {
		t.Fatalf("kept snapshots were selected for deletion: %v", del)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 deletions, got %d", len(got))
	}
}

func TestPlanPerInstanceGrouping(t *testing.T) {
	// keep-last is evaluated per instance: each instance keeps its own newest.
	snaps := []Snapshot{
		manual("a1", "db1", 100),
		manual("a2", "db1", 200),
		manual("b1", "db2", 150),
	}
	got := ids(ToDelete(Plan(snaps, Policy{KeepLast: 1, OlderThan: 0}, base)))
	if !got["a2"] {
		t.Fatalf("expected db1's older snapshot a2 deleted, got %v", got)
	}
	if got["a1"] || got["b1"] {
		t.Fatalf("each instance should keep its newest; got %v", got)
	}
}

func TestPlanNeverTouchesAutomatedOrShared(t *testing.T) {
	snaps := []Snapshot{
		{ID: "auto1", InstanceID: "db1", CreateTime: day(300), Manual: false},
		{ID: "shared1", InstanceID: "db1", CreateTime: day(300), Manual: true, Shared: true},
		manual("m1", "db1", 300),
	}
	got := ids(ToDelete(Plan(snaps, Policy{KeepLast: 0, OlderThan: 90 * 24 * time.Hour}, base)))
	if got["auto1"] {
		t.Fatal("automated snapshot must never be deleted")
	}
	if got["shared1"] {
		t.Fatal("shared snapshot must never be deleted")
	}
	if !got["m1"] {
		t.Fatalf("manual old snapshot should be deleted, got %v", got)
	}
}

func TestPlanAllowDeny(t *testing.T) {
	snaps := []Snapshot{
		manual("p1", "prod-db", 300),
		manual("s1", "staging-db", 300),
	}
	// deny protects prod
	got := ids(ToDelete(Plan(snaps, Policy{OlderThan: 90 * 24 * time.Hour, Deny: regexp.MustCompile("^prod-")}, base)))
	if got["p1"] {
		t.Fatal("denied instance must be protected")
	}
	if !got["s1"] {
		t.Fatal("staging should be deletable")
	}
	// allow restricts to prod only
	got = ids(ToDelete(Plan(snaps, Policy{OlderThan: 90 * 24 * time.Hour, Allow: regexp.MustCompile("^prod-")}, base)))
	if !got["p1"] || got["s1"] {
		t.Fatalf("allow set should limit to prod, got %v", got)
	}
}

func TestPlanDeterministicOrder(t *testing.T) {
	snaps := []Snapshot{
		manual("z", "db1", 300),
		manual("a", "db1", 300),
		manual("m", "db1", 250),
	}
	p := Policy{OlderThan: 90 * 24 * time.Hour}
	first := Plan(snaps, p, base)
	for i := 0; i < 20; i++ {
		got := Plan(snaps, p, base)
		for j := range got {
			if got[j].Snapshot.ID != first[j].Snapshot.ID {
				t.Fatalf("non-deterministic order at %d: %s vs %s", j, got[j].Snapshot.ID, first[j].Snapshot.ID)
			}
		}
	}
	// deletions come first, oldest-created first, ties by id
	if first[0].Snapshot.ID != "a" || first[1].Snapshot.ID != "z" {
		t.Fatalf("expected a,z ordering for oldest deletions, got %s,%s", first[0].Snapshot.ID, first[1].Snapshot.ID)
	}
}

func TestPlanKeepTag(t *testing.T) {
	tagged := func(id string, tags map[string]string) Snapshot {
		s := manual(id, "db1", 300)
		s.Tags = tags
		return s
	}
	snaps := []Snapshot{
		tagged("any", map[string]string{"keep": "whatever"}),
		tagged("exact", map[string]string{"purpose": "pre-migration"}),
		tagged("other-value", map[string]string{"purpose": "scratch"}),
		tagged("untagged", nil),
		manual("newest", "db1", 1),
	}
	p := Policy{
		KeepLast:  1,
		OlderThan: 90 * 24 * time.Hour,
		KeepTags: []TagMatch{
			{Key: "keep", AnyValue: true},
			{Key: "purpose", Value: "pre-migration"},
		},
	}
	decisions := Plan(snaps, p, base)
	got := ids(ToDelete(decisions))
	if got["any"] || got["exact"] {
		t.Fatalf("tagged snapshots must be protected, got %v", got)
	}
	if !got["other-value"] || !got["untagged"] {
		t.Fatalf("non-matching snapshots should be deleted, got %v", got)
	}
	for _, d := range decisions {
		if (d.Snapshot.ID == "any" || d.Snapshot.ID == "exact") && d.Reason != "kept: protected tag" {
			t.Fatalf("%s: unexpected reason %q", d.Snapshot.ID, d.Reason)
		}
	}
}

func TestPlanOrphans(t *testing.T) {
	orphan := func(id string, age int) Snapshot {
		s := manual(id, "gone", age)
		s.SourceDeleted = true
		return s
	}
	snaps := []Snapshot{orphan("final", 300), orphan("recent", 10)}
	p := Policy{KeepLast: 5, OlderThan: 90 * 24 * time.Hour}

	// default: orphans are kept no matter their age
	for _, d := range Plan(snaps, p, base) {
		if d.Delete || d.Reason != "kept: source deleted" {
			t.Fatalf("%s: default must keep orphans, got delete=%v %q", d.Snapshot.ID, d.Delete, d.Reason)
		}
	}

	// prune: keep-last 5 does not protect them, the age rule still does
	p.PruneOrphans = true
	reasons := map[string]string{}
	for _, d := range Plan(snaps, p, base) {
		reasons[d.Snapshot.ID] = d.Reason
	}
	if reasons["final"] != "delete: source deleted" {
		t.Fatalf("old orphan should be deleted, got %q", reasons["final"])
	}
	if reasons["recent"] != "kept: source deleted, newer than age threshold" {
		t.Fatalf("young orphan should be kept by age, got %q", reasons["recent"])
	}

	// a protected tag still wins over orphan pruning
	tagged := orphan("tagged", 300)
	tagged.Tags = map[string]string{"keep": ""}
	p.KeepTags = []TagMatch{{Key: "keep", AnyValue: true}}
	if got := ToDelete(Plan([]Snapshot{tagged}, p, base)); len(got) != 0 {
		t.Fatalf("tagged orphan must be kept, got %v", got)
	}
}

func TestParseTagMatch(t *testing.T) {
	cases := []struct {
		in   string
		want TagMatch
	}{
		{"keep", TagMatch{Key: "keep", AnyValue: true}},
		{"env=prod", TagMatch{Key: "env", Value: "prod"}},
		{"note=a=b", TagMatch{Key: "note", Value: "a=b"}},
		{"empty=", TagMatch{Key: "empty", Value: ""}},
	}
	for _, c := range cases {
		got, err := ParseTagMatch(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseTagMatch(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	if _, err := ParseTagMatch("=value"); err == nil {
		t.Error("empty key must be rejected")
	}
}
