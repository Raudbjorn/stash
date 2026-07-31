package python

import (
	"fmt"
	"io"
	"strconv"
	"time"
)

const ManagedUVVersion = "0.11.33"

type Phase string

const (
	PhaseResolving  Phase = "RESOLVING"
	PhasePreparing  Phase = "PREPARING"
	PhaseInstalling Phase = "INSTALLING"
	PhaseValidating Phase = "VALIDATING"
	PhaseComplete   Phase = "COMPLETE"
)

type Event struct {
	Phase   Phase
	Message string
}

type Reporter func(Event)

type RuntimeSource string

const (
	RuntimeSourceManaged  RuntimeSource = "MANAGED"
	RuntimeSourceSystem   RuntimeSource = "SYSTEM"
	RuntimeSourceExternal RuntimeSource = "EXTERNAL"
)

type EngineSource string

const (
	EngineSourceManaged EngineSource = "MANAGED"
	EngineSourceSystem  EngineSource = "SYSTEM"
)

func (e EngineSource) IsValid() bool {
	return e == EngineSourceManaged || e == EngineSourceSystem
}

func (e EngineSource) String() string { return string(e) }

func (e *EngineSource) UnmarshalGQL(value interface{}) error {
	raw, ok := value.(string)
	if !ok {
		return fmt.Errorf("PythonManagerSource must be a string")
	}
	*e = EngineSource(raw)
	if !e.IsValid() {
		return fmt.Errorf("%q is not a valid PythonManagerSource", raw)
	}
	return nil
}

func (e EngineSource) MarshalGQL(writer io.Writer) {
	fmt.Fprint(writer, strconv.Quote(e.String()))
}

func (e RuntimeSource) IsValid() bool {
	return e == RuntimeSourceManaged || e == RuntimeSourceSystem || e == RuntimeSourceExternal
}

func (e RuntimeSource) String() string { return string(e) }

func (e *RuntimeSource) UnmarshalGQL(value interface{}) error {
	raw, ok := value.(string)
	if !ok {
		return fmt.Errorf("PythonRuntimeSource must be a string")
	}
	*e = RuntimeSource(raw)
	if !e.IsValid() {
		return fmt.Errorf("%q is not a valid PythonRuntimeSource", raw)
	}
	return nil
}

func (e RuntimeSource) MarshalGQL(writer io.Writer) {
	fmt.Fprint(writer, strconv.Quote(e.String()))
}

type Index struct {
	Name    string `json:"name" yaml:"name" toml:"name"`
	URL     string `json:"url" yaml:"url" toml:"url"`
	Default bool   `json:"default" yaml:"default" toml:"default"`
}

type ManagerStatus struct {
	Installed bool
	Version   *string
	Path      *string
	Source    *EngineSource
	Error     *string
}

type Runtime struct {
	ID              string
	Key             string
	Version         string
	Implementation  string
	Architecture    string
	Libc            *string
	Variant         string
	Source          RuntimeSource
	Installed       bool
	Selected        bool
	Path            *string
	EnvironmentPath *string
	DownloadURL     *string
}

type Package struct {
	Name           string  `json:"name"`
	Version        string  `json:"version"`
	LatestVersion  *string `json:"latest_version,omitempty"`
	LatestFiletype *string `json:"latest_filetype,omitempty"`
}

type CatalogStatus struct {
	Index        string
	RefreshedAt  *time.Time
	ProjectCount int
	Stale        bool
	Error        *string
}

type PackageSearchResult struct {
	Name    string
	Indexes []string
}

type Status struct {
	Manager              ManagerStatus
	SelectedRuntime      *Runtime
	ConfiguredPythonPath string
	ManagedEnvironment   bool
}

type EnvironmentMetadata struct {
	RuntimeID string `json:"runtime_id"`
	Key       string `json:"key"`
	BasePath  string `json:"base_path"`
	Version   string `json:"version"`
}
