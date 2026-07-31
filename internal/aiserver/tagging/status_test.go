package tagging

import "testing"

func TestStatusCurrentTracksLiveSupervisorState(t *testing.T) {
	available := false
	message := "Loading the vision model."
	remediation := "Wait for llama-server to finish loading."
	status := Status{
		Provider: ProviderVLM,
		Message:  "frozen startup message",
		live: func() (bool, string, string) {
			return available, message, remediation
		},
	}

	loading := status.Current(nil)
	if loading.Available || loading.Message != message || loading.Remediation != remediation {
		t.Fatalf("loading status = %+v", loading)
	}

	available = true
	message = "llama-server is ready."
	remediation = ""
	ready := status.Current(nil)
	if !ready.Available || ready.Message != message || ready.Remediation != "" {
		t.Fatalf("ready status = %+v", ready)
	}

	available = false
	message = "llama-server crashed."
	remediation = "llama-server stderr: projector mismatch"
	failed := status.Current(nil)
	if failed.Available || failed.Message != message || failed.Remediation != remediation {
		t.Fatalf("failed status = %+v", failed)
	}
}
