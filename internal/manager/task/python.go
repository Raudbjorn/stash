package task

import (
	"context"
	"errors"
	"fmt"

	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/python"
)

type PythonConfig interface {
	GetPythonPath() string
	GetPythonRuntimeID() string
	GetPythonIndexes() []python.Index
	UpdatePythonSelection(runtimeID, executable string) error
}

type InstallPythonManagerJob struct {
	Manager *python.Manager
}

func (j *InstallPythonManagerJob) Execute(ctx context.Context, progress *job.Progress) error {
	progress.Indefinite()
	err := j.Manager.InstallEngine(ctx, func(processed, total int64) {
		if total > 0 {
			progress.SetTotal(int(total))
			progress.SetProcessed(int(processed))
		}
	})
	if job.IsCancelled(ctx) {
		return nil
	}
	return err
}

type InstallPythonRuntimeJob struct {
	Manager   *python.Manager
	RuntimeID string
}

func (j *InstallPythonRuntimeJob) Execute(ctx context.Context, progress *job.Progress) error {
	progress.Indefinite()
	end := progress.BeginTask("Installing Python runtime")
	defer end()
	_, err := j.Manager.InstallRuntime(ctx, j.RuntimeID)
	if job.IsCancelled(ctx) {
		return nil
	}
	return err
}

type SelectPythonRuntimeJob struct {
	Manager   *python.Manager
	Config    PythonConfig
	RuntimeID string
}

func (j *SelectPythonRuntimeJob) Execute(ctx context.Context, progress *job.Progress) error {
	progress.Indefinite()
	release, err := python.AcquireMutation(ctx)
	if err != nil {
		return err
	}
	defer release()

	end := progress.BeginTask("Resolving Python runtime")
	runtimes, err := j.Manager.ListRuntimes(ctx, true, j.Config.GetPythonRuntimeID(), j.Config.GetPythonPath())
	end()
	if err != nil {
		return err
	}
	var selected *python.Runtime
	for n := range runtimes {
		if runtimes[n].ID == j.RuntimeID {
			selected = &runtimes[n]
			break
		}
	}
	if selected == nil {
		return python.ErrRuntimeNotFound
	}
	if !selected.Installed || selected.Path == nil {
		return python.ErrRuntimeNotInstalled
	}
	end = progress.BeginTask("Creating isolated Python environment")
	executable, err := j.Manager.EnsureEnvironment(ctx, *selected)
	end()
	if err != nil {
		return err
	}
	end = progress.BeginTask("Saving Python selection")
	err = j.Config.UpdatePythonSelection(selected.ID, executable)
	end()
	if job.IsCancelled(ctx) {
		return nil
	}
	return err
}

type PythonPackageOperation string

const (
	PythonPackageInstall   PythonPackageOperation = "install"
	PythonPackageUpdate    PythonPackageOperation = "update"
	PythonPackageUninstall PythonPackageOperation = "uninstall"
)

type PythonPackagesJob struct {
	Manager   *python.Manager
	Config    PythonConfig
	Operation PythonPackageOperation
	Names     *[]string
}

func (j *PythonPackagesJob) Execute(ctx context.Context, progress *job.Progress) error {
	progress.Indefinite()
	var currentEnd func()
	reporter := func(event python.Event) {
		if currentEnd != nil {
			currentEnd()
		}
		if event.Phase != python.PhaseComplete {
			currentEnd = progress.BeginTask(event.Message)
		} else {
			currentEnd = nil
		}
	}
	defer func() {
		if currentEnd != nil {
			currentEnd()
		}
	}()

	runtimeID := j.Config.GetPythonRuntimeID()
	executable := j.Config.GetPythonPath()
	indexes := j.Config.GetPythonIndexes()
	var err error
	switch j.Operation {
	case PythonPackageInstall:
		if j.Names == nil {
			return errors.New("package names are required")
		}
		err = j.Manager.InstallPackages(ctx, runtimeID, executable, *j.Names, indexes, reporter)
	case PythonPackageUpdate:
		err = j.Manager.UpdatePackages(ctx, runtimeID, executable, j.Names, indexes, reporter)
	case PythonPackageUninstall:
		if j.Names == nil {
			return errors.New("package names are required")
		}
		err = j.Manager.UninstallPackages(ctx, runtimeID, executable, *j.Names, indexes, reporter)
	default:
		err = fmt.Errorf("unsupported Python package operation %q", j.Operation)
	}
	if job.IsCancelled(ctx) {
		return nil
	}
	return err
}

type RefreshPythonCatalogJob struct {
	Manager *python.Manager
	Indexes []python.Index
}

func (j *RefreshPythonCatalogJob) Execute(ctx context.Context, progress *job.Progress) error {
	progress.SetTotal(len(j.Indexes))
	for _, index := range j.Indexes {
		if job.IsCancelled(ctx) {
			return nil
		}
		end := progress.BeginTask("Refreshing " + index.Name)
		err := j.Manager.RefreshCatalog(ctx, index, nil)
		end()
		if err != nil {
			return err
		}
		progress.Increment()
	}
	return nil
}
