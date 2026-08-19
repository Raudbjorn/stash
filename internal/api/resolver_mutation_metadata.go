package api

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/stashapp/stash/internal/identify"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/internal/manager/task"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/session"
)

func (r *mutationResolver) MetadataScan(ctx context.Context, input manager.ScanMetadataInput) (string, error) {
	jobID, err := manager.GetInstance().Scan(ctx, input)

	if err != nil {
		return "", err
	}

	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) MetadataImport(ctx context.Context) (string, error) {
	jobID, err := manager.GetInstance().Import(ctx)
	if err != nil {
		return "", err
	}

	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) ImportObjects(ctx context.Context, input manager.ImportObjectsInput) (string, error) {
	t, err := manager.CreateImportTask(config.GetInstance().GetVideoFileNamingAlgorithm(), input)
	if err != nil {
		return "", err
	}

	jobID := manager.GetInstance().RunSingleTask(ctx, t)

	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) MetadataExport(ctx context.Context) (string, error) {
	jobID, err := manager.GetInstance().Export(ctx)
	if err != nil {
		return "", err
	}

	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) ExportObjects(ctx context.Context, input manager.ExportObjectsInput) (*string, error) {
	t := manager.CreateExportTask(config.GetInstance().GetVideoFileNamingAlgorithm(), input)

	var wg sync.WaitGroup
	wg.Add(1)
	t.Start(ctx, &wg)

	if t.DownloadHash != "" {
		baseURL, _ := ctx.Value(BaseURLCtxKey).(string)

		// generate timestamp
		suffix := time.Now().Format("20060102-150405")
		ret := baseURL + "/downloads/" + t.DownloadHash + "/export" + suffix + ".zip"
		return &ret, nil
	}

	return nil, nil
}

func (r *mutationResolver) MetadataGenerate(ctx context.Context, input manager.GenerateMetadataInput) (string, error) {
	jobID, err := manager.GetInstance().Generate(ctx, input)

	if err != nil {
		return "", err
	}

	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) MetadataAutoTag(ctx context.Context, input manager.AutoTagMetadataInput) (string, error) {
	jobID := manager.GetInstance().AutoTag(ctx, input)
	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) MetadataDetectSceneCuts(ctx context.Context, input manager.DetectSceneCutsInput) (string, error) {
	jobID := manager.GetInstance().DetectSceneCuts(ctx, input)
	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) MetadataAnalyzeScenes(ctx context.Context, input manager.AnalyzeSceneMetadataInput) (string, error) {
	jobID := manager.GetInstance().AnalyzeSceneMetadata(ctx, input)
	return strconv.Itoa(jobID), nil
}
func (r *mutationResolver) SceneMetadataModelInstall(ctx context.Context, modelKey string) (string, error) {
	jobID := manager.GetInstance().SceneMetadataModelInstall(ctx, modelKey)
	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) SceneMetadataModelUninstall(ctx context.Context, modelKey string) (bool, error) {
	if err := manager.GetInstance().SceneMetadataModelUninstall(modelKey); err != nil {
		return false, err
	}
	return true, nil
}

func (r *mutationResolver) SceneMetadataModelRepair(ctx context.Context, key string) (bool, error) {
	return manager.GetInstance().SceneMetadataModelRepair(key)
}

func (r *mutationResolver) SceneMetadataModelAssign(ctx context.Context, role manager.SceneMetadataModelRole, modelKey *string) (bool, error) {
	if err := manager.GetInstance().SceneMetadataModelAssign(role, modelKey); err != nil {
		return false, err
	}
	return true, nil
}

func (r *mutationResolver) SceneMetadataModelReload(ctx context.Context) (bool, error) {
	return manager.GetInstance().SceneMetadataModelReload(), nil
}

func (r *mutationResolver) AcceptSceneMetadataProposalAction(ctx context.Context, runID string, sceneID string, actionIDs []string) (bool, error) {
	id, err := strconv.Atoi(sceneID)
	if err != nil {
		return false, fmt.Errorf("invalid scene ID %q: %w", sceneID, err)
	}
	return manager.GetInstance().AcceptSceneMetadataProposalActions(ctx, runID, id, actionIDs)
}

func (r *mutationResolver) RejectSceneMetadataProposalAction(ctx context.Context, runID string, sceneID string, actionIDs []string) (bool, error) {
	id, err := strconv.Atoi(sceneID)
	if err != nil {
		return false, fmt.Errorf("invalid scene ID %q: %w", sceneID, err)
	}
	return manager.GetInstance().RejectSceneMetadataProposalActions(ctx, runID, id, actionIDs)
}
func (r *mutationResolver) SelectSceneMetadataRemoteCandidate(
	ctx context.Context,
	runID string,
	sceneID string,
	endpoint string,
	remoteID string,
) (bool, error) {
	id, err := strconv.Atoi(sceneID)
	if err != nil {
		return false, fmt.Errorf("invalid scene ID %q: %w", sceneID, err)
	}
	return manager.GetInstance().SelectSceneMetadataRemoteCandidate(
		ctx, runID, id, endpoint, remoteID,
	)
}

