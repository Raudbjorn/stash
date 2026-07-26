package manager

import (
	"context"

	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/models"
)

// RunAutoTagFilesForBench runs the file-based auto-tag flow against the
// given repository and paths. Exported so the cmd/autotag-bench tool can
// measure the same entry point across revisions without depending on the
// unexported autoTagJob type. Keeping this function signature stable means
// the bench binary built on one commit can be run against the autotag
// implementation on another commit without source changes.
//
// Do not use in production code — Execute on the real job manager path
// handles cancellation, progress telemetry, and persistence concerns that
// this bench wrapper intentionally leaves to the caller.
func RunAutoTagFilesForBench(
	ctx context.Context,
	repo models.Repository,
	progress *job.Progress,
	paths []string,
	performers, studios, tags bool,
) {
	j := &autoTagJob{repository: repo}
	j.autoTagFiles(ctx, progress, paths, performers, studios, tags)
}
