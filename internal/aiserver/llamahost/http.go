package llamahost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/stashapp/stash/internal/aiserver/proc"
)

var errRestartRequested = fmt.Errorf("llama-server restart requested")

type configurationError struct {
	message     string
	remediation string
}

func (e *configurationError) Error() string { return e.message }

type generationReason int

const (
	generationStopped generationReason = iota
	generationRestarted
	generationCrashed
)

func (s *Supervisor) awaitReady(child *proc.Process) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StartupTimeout)
	defer cancel()
	for {
		ready, err := s.startupProbe(ctx)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		timer := time.NewTimer(s.pollInterval)
		select {
		case <-s.stopCh:
			timer.Stop()
			return ErrClosed
		case <-s.restart:
			timer.Stop()
			return errRestartRequested
		case <-child.Exited():
			timer.Stop()
			return fmt.Errorf("llama-server exited during startup with code %d", child.ExitCode())
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("llama-server did not become ready within %s: %w", s.cfg.StartupTimeout, ctx.Err())
		case <-timer.C:
		}
	}
}

func (s *Supervisor) startupProbe(ctx context.Context) (bool, error) {
	response, err := s.get(ctx, "/health")
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		// The socket is not bound yet while model startup begins.
		return false, nil
	}
	_ = os.Chmod(s.socketPath(), 0o600)
	switch response.StatusCode {
	case http.StatusServiceUnavailable:
		discardBody(response.Body)
		return false, nil
	case http.StatusOK:
		discardBody(response.Body)
	default:
		discardBody(response.Body)
		return false, fmt.Errorf("llama-server health returned HTTP %d during startup", response.StatusCode)
	}

	props, err := s.get(ctx, "/props")
	if err != nil {
		return false, fmt.Errorf("read llama-server properties: %w", err)
	}
	defer props.Body.Close()
	if props.StatusCode != http.StatusOK {
		discardBody(props.Body)
		return false, fmt.Errorf("llama-server props returned HTTP %d", props.StatusCode)
	}
	var payload struct {
		Modalities struct {
			Vision bool `json:"vision"`
		} `json:"modalities"`
	}
	if err := json.NewDecoder(io.LimitReader(props.Body, 1<<20)).Decode(&payload); err != nil {
		return false, fmt.Errorf("decode llama-server properties: %w", err)
	}
	if !payload.Modalities.Vision {
		return false, &configurationError{
			message:     "The selected llama-server model does not expose vision input.",
			remediation: "Install a matched vision-language GGUF model and multimodal projector pair.",
		}
	}
	return true, nil
}

func (s *Supervisor) monitor(child *proc.Process) (generationReason, error) {
	ticker := time.NewTicker(s.probeInterval)
	defer ticker.Stop()
	missed := 0
	for {
		select {
		case <-s.stopCh:
			return generationStopped, nil
		case <-s.restart:
			return generationRestarted, nil
		case <-child.Exited():
			return generationCrashed, fmt.Errorf("llama-server exited with code %d", child.ExitCode())
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := s.readyHealth(ctx)
			cancel()
			if err == nil {
				missed = 0
				continue
			}
			missed++
			if missed >= failedProbeLimit {
				return generationCrashed, fmt.Errorf(
					"llama-server failed %d consecutive health probes: %w", missed, err)
			}
		}
	}
}

func (s *Supervisor) readyHealth(ctx context.Context) error {
	response, err := s.get(ctx, "/health")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (s *Supervisor) get(ctx context.Context, path string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://llama"+path, nil)
	if err != nil {
		return nil, err
	}
	return s.client.Do(request)
}

func (s *Supervisor) socketPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.socket
}
