package prune

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// fakeRDS is an in-memory RDSAPI. It paginates in fixed-size pages and records
// every DeleteDBSnapshot call so tests can assert dry-run never deletes.
type fakeRDS struct {
	snaps    []rdstypes.DBSnapshot
	pageSize int
	deleted  []string
}

func (f *fakeRDS) DescribeDBSnapshots(_ context.Context, in *rds.DescribeDBSnapshotsInput, _ ...func(*rds.Options)) (*rds.DescribeDBSnapshotsOutput, error) {
	start := 0
	if in.Marker != nil {
		// marker is the start index encoded as a string
		for i, s := range f.snaps {
			if aws.ToString(s.DBSnapshotIdentifier) == *in.Marker {
				start = i
				break
			}
		}
	}
	size := f.pageSize
	if size <= 0 {
		size = len(f.snaps)
	}
	end := start + size
	var marker *string
	if end < len(f.snaps) {
		marker = f.snaps[end].DBSnapshotIdentifier
	} else {
		end = len(f.snaps)
	}
	return &rds.DescribeDBSnapshotsOutput{DBSnapshots: f.snaps[start:end], Marker: marker}, nil
}

func (f *fakeRDS) DeleteDBSnapshot(_ context.Context, in *rds.DeleteDBSnapshotInput, _ ...func(*rds.Options)) (*rds.DeleteDBSnapshotOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(in.DBSnapshotIdentifier))
	return &rds.DeleteDBSnapshotOutput{}, nil
}

func sdkSnap(id, inst, typ string, ageDays int) rdstypes.DBSnapshot {
	t := base.AddDate(0, 0, -ageDays)
	return rdstypes.DBSnapshot{
		DBSnapshotIdentifier: aws.String(id),
		DBInstanceIdentifier: aws.String(inst),
		SnapshotType:         aws.String(typ),
		SnapshotCreateTime:   &t,
		DBSnapshotArn:        aws.String("arn:aws:rds:eu-west-1:111122223333:snapshot:" + id),
	}
}

func fixture() *fakeRDS {
	return &fakeRDS{pageSize: 2, snaps: []rdstypes.DBSnapshot{
		sdkSnap("keep-new", "db1", "manual", 1),
		sdkSnap("old-1", "db1", "manual", 120),
		sdkSnap("old-2", "db1", "manual", 200),
		sdkSnap("auto", "db1", "automated", 300),
	}}
}

func TestRunDryRunNeverDeletes(t *testing.T) {
	f := fixture()
	res, err := Run(context.Background(), f, Options{
		Policy: Policy{KeepLast: 1, OlderThan: 90 * 24 * time.Hour}, Now: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("dry-run must not call Delete, got %v", f.deleted)
	}
	if res.PendingDeletions() != 2 {
		t.Fatalf("expected 2 pending, got %d", res.PendingDeletions())
	}
}

func TestRunApplyDeletes(t *testing.T) {
	f := fixture()
	res, err := Run(context.Background(), f, Options{
		Policy: Policy{KeepLast: 1, OlderThan: 90 * 24 * time.Hour}, Apply: true, Now: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 2 {
		t.Fatalf("apply should delete 2, got %v", f.deleted)
	}
	want := map[string]bool{"old-1": true, "old-2": true}
	for _, id := range f.deleted {
		if !want[id] {
			t.Fatalf("unexpected deletion of %s", id)
		}
	}
	if len(res.Deleted) != 2 {
		t.Fatalf("result should record 2 deletions, got %d", len(res.Deleted))
	}
}

func TestRunPaginates(t *testing.T) {
	f := fixture() // 4 snapshots, page size 2
	res, err := Run(context.Background(), f, Options{Policy: Policy{KeepLast: 100}, Now: base})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Decisions) != 4 {
		t.Fatalf("expected all 4 snapshots across pages, got %d", len(res.Decisions))
	}
}

func TestJSONOutputStable(t *testing.T) {
	f := fixture()
	res, _ := Run(context.Background(), f, Options{
		Policy: Policy{KeepLast: 1, OlderThan: 90 * 24 * time.Hour}, Now: base,
	})
	var buf bytes.Buffer
	if err := res.Write(&buf, true); err != nil {
		t.Fatal(err)
	}
	var round Result
	if err := json.Unmarshal(buf.Bytes(), &round); err != nil {
		t.Fatalf("JSON output is not valid: %v", err)
	}
	if round.PendingDeletions() != 2 {
		t.Fatalf("round-tripped JSON lost decisions: %d", round.PendingDeletions())
	}
}
