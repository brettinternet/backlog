package project

// Settings is the shared, non-secret project configuration vocabulary. Loading
// these declarations never executes a program or contacts a source.
type Settings struct {
	Version      int                `yaml:"version,omitempty" json:"version"`
	Git          GitConfig          `yaml:"git,omitempty" json:"git"`
	Root         string             `yaml:"root,omitempty" json:"root,omitempty"`
	Project      ProjectConfig      `yaml:"project,omitempty" json:"project"`
	UI           UIConfig           `yaml:"ui,omitempty" json:"ui"`
	Sources      []SourceConfig     `yaml:"sources,omitempty" json:"sources,omitempty"`
	Statuses     []StatusConfig     `yaml:"statuses,omitempty" json:"statuses,omitempty"`
	Workflow     WorkflowConfig     `yaml:"workflow,omitempty" json:"workflow"`
	Milestones   []Milestone        `yaml:"milestones,omitempty" json:"milestones,omitempty"`
	Labels       []string           `yaml:"labels,omitempty" json:"labels,omitempty"`
	Ready        ReadyConfig        `yaml:"ready,omitempty" json:"ready"`
	Defaults     DefaultsConfig     `yaml:"defaults,omitempty" json:"defaults"`
	Views        []ViewConfig       `yaml:"views,omitempty" json:"views,omitempty"`
	Coordination CoordinationConfig `yaml:"coordination,omitempty" json:"coordination"`
	Hooks        []HookConfig       `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	Actions      []ActionConfig     `yaml:"actions,omitempty" json:"actions,omitempty"`
}
type GitConfig struct {
	AutoCommit bool `yaml:"autoCommit,omitempty" json:"autoCommit"`
}
type ProjectConfig struct {
	ID   string `yaml:"id,omitempty" json:"id"`
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
}
type SourceConfig struct {
	ID            string               `yaml:"id" json:"id"`
	Type          string               `yaml:"type" json:"type"`
	Path          string               `yaml:"path,omitempty" json:"path,omitempty"`
	Prefix        string               `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	Team          string               `yaml:"team,omitempty" json:"team,omitempty"`
	Account       string               `yaml:"account,omitempty" json:"account,omitempty"`
	Site          string               `yaml:"site,omitempty" json:"site,omitempty"`
	Project       string               `yaml:"project,omitempty" json:"project,omitempty"`
	Host          string               `yaml:"host,omitempty" json:"host,omitempty"`
	Repo          string               `yaml:"repo,omitempty" json:"repo,omitempty"`
	Run           []string             `yaml:"run,omitempty" json:"run,omitempty"`
	GitHubProject *GitHubProjectConfig `yaml:"githubProject,omitempty" json:"githubProject,omitempty"`
	AllowWrites   bool                 `yaml:"allowWrites,omitempty" json:"allowWrites,omitempty"`
}

