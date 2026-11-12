package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/moveeeax/aws-rds-snapshot-prune/internal/prune"
	"github.com/spf13/cobra"
)

var version = "v0.2.0"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

func newRootCmd() *cobra.Command {
	var (
		keepLast  int
		olderThan string
		region    string
		allow     string
		deny      string
		keepTags  []string
		orphans   string
		apply     bool
		asJSON    bool
	)

	cmd := &cobra.Command{
		Use:     "aws-rds-snapshot-prune",
		Short:   "Prune old manual RDS snapshots by age and retained count",
		Version: version,
		Long: "Prune old Amazon RDS manual snapshots safely: keep the N most-recent\n" +
			"per instance and anything newer than a cutoff, delete the rest.\n" +
			"Dry-run by default; automated and shared snapshots are never touched.\n" +
			"Snapshots of deleted instances are kept unless --orphans prune.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dur, err := parseDuration(olderThan)
			if err != nil {
				return fmt.Errorf("--older-than: %w", err)
			}
			policy := prune.Policy{KeepLast: keepLast, OlderThan: dur}
			if allow != "" {
				if policy.Allow, err = regexp.Compile(allow); err != nil {
					return fmt.Errorf("--allow: %w", err)
				}
			}
			if deny != "" {
				if policy.Deny, err = regexp.Compile(deny); err != nil {
					return fmt.Errorf("--deny: %w", err)
				}
			}

			for _, kt := range keepTags {
				m, err := prune.ParseTagMatch(kt)
				if err != nil {
					return fmt.Errorf("--keep-tag: %w", err)
				}
				policy.KeepTags = append(policy.KeepTags, m)
			}
			switch orphans {
			case "keep":
			case "prune":
				policy.PruneOrphans = true
			default:
				return fmt.Errorf("--orphans: must be keep or prune, got %q", orphans)
			}

			ctx := context.Background()
			opts := []func(*awsconfig.LoadOptions) error{}
			if region != "" {
				opts = append(opts, awsconfig.WithRegion(region))
			}
			cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
			if err != nil {
				return fmt.Errorf("load AWS config: %w", err)
			}

			res, err := prune.Run(ctx, rds.NewFromConfig(cfg), prune.Options{
				Policy: policy, Apply: apply, JSON: asJSON,
			})
			if err != nil {
				return err
			}
			if err := res.Write(cmd.OutOrStdout(), asJSON); err != nil {
				return err
			}
			// CI gate: a dry-run with pending deletions exits non-zero so a
			// pipeline notices drift. Apply-mode failures also exit non-zero.
			if len(res.Errors) > 0 {
				os.Exit(1)
			}
			if !apply && res.PendingDeletions() > 0 {
				os.Exit(1)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.IntVar(&keepLast, "keep-last", 5, "keep the N most-recent snapshots per instance")
	f.StringVar(&olderThan, "older-than", "90d", "keep snapshots younger than this (e.g. 90d, 24h)")
	f.StringVar(&region, "region", "", "AWS region (default: from environment/profile)")
	f.StringVar(&allow, "allow", "", "only prune instances whose id matches this regex")
	f.StringVar(&deny, "deny", "", "never prune instances whose id matches this regex")
	f.StringArrayVar(&keepTags, "keep-tag", nil, "never delete snapshots with this tag: key or key=value (repeatable)")
	f.StringVar(&orphans, "orphans", "keep", "snapshots whose source instance was deleted: keep or prune (prune ignores --keep-last)")
	f.BoolVar(&apply, "apply", false, "actually delete (default: dry-run)")
	f.BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

// parseDuration extends time.ParseDuration with a trailing "d" (days) unit,
// since retention windows are naturally expressed in days.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid day value %q", s)
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}
