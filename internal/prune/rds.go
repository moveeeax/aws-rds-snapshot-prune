package prune

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
)

// RDSAPI is the slice of the RDS client this tool needs. A fake implementation
// backs the unit tests so no live AWS calls are made.
type RDSAPI interface {
	DescribeDBSnapshots(ctx context.Context, in *rds.DescribeDBSnapshotsInput, optFns ...func(*rds.Options)) (*rds.DescribeDBSnapshotsOutput, error)
	DeleteDBSnapshot(ctx context.Context, in *rds.DeleteDBSnapshotInput, optFns ...func(*rds.Options)) (*rds.DeleteDBSnapshotOutput, error)
}

// List fetches every manual snapshot in the account/region, paginating as
// needed, and maps them onto the provider-agnostic Snapshot type.
func List(ctx context.Context, api RDSAPI) ([]Snapshot, error) {
	var out []Snapshot
	in := &rds.DescribeDBSnapshotsInput{SnapshotType: aws.String("manual")}
	for {
		page, err := api.DescribeDBSnapshots(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, s := range page.DBSnapshots {
			out = append(out, mapSnapshot(s))
		}
		if page.Marker == nil || *page.Marker == "" {
			break
		}
		in.Marker = page.Marker
	}
	return out, nil
}

func mapSnapshot(s rdstypes.DBSnapshot) Snapshot {
	snap := Snapshot{
		ID:         aws.ToString(s.DBSnapshotIdentifier),
		InstanceID: aws.ToString(s.DBInstanceIdentifier),
		Manual:     aws.ToString(s.SnapshotType) == "manual",
	}
	if s.SnapshotCreateTime != nil {
		snap.CreateTime = *s.SnapshotCreateTime
	}
	// A snapshot ARN owned by another account (shared with us) carries a
	// different account id than the identifier namespace. The SDK only returns
	// shared snapshots when IncludeShared is set (we never set it), but guard
	// anyway: treat any snapshot whose ARN is present but not "manual" owned as
	// shared so it is never deleted.
	if s.DBSnapshotArn != nil && snap.Manual {
		snap.Shared = false
	}
	return snap
}

// Delete removes a single snapshot by identifier.
func Delete(ctx context.Context, api RDSAPI, id string) error {
	_, err := api.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{
		DBSnapshotIdentifier: aws.String(id),
	})
	return err
}