// GitHubProjectConfig binds immutable provider IDs, not mutable display names.
type GitHubProjectConfig struct {
	Owner       string            `yaml:"owner" json:"owner"`
	Number      int               `yaml:"number" json:"number"`
	ID          string            `yaml:"id" json:"id"`
	FieldID     string            `yaml:"fieldId" json:"fieldId"`
	Options     map[string]string `yaml:"options" json:"options"`
	AllowWrites bool              `yaml:"allowWrites,omitempty" json:"allowWrites,omitempty"`
}
type StatusConfig struct {
	Key      string `yaml:"key" json:"key"`
	Name     string `yaml:"name" json:"name"`
	Category string `yaml:"category" json:"category"`
}
type WorkflowConfig struct {
	Start string `yaml:"start,omitempty" json:"start,omitempty"`
	Done  string `yaml:"done,omitempty" json:"done,omitempty"`
}
type Milestone struct {
	Key         string `yaml:"key" json:"key"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Archived    bool   `yaml:"archived,omitempty" json:"archived"`
}
type ReadyConfig struct {
	Statuses       []string `yaml:"statuses,omitempty" json:"statuses,omitempty"`
	SatisfiedBy    []string `yaml:"satisfiedBy,omitempty" json:"satisfiedBy,omitempty"`
	ExcludeLabels  []string `yaml:"excludeLabels,omitempty" json:"excludeLabels,omitempty"`
	SkipContainers bool     `yaml:"skipContainers,omitempty" json:"skipContainers"`
}
type DefaultsConfig struct {
	Type string   `yaml:"type,omitempty" json:"type,omitempty"`
	DOD  []string `yaml:"dod,omitempty" json:"dod,omitempty"`
}
type ViewConfig struct {
	Columns []string `yaml:"columns,omitempty" json:"columns,omitempty"`
	Name    string   `yaml:"name" json:"name"`
	Query   string   `yaml:"query,omitempty" json:"query,omitempty"`
	Sort    string   `yaml:"sort,omitempty" json:"sort,omitempty"`
	Group   string   `yaml:"group,omitempty" json:"group,omitempty"`
	Layout  string   `yaml:"layout,omitempty" json:"layout,omitempty"`
}
type CoordinationConfig struct {
	Worklease *struct{} `yaml:"worklease,omitempty" json:"worklease,omitempty"`
}
type HookConfig struct {
	Name    string   `yaml:"name" json:"name"`
	On      string   `yaml:"on" json:"on"`
	If      string   `yaml:"if,omitempty" json:"if,omitempty"`
	Phase   string   `yaml:"phase,omitempty" json:"phase,omitempty"`
	CWD     string   `yaml:"cwd,omitempty" json:"cwd,omitempty"`
	Run     []string `yaml:"run" json:"run"`
	Timeout string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}
type ActionConfig struct {
	Name    string   `yaml:"name" json:"name"`
	Key     string   `yaml:"key,omitempty" json:"key,omitempty"`
	When    string   `yaml:"when,omitempty" json:"when,omitempty"`
	CWD     string   `yaml:"cwd,omitempty" json:"cwd,omitempty"`
	Run     []string `yaml:"run" json:"run"`
	Confirm bool     `yaml:"confirm,omitempty" json:"confirm,omitempty"`
	Detach  bool     `yaml:"detach,omitempty" json:"detach,omitempty"`
}
type AccountConfig struct {
	Name       string   `yaml:"name,omitempty" json:"name,omitempty"`
	Host       string   `yaml:"host,omitempty" json:"host,omitempty"`
	User       string   `yaml:"user,omitempty" json:"user,omitempty"`
	Credential []string `yaml:"credential,omitempty" json:"-"`
	Env        string   `yaml:"env,omitempty" json:"-"`
}
type MeConfig struct {
	Default string `yaml:"default,omitempty" json:"default,omitempty"`
}
type WorkspaceConfig struct {
	Name     string   `yaml:"name" json:"name"`
	Projects []string `yaml:"projects" json:"projects"`
}
type UserConfig struct {
	Settings         `yaml:",inline"`
	Me               MeConfig                   `yaml:"me,omitempty" json:"me"`
	Accounts         map[string][]AccountConfig `yaml:"accounts,omitempty" json:"-"`
	Projects         map[string]Settings        `yaml:"projects,omitempty" json:"-"`
	Workspaces       []WorkspaceConfig          `yaml:"workspaces,omitempty" json:"workspaces,omitempty"`
	DefaultWorkspace string                     `yaml:"defaultWorkspace,omitempty" json:"defaultWorkspace,omitempty"`
	Keys             map[string]string          `yaml:"keys,omitempty" json:"keys,omitempty"`
}

type Provenance struct {
	Layer string `json:"layer"`
	File  string `json:"file,omitempty"`
	Line  int    `json:"line,omitempty"`
}

// Privileged identifies an effective declaration and its resolved trust boundary.
// Source and hook runners must enforce this boundary before executing entries.
type Privileged struct {
	Key     string     `json:"key"`
	Trusted bool       `json:"trusted"`
	Origin  Provenance `json:"origin"`
}