func (r *mutationResolver) ApplySceneMetadataPlan(ctx context.Context, runID string, sceneIDs []string) (bool, error) {
	return manager.GetInstance().ApplySceneMetadataPlans(ctx, runID, sceneIDs)
}

func (r *mutationResolver) PurgeSceneMetadataPlans(ctx context.Context, olderThanSeconds int) (int, error) {
	const minPurgeAgeSeconds = 3600
	if olderThanSeconds < minPurgeAgeSeconds {
		return 0, fmt.Errorf("olderThanSeconds must be at least %d", minPurgeAgeSeconds)
	}
	actor := "unauthenticated"
	if userID := session.GetCurrentUserID(ctx); userID != nil {
		actor = *userID
	}
	count, err := manager.GetInstance().PurgeSceneMetadataPlans(ctx, time.Duration(olderThanSeconds)*time.Second)
	if err != nil {
		logger.Warnf("[scene metadata] purge failed actor=%s older_than_seconds=%d: %v", actor, olderThanSeconds, err)
		return 0, err
	}
	logger.Infof("[scene metadata] purged %d plan(s) actor=%s older_than_seconds=%d", count, actor, olderThanSeconds)
	return count, nil
}

func (r *mutationResolver) MetadataIdentify(ctx context.Context, input identify.Options) (string, error) {
	t := manager.CreateIdentifyJob(input)
	jobID := manager.GetInstance().JobManager.Add(ctx, "Identifying...", t)

	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) MetadataClean(ctx context.Context, input manager.CleanMetadataInput) (string, error) {
	jobID := manager.GetInstance().Clean(ctx, input)
	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) MetadataCleanGenerated(ctx context.Context, input task.CleanGeneratedOptions) (string, error) {
	mgr := manager.GetInstance()
	t := &task.CleanGeneratedJob{
		Options:                  input,
		Paths:                    mgr.Paths,
		BlobsStorageType:         mgr.Config.GetBlobsStorage(),
		VideoFileNamingAlgorithm: mgr.Config.GetVideoFileNamingAlgorithm(),
		Repository:               mgr.Repository,
		BlobCleaner:              mgr.Repository.Blob,
	}
	jobID := mgr.JobManager.Add(ctx, "Cleaning generated files...", t)

	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) MigrateHashNaming(ctx context.Context) (string, error) {
	jobID := manager.GetInstance().MigrateHash(ctx)
	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) BackupDatabase(ctx context.Context, input BackupDatabaseInput) (*string, error) {
	// if download is true, then backup to temporary file and return a link
	download := input.Download != nil && *input.Download
	includeBlobs := input.IncludeBlobs != nil && *input.IncludeBlobs
	mgr := manager.GetInstance()

	backupPath, backupName, err := mgr.BackupDatabase(download, includeBlobs)
	if err != nil {
		logger.Errorf("Error backing up database: %v", err)
		return nil, err
	}

	if download {
		downloadHash, err := mgr.DownloadStore.RegisterFile(backupPath, "", false)
		if err != nil {
			return nil, fmt.Errorf("error registering file for download: %w", err)
		}
		logger.Debugf("Generated backup file %s with hash %s", backupPath, downloadHash)

		baseURL, _ := ctx.Value(BaseURLCtxKey).(string)

		ret := baseURL + "/downloads/" + downloadHash + "/" + backupName
		return &ret, nil
	} else {
		logger.Infof("Successfully backed up database to: %s", backupPath)
	}

	return nil, nil
}

func (r *mutationResolver) AnonymiseDatabase(ctx context.Context, input AnonymiseDatabaseInput) (*string, error) {
	// if download is true, then save to temporary file and return a link
	download := input.Download != nil && *input.Download
	mgr := manager.GetInstance()

	outPath, outName, err := mgr.AnonymiseDatabase(download)
	if err != nil {
		logger.Errorf("Error anonymising database: %v", err)
		return nil, err
	}

	if download {
		downloadHash, err := mgr.DownloadStore.RegisterFile(outPath, "", false)
		if err != nil {
			return nil, fmt.Errorf("error registering file for download: %w", err)
		}
		logger.Debugf("Generated anonymised file %s with hash %s", outPath, downloadHash)

		baseURL, _ := ctx.Value(BaseURLCtxKey).(string)

		ret := baseURL + "/downloads/" + downloadHash + "/" + outName
		return &ret, nil
	} else {
		logger.Infof("Successfully anonymised database to: %s", outPath)
	}

	return nil, nil
}

func (r *mutationResolver) OptimiseDatabase(ctx context.Context) (string, error) {
	jobID := manager.GetInstance().OptimiseDatabase(ctx)
	return strconv.Itoa(jobID), nil
}
