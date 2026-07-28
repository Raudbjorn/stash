package python

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func (m *Manager) EnvironmentPath(runtimeID string) string {
	digest := pathDigest(runtimeID)
	return filepath.Join(m.environmentsDir, digest[:16])
}

func EnvironmentExecutable(environmentPath string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(environmentPath, "Scripts", "python.exe")
	}
	return filepath.Join(environmentPath, "bin", "python")
}

func (m *Manager) EnsureEnvironment(ctx context.Context, selected Runtime) (string, error) {
	if !selected.Installed || selected.Path == nil || *selected.Path == "" {
		return "", ErrRuntimeNotInstalled
	}
	if err := m.ensureDirectories(); err != nil {
		return "", err
	}
	finalPath := m.EnvironmentPath(selected.ID)
	if err := m.validateEnvironment(ctx, finalPath, selected.ID, selected.Version); err == nil {
		return EnvironmentExecutable(finalPath), nil
	}

	temporaryPath, err := os.MkdirTemp(m.environmentsDir, ".environment-*")
	if err != nil {
		return "", err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return "", err
	}
	defer os.RemoveAll(temporaryPath)

	if _, err := m.runUV(ctx, []string{"venv", temporaryPath, "--python", *selected.Path, "--no-python-downloads", "--no-config"}); err != nil {
		return "", err
	}
	metadata := EnvironmentMetadata{RuntimeID: selected.ID, Key: selected.Key, BasePath: *selected.Path, Version: selected.Version}
	encoded, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(temporaryPath, "environment.json"), encoded, 0600); err != nil {
		return "", err
	}
	if err := m.validateEnvironment(ctx, temporaryPath, selected.ID, selected.Version); err != nil {
		return "", fmt.Errorf("validate new Python environment: %w", err)
	}

	backupPath := finalPath + ".backup"
	_ = os.RemoveAll(backupPath)
	hadFinal := false
	if _, err := os.Stat(finalPath); err == nil {
		hadFinal = true
		if err := os.Rename(finalPath, backupPath); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		if hadFinal {
			_ = os.Rename(backupPath, finalPath)
		}
		return "", err
	}
	if err := m.validateEnvironment(ctx, finalPath, selected.ID, selected.Version); err != nil {
		_ = os.RemoveAll(finalPath)
		if hadFinal {
			_ = os.Rename(backupPath, finalPath)
		}
		return "", fmt.Errorf("validate published Python environment: %w", err)
	}
	if hadFinal {
		_ = os.RemoveAll(backupPath)
	}
	return EnvironmentExecutable(finalPath), nil
}

func (m *Manager) CheckEnvironment(ctx context.Context, runtimeID, executable string) error {
	if runtimeID == "" {
		return errors.New("no managed Python runtime is selected")
	}
	environmentPath := m.EnvironmentPath(runtimeID)
	expectedExecutable, err := filepath.Abs(EnvironmentExecutable(environmentPath))
	if err != nil {
		return err
	}
	actualExecutable, err := filepath.Abs(executable)
	if err != nil {
		return err
	}
	if filepath.Clean(actualExecutable) != filepath.Clean(expectedExecutable) {
		return errors.New("selected Python executable is outside the Stash-managed environment")
	}
	metadata, err := readEnvironmentMetadata(environmentPath)
	if err != nil {
		return err
	}
	if metadata.RuntimeID != runtimeID {
		return errors.New("python environment metadata does not match the selected runtime")
	}
	return m.validateEnvironment(ctx, environmentPath, runtimeID, metadata.Version)
}

func readEnvironmentMetadata(environmentPath string) (EnvironmentMetadata, error) {
	data, err := os.ReadFile(filepath.Join(environmentPath, "environment.json"))
	if err != nil {
		return EnvironmentMetadata{}, err
	}
	var metadata EnvironmentMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return EnvironmentMetadata{}, err
	}
	if metadata.RuntimeID == "" || metadata.BasePath == "" || metadata.Version == "" {
		return EnvironmentMetadata{}, errors.New("incomplete Python environment metadata")
	}
	return metadata, nil
}

func (m *Manager) validateEnvironment(ctx context.Context, environmentPath, runtimeID, version string) error {
	metadata, err := readEnvironmentMetadata(environmentPath)
	if err != nil {
		return err
	}
	if metadata.RuntimeID != runtimeID {
		return errors.New("runtime ID does not match environment metadata")
	}
	executable := EnvironmentExecutable(environmentPath)
	result, err := m.run(ctx, executable, []string{"-c", "import json,platform,sys; print(json.dumps({'executable': sys.executable, 'prefix': sys.prefix, 'version': platform.python_version()}))"}, m.environment())
	if err != nil {
		return err
	}
	var health struct {
		Executable string `json:"executable"`
		Prefix     string `json:"prefix"`
		Version    string `json:"version"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.stdout)), &health); err != nil {
		return fmt.Errorf("decode Python environment health: %w", err)
	}
	expectedPrefix, _ := filepath.Abs(environmentPath)
	actualPrefix, _ := filepath.Abs(health.Prefix)
	if filepath.Clean(actualPrefix) != filepath.Clean(expectedPrefix) {
		return fmt.Errorf("python sys.prefix %q does not match environment %q", health.Prefix, environmentPath)
	}
	expectedExecutable, _ := filepath.Abs(executable)
	actualExecutable, _ := filepath.Abs(health.Executable)
	if filepath.Clean(actualExecutable) != filepath.Clean(expectedExecutable) {
		return fmt.Errorf("python sys.executable %q does not match %q", health.Executable, executable)
	}
	if majorMinor(health.Version) != majorMinor(version) {
		return fmt.Errorf("python version %q does not match runtime %q", health.Version, version)
	}
	return nil
}

func majorMinor(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return version
	}
	return strings.Join(parts[:2], ".")
}
