// Package project owns discovery, identity, and read-only layered configuration.
package project

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Paths are resolved without creating directories. Use XDG conventions on macOS
// too, rather than os.UserConfigDir's Application Support default.
type Paths struct {
	ConfigFile string `json:"configFile"`
	StateDir   string `json:"stateDir"`
	CacheDir   string `json:"cacheDir"`
}

func UserPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	resolve := func(name, fallback string) (string, error) {
		value := os.Getenv(name)
		if value == "" {
			value = filepath.Join(home, fallback)
		}
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("%s must be absolute", name)
		}
		return filepath.Join(value, "backlog"), nil
	}
	config, err := resolve("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return Paths{}, err
	}
	state, err := resolve("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return Paths{}, err
	}
	cache, err := resolve("XDG_CACHE_HOME", ".cache")
	if err != nil {
		return Paths{}, err
	}
	return Paths{ConfigFile: filepath.Join(config, "config.yaml"), StateDir: state, CacheDir: cache}, nil
}

type UIConfig struct {
	HighContrast bool `yaml:"highContrast" json:"highContrast"`
}

type Config struct {
	ReadOnlySources  map[string]bool `yaml:"-" json:"readOnlySources,omitempty"`
	Workspace        *Workspace      `yaml:"-" json:"-"`
	Settings         `yaml:",inline"`
	SavedViews       []SavedView                `yaml:"-" json:"savedViews,omitempty"`
	Paths            Paths                      `yaml:"-" json:"paths"`
	Discovery        Discovery                  `yaml:"-" json:"discovery"`
	HostLocal        bool                       `yaml:"-" json:"hostLocal"`
	Provenance       map[string]Provenance      `yaml:"-" json:"provenance"`
	Privileged       []Privileged               `yaml:"-" json:"privileged,omitempty"`
	Ignored          []Ignored                  `yaml:"-" json:"ignored,omitempty"`
	Me               MeConfig                   `yaml:"-" json:"me"`
	Accounts         map[string][]AccountConfig `yaml:"-" json:"-"`
	Workspaces       []WorkspaceConfig          `yaml:"-" json:"workspaces,omitempty"`
	DefaultWorkspace string                     `yaml:"-" json:"defaultWorkspace,omitempty"`
	Keys             map[string]string          `yaml:"-" json:"keys,omitempty"`
}

// Load retains the user-settings entry point for callers without a project.
func Load(paths Paths, highContrast *bool) (Config, error) {
	return Resolve(paths, "", highContrast)
}

// Resolve applies D16 layers without changing cwd or writing any files. A nil
// flag is absent; false is an explicit override. Lists replace rather than append.
func Resolve(paths Paths, directory string, highContrast *bool) (Config, error) {
	cfg := Config{Paths: paths, Provenance: map[string]Provenance{}}
	effective := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	var defaults yaml.Node
	_ = yaml.Unmarshal([]byte("version: 1\nroot: primary\ngit: {autoCommit: false}\nui: {highContrast: false}\nready: {statuses: [todo], satisfiedBy: [done, canceled], skipContainers: true}\nagents: {claude: {session: CLAUDE_CODE_SESSION_ID}, codex: {session: CODEX_THREAD_ID}, pi: {session: PI_SESSION_ID}}\n"), &defaults)
	overlay(effective, defaults.Content[0], "", Provenance{Layer: "built-in"}, cfg.Provenance)
	userNode, err := readConfig(paths.ConfigFile, reflect.TypeFor[UserConfig]())
	if err != nil {
		return Config{}, err
	}
	var user UserConfig
	if err := userNode.Decode(&user); err != nil {
		return Config{}, err
	}
	if err := validateUserAccounts(userNode); err != nil {
		return Config{}, fmt.Errorf("%s: %w", paths.ConfigFile, err)
	}
	overlaySettings(effective, userNode, Provenance{Layer: "user", File: paths.ConfigFile}, cfg.Provenance)
	cfg.Me, cfg.Accounts, cfg.Workspaces, cfg.DefaultWorkspace, cfg.Keys = user.Me, user.Accounts, user.Workspaces, user.DefaultWorkspace, user.Keys
	for _, key := range []string{"me", "workspaces", "defaultWorkspace", "keys"} {
		if value := field(userNode, key); value != nil {
			recordOrigin(value, key, Provenance{Layer: "user", File: paths.ConfigFile}, cfg.Provenance)
		}
	}
	if directory != "" {
		cfg.Discovery, err = Discover(directory)
		if err != nil {
			return Config{}, err
		}
		d := &cfg.Discovery
		if d.CheckoutRoot != "" {
			// Route first, then load facts only from the selected checkout. The
			// invoking checkout may override the shared root policy, not its facts.
			type repoLayer struct {
				file, layer string
				node        *yaml.Node
			}
			var layers []repoLayer
			roots := []string{d.ControlRoot}
			if d.CheckoutRoot != d.ControlRoot {
				roots = append(roots, d.CheckoutRoot)
			}
			root := field(effective, "root")
			rootOrigin := cfg.Provenance["root"]
			for _, directory := range roots {
				for _, entry := range []repoLayer{{file: ProjectFile(directory), layer: "project"}, {file: LocalFile(directory), layer: "local"}} {
					entry.node, err = readConfig(entry.file, reflect.TypeFor[Settings]())
					if err != nil {
						return Config{}, err
					}
					layers = append(layers, entry)
					if n := field(entry.node, "root"); n != nil {
						root = n
						rootOrigin = Provenance{Layer: entry.layer, File: entry.file, Line: n.Line}
					}
				}
			}
			if root.Value == "checkout" {
				d.ControlRoot = d.CheckoutRoot
			}
			for _, entry := range layers {
				if filepath.Dir(entry.file) != d.ControlRoot {
					continue
				}
				if views := field(entry.node, "views"); views != nil {
					var entries []ViewConfig
					if err := views.Decode(&entries); err != nil {
						return Config{}, err
					}
					cfg.SavedViews = appendViews(cfg.SavedViews, entry.layer, entries)
				}
				node := entry.node
				// Agent entries will carry commands (BL-138), so a repository
				// can't set them.
				if agents := field(node, "agents"); agents != nil {
					cfg.Ignored = append(cfg.Ignored, Ignored{Key: "agents", Origin: Provenance{Layer: entry.layer, File: entry.file, Line: agents.Line}})
					node = withoutField(node, "agents")
				}
				overlay(effective, node, "", Provenance{Layer: entry.layer, File: entry.file}, cfg.Provenance)
			}
			replaceField(effective, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "root"}, cloneNode(root))
			cfg.Provenance["root"] = rootOrigin
			if n := field(field(effective, "project"), "id"); n == nil || n.Value == "" {
				id, local, idErr := Identity(*d)
				if idErr != nil {
					return Config{}, idErr
				}
				cfg.HostLocal = local
				identity := &yaml.Node{}
				if err := identity.Encode(ProjectConfig{ID: id}); err != nil {
					return Config{}, err
				}
				overlay(effective, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: "project"}, identity}}, "", Provenance{Layer: "discovery"}, cfg.Provenance)
			}
			id := field(field(effective, "project"), "id").Value
			if projects := field(userNode, "projects"); projects != nil {
				if override := field(projects, id); override != nil {
					overlay(effective, override, "", Provenance{Layer: "user-project", File: paths.ConfigFile}, cfg.Provenance)
					if field(field(override, "project"), "id") != nil {
						cfg.HostLocal = false
					}
				}
			}
			d.Detected = detectSources(d.ControlRoot)
		}
	}
	if highContrast == nil {
		if raw := os.Getenv("BL_HIGH_CONTRAST"); raw != "" {
			value, parseErr := strconv.ParseBool(raw)
			if parseErr != nil {
				return Config{}, fmt.Errorf("environment: line 1: key BL_HIGH_CONTRAST: must be a boolean (true or false)")
			}
			setContrast(effective, value, Provenance{Layer: "environment"}, cfg.Provenance)
		}
	} else {
		setContrast(effective, *highContrast, Provenance{Layer: "flag"}, cfg.Provenance)
	}
	if err := effective.Decode(&cfg.Settings); err != nil {
		return Config{}, err
	}
	if err := validateSettings(effective); err != nil {
		return Config{}, err
	}
	if err := cfg.validateGitHubStatuses(); err != nil {
		return Config{}, err
	}
	if err := cfg.validateSourceStatuses(); err != nil {
		return Config{}, err
	}
	if err := cfg.resolveBacklogMD(); err != nil {
		return Config{}, err
	}
	if err := cfg.validateStatusIncludes(); err != nil {
		return Config{}, err
	}
	// A project-level ready.sprint with no capable source gates nothing, which
	// is a mistake. A user-wide default may apply only where sources allow.
	if origin := cfg.Provenance["ready.sprint"]; cfg.Discovery.CheckoutRoot != "" && cfg.Ready.Sprint != "" && origin.Layer != "user" && !cfg.SprintCapable() {
		return Config{}, fmt.Errorf("%s: line %d: key ready.sprint: no source provides sprints", origin.File, origin.Line)
	}
	// Milestone archived is serialized even when omitted in YAML. Attribute its
	// implicit false value to built-ins, without replacing an explicit origin.
	for i := range cfg.Milestones {
		key := fmt.Sprintf("milestones[%d].archived", i)
		if _, ok := cfg.Provenance[key]; !ok {
			cfg.Provenance[key] = Provenance{Layer: "built-in"}
		}
	}
	cfg.SavedViews = appendViews(cfg.SavedViews, "user", user.Views)
	if override, ok := user.Projects[cfg.Project.ID]; ok {
		cfg.SavedViews = appendViews(cfg.SavedViews, "user-project", override.Views)
	}
	cfg.Privileged = privileged(cfg)
	cfg.resolveTrust()
	return cfg, nil
}

func setContrast(node *yaml.Node, value bool, origin Provenance, provenance map[string]Provenance) {
	var n yaml.Node
	_ = yaml.Unmarshal([]byte("ui: {highContrast: "+strconv.FormatBool(value)+"}"), &n)
	overlay(node, n.Content[0], "", origin, provenance)
}

func field(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func overlaySettings(dst, src *yaml.Node, origin Provenance, provenance map[string]Provenance) {
	allowed := yamlFields(reflect.TypeFor[Settings]())
	filtered := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i < len(src.Content); i += 2 {
		if _, ok := allowed[src.Content[i].Value]; ok {
			filtered.Content = append(filtered.Content, src.Content[i], src.Content[i+1])
		}
	}
	overlay(dst, filtered, "", origin, provenance)
}

func withoutField(node *yaml.Node, key string) *yaml.Node {
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value != key {
			out.Content = append(out.Content, node.Content[i], node.Content[i+1])
		}
	}
	return out
}

// Copy nodes before merging so earlier layers remain reusable for checkout opt-out.
func cloneNode(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = nil
	for _, child := range n.Content {
		c.Content = append(c.Content, cloneNode(child))
	}
	return &c
}
func overlay(dst, src *yaml.Node, prefix string, origin Provenance, provenance map[string]Provenance) {
	for i := 0; i < len(src.Content); i += 2 {
		key, value := src.Content[i], src.Content[i+1]
		path := key.Value
		if prefix != "" {
			path = prefix + "." + path
		}
		existing := field(dst, key.Value)
		if value.Kind == yaml.MappingNode {
			if existing == nil || existing.Kind != yaml.MappingNode {
				existing = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				replaceField(dst, key, existing)
			}
			overlay(existing, value, path, origin, provenance)
			if len(value.Content) == 0 {
				p := origin
				p.Line = key.Line
				provenance[path] = p
			}
		} else {
			replaceField(dst, key, cloneNode(value))
			for old := range provenance {
				if old == path || strings.HasPrefix(old, path+".") || strings.HasPrefix(old, path+"[") {
					delete(provenance, old)
				}
			}
			recordOrigin(value, path, origin, provenance)
		}
	}
}

func recordOrigin(node *yaml.Node, path string, origin Provenance, provenance map[string]Provenance) {
	origin.Line = node.Line
	provenance[path] = origin
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			recordOrigin(node.Content[i+1], path+"."+node.Content[i].Value, origin, provenance)
		}
	}
	if node.Kind == yaml.SequenceNode {
		for i, child := range node.Content {
			recordOrigin(child, fmt.Sprintf("%s[%d]", path, i), origin, provenance)
		}
	}
}
func replaceField(node, key, value *yaml.Node) {
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key.Value {
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content, cloneNode(key), value)
}

func readConfig(path string, typ reflect.Type) (*yaml.Node, error) {
	empty := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: line 1: key $: %w", path, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("%s: line 1: key $: config exceeds 1 MiB", path)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return empty, nil
		}
		return nil, fmt.Errorf("%s: line 1: key $: %w", path, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: line %d: key $: expected a single YAML document: %v", path, extra.Line, err)
	}
	node := doc.Content[0]
	if err := validateNode(node, typ, "$"); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := validateSettings(node); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if projects := field(node, "projects"); projects != nil {
		for i := 1; i < len(projects.Content); i += 2 {
			if err := validateSettings(projects.Content[i]); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
	}
	return node, nil
}

func yamlFields(typ reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("yaml")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if opts == "inline" {
			for key, value := range yamlFields(f.Type) {
				fields[key] = value
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		fields[name] = f.Type
	}
	return fields
}
func validateNode(node *yaml.Node, typ reflect.Type, key string) error {
	fail := func(message string) error { return fmt.Errorf("line %d: key %s: %s", node.Line, key, message) }
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return fail("YAML anchors and aliases are not supported")
	}
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct, reflect.Map:
		if node.Kind != yaml.MappingNode {
			return fail("expected a mapping")
		}
		seen := map[string]bool{}
		var fields map[string]reflect.Type
		if typ.Kind() == reflect.Struct {
			fields = yamlFields(typ)
		}
		for i := 0; i < len(node.Content); i += 2 {
			k, v := node.Content[i], node.Content[i+1]
			path := k.Value
			if key != "$" {
				path = key + "." + path
			}
			if k.Tag != "!!str" {
				return fmt.Errorf("line %d: key %s: expected a string key", k.Line, path)
			}
			if seen[k.Value] {
				return fmt.Errorf("line %d: key %s: duplicate key", k.Line, path)
			}
			seen[k.Value] = true
			var child reflect.Type
			if typ.Kind() == reflect.Map {
				child = typ.Elem()
			} else {
				var ok bool
				child, ok = fields[k.Value]
				if !ok {
					return fmt.Errorf("line %d: key %s: unknown field", k.Line, path)
				}
			}
			if err := validateNode(v, child, path); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if node.Kind != yaml.SequenceNode {
			return fail("expected a sequence")
		}
		for i, child := range node.Content {
			if err := validateNode(child, typ.Elem(), fmt.Sprintf("%s[%d]", key, i)); err != nil {
				return err
			}
		}
	default:
		tag := "!!str"
		if typ.Kind() == reflect.Bool {
			tag = "!!bool"
		}
		if typ.Kind() == reflect.Int {
			tag = "!!int"
		}
		if node.Kind != yaml.ScalarNode || node.Tag != tag {
			return fail("expected " + strings.TrimPrefix(tag, "!!"))
		}
		if err := node.Decode(reflect.New(typ).Interface()); err != nil {
			return fail(err.Error())
		}
	}
	return nil
}
func validateUserAccounts(node *yaml.Node) error {
	accounts := field(node, "accounts")
	if accounts == nil {
		return nil
	}
	for i := 0; i+1 < len(accounts.Content); i += 2 {
		provider, list := accounts.Content[i].Value, accounts.Content[i+1]
		for index, entry := range list.Content {
			key := fmt.Sprintf("accounts.%s[%d]", provider, index)
			optIn := field(entry, "oauth")
			if optIn == nil || optIn.Value != "true" {
				continue
			}
			for _, required := range []string{"name", "user"} {
				value := field(entry, required)
				if value == nil || strings.TrimSpace(value.Value) == "" {
					return fmt.Errorf("line %d: key %s.%s: required for OAuth", entry.Line, key, required)
				}
			}
			if field(entry, "credential") != nil || field(entry, "env") != nil {
				return fmt.Errorf("line %d: key %s: OAuth cannot be combined with helper or environment credentials", entry.Line, key)
			}
		}
	}
	return nil
}

func validateSettings(node *yaml.Node) error {
	fail := func(n *yaml.Node, key, message string) error {
		return fmt.Errorf("line %d: key %s: %s", n.Line, key, message)
	}
	if n := field(node, "version"); n != nil && n.Value != "1" {
		return fail(n, "version", "must be 1")
	}
	if n := field(node, "root"); n != nil && n.Value != "primary" && n.Value != "checkout" {
		return fail(n, "root", "must be primary or checkout")
	}
	if ready := field(node, "ready"); ready != nil {
		if n := field(ready, "sprint"); n != nil && n.Value != "current" && n.Value != "next" {
			return fail(n, "ready.sprint", "must be current or next")
		}
	}
	for _, group := range []string{"hooks", "actions", "views", "workspaces"} {
		if list := field(node, group); list != nil {
			seenNames := map[string]bool{}
			for i, entry := range list.Content {
				required := []string{"name"}
				switch group {
				case "hooks":
					required = append(required, "on", "run")
				case "actions":
					required = append(required, "run")
				case "workspaces":
					required = append(required, "projects")
				}
				for _, key := range required {
					if n := field(entry, key); n == nil {
						return fail(entry, fmt.Sprintf("%s[%d].%s", group, i, key), "required")
					}
				}
				if group == "hooks" {
					name := field(entry, "name")
					if strings.TrimSpace(name.Value) == "" {
						return fail(name, fmt.Sprintf("hooks[%d].name", i), "must not be empty")
					}
					if seenNames[name.Value] {
						return fail(name, "hooks.name", "duplicate "+name.Value)
					}
					seenNames[name.Value] = true
					on := field(entry, "on")
					if strings.TrimSpace(on.Value) == "" {
						return fail(on, fmt.Sprintf("hooks[%d].on", i), "must not be empty")
					}
					run := field(entry, "run")
					if len(run.Content) == 0 || strings.TrimSpace(run.Content[0].Value) == "" {
						return fail(run, fmt.Sprintf("hooks[%d].run", i), "requires an executable")
					}
				}
				if phase := field(entry, "phase"); phase != nil {
					if phase.Value != "before" && phase.Value != "after" {
						return fail(phase, "hooks.phase", "must be before or after")
					}
					if phase.Value == "before" {
						on := field(entry, "on").Value
						if on != "item.created" && on != "item.updated" && on != "item.archived" {
							return fail(phase, "hooks.phase", "before hooks require an item mutation event")
						}
					}
				}
			}
		}
	}
	if views := field(node, "views"); views != nil {
		seen := map[string]bool{}
		for _, entry := range views.Content {
			name := field(entry, "name")
			if name == nil || strings.TrimSpace(name.Value) == "" {
				return fail(entry, "views.name", "required")
			}
			if seen[name.Value] {
				return fail(name, "views.name", "duplicate "+name.Value)
			}
			seen[name.Value] = true
			if columns := field(entry, "columns"); columns != nil {
				for _, column := range columns.Content {
					switch column.Value {
					case "id", "priority", "status", "blockers", "children", "claim", "assignee", "updated", "title":
					default:
						return fail(column, "views.columns", "unknown column")
					}
				}
			}
		}
	}
	for _, list := range []string{"milestones", "sources", "statuses"} {
		n := field(node, list)
		if n == nil {
			continue
		}
		seen := map[string]bool{}
		id := "key"
		if list == "sources" {
			id = "id"
		}
		for i, entry := range n.Content {
			key := field(entry, id)
			if key == nil || key.Value == "" {
				return fail(entry, fmt.Sprintf("%s[%d].%s", list, i, id), "required")
			}
			if seen[key.Value] {
				return fail(key, list+"."+id, "duplicate "+key.Value)
			}
			seen[key.Value] = true
			if list == "milestones" || list == "statuses" {
				if name := field(entry, "name"); name == nil || name.Value == "" {
					return fail(entry, list+".name", "required")
				}
			}
			if list == "sources" {
				kind := field(entry, "type")
				if kind == nil {
					return fail(entry, "sources.type", "required")
				}
				switch kind.Value {
				case "native", "backlog-md", "beads", "github", "linear", "jira", "external":
				default:
					return fail(kind, "sources.type", "unknown source type")
				}
			}
			if list == "statuses" {
				category := field(entry, "category")
				if category == nil {
					return fail(entry, "statuses.category", "required")
				}
				switch category.Value {
				case "backlog", "todo", "active", "done", "canceled":
				default:
					return fail(category, "statuses.category", "unknown category")
				}
			}
		}
	}
	return nil
}

// validateSourceStatuses checks that sources[].statuses names configured
// statuses, on sources that don't report their own (design.md D35).
func (c Config) validateSourceStatuses() error {
	for i, s := range c.Sources {
		key := fmt.Sprintf("sources[%d].statuses", i)
		p := c.Provenance[key]
		if len(s.Statuses) > 0 && (s.Type == "github" || s.Type == "linear" || s.Type == "jira") {
			return fmt.Errorf("%s: line %d: key %s: source %s reports its own statuses; remove statuses", p.File, p.Line, key, s.ID)
		}
		for _, status := range s.Statuses {
			if !slices.ContainsFunc(c.Statuses, func(configured StatusConfig) bool { return configured.Key == status }) {
				return fmt.Errorf("%s: line %d: key %s: unknown status %s; add it to statuses or remove it here", p.File, p.Line, key, status)
			}
		}
	}
	return nil
}

// validateStatusIncludes keeps every key in at most one status entry, so a
// key always shows in one column and a write to an entry has a single meaning
// per source (design.md D36).
func (c Config) validateStatusIncludes() error {
	at := func(key, message string) error {
		p := c.Provenance[key]
		return fmt.Errorf("%s: line %d: key %s: %s", p.File, p.Line, key, message)
	}
	owner := map[string]string{}
	for _, status := range c.Statuses {
		owner[status.Key] = status.Key
	}
	for i, status := range c.Statuses {
		for j, member := range status.Includes {
			path := fmt.Sprintf("statuses[%d].includes[%d]", i, j)
			switch previous, ok := owner[member]; {
			case ok && previous == member:
				return at(path, fmt.Sprintf("%s is a status entry; merge the entries or remove it here", member))
			case ok:
				return at(path, fmt.Sprintf("%s is already included by %s; keep it in one entry", member, previous))
			}
			owner[member] = status.Key
		}
	}
	return nil
}

// validateGitHubStatuses refuses status entries that contradict the category
// the GitHub adapter assigns a key, because the adapter would ignore them: an
// open issue would stay ready under {key: open, category: backlog}. Mapped
// labels and project options share that key space, so they're checked here.
func (c Config) validateGitHubStatuses() error {
	at := func(key, message string) error {
		p := c.Provenance[key]
		return fmt.Errorf("%s: line %d: key %s: %s", p.File, p.Line, key, message)
	}
	// closed is done, or canceled when closed as not planned.
	matches := func(key, assigned, category string) bool {
		return category == assigned || key == "closed" && category == "canceled"
	}
	assigned, by := map[string]string{}, map[string]string{}
	if slices.ContainsFunc(c.Sources, func(s SourceConfig) bool { return s.Type == "github" }) {
		for key, category := range map[string]string{"open": "todo", "closed": "done", "unknown": "backlog"} {
			assigned[key], by[key] = category, "GitHub"
		}
	}
	if slices.ContainsFunc(c.Sources, func(s SourceConfig) bool { return s.Type == "github" && s.GitHubProject != nil }) {
		assigned["draft"], by["draft"] = "backlog", "GitHub"
	}
	for i, s := range c.Sources {
		if s.Type != "github" {
			continue
		}
		path, mapping := fmt.Sprintf("sources[%d].statusLabels", i), s.StatusLabels
		if s.Repo == "" && len(s.StatusLabels) > 0 {
			return at(path, fmt.Sprintf("source %s has statusLabels but no repo; status labels need one repository", s.ID))
		}
		if s.GitHubProject != nil {
			if len(s.StatusLabels) > 0 {
				return at(path, fmt.Sprintf("source %s sets both statusLabels and githubProject; keep one", s.ID))
			}
			path, mapping = fmt.Sprintf("sources[%d].githubProject.options", i), s.GitHubProject.Options
		}
		// GitHub label names are case-insensitive.
		labels := map[string]string{}
		for _, key := range slices.Sorted(maps.Keys(mapping)) {
			category := mapping[key]
			if s.GitHubProject == nil {
				folded := strings.ToLower(key)
				switch {
				case strings.TrimSpace(key) == "":
					return at(path, "label names can't be empty")
				case folded == "open" || folded == "closed" || folded == "unknown":
					return at(path+"."+key, fmt.Sprintf("label %s would shadow the %s status; use another label", key, folded))
				case labels[folded] != "":
					return at(path+"."+key, fmt.Sprintf("labels %s and %s are the same GitHub label; keep one", labels[folded], key))
				case !slices.Contains([]string{"backlog", "todo", "active", "done", "canceled"}, category):
					return at(path+"."+key, fmt.Sprintf("unknown category %s; use backlog, todo, active, done, or canceled", category))
				}
				labels[folded] = key
			}
			if previous, ok := assigned[key]; ok && !matches(key, previous, category) {
				return at(path+"."+key, fmt.Sprintf("source %s maps %s to %s, but %s assigns it %s; map it to one category", s.ID, key, category, by[key], previous))
			}
			assigned[key], by[key] = category, "source "+s.ID
		}
	}
	for i, status := range c.Statuses {
		for j, member := range status.Includes {
			if category, ok := assigned[member]; ok && !matches(member, category, status.Category) {
				return at(fmt.Sprintf("statuses[%d].includes[%d]", i, j), fmt.Sprintf("%s assigns %s the %s category, but entry %s is %s; include it in a %s entry", by[member], member, category, status.Key, status.Category, category))
			}
		}
		if category, ok := assigned[status.Key]; ok && !matches(status.Key, category, status.Category) {
			if status.Key == "closed" {
				category = "done or canceled"
			}
			return at(fmt.Sprintf("statuses[%d].category", i), fmt.Sprintf("%s assigns %s the %s category, not %s; set category: %s or remove the entry", by[status.Key], status.Key, category, status.Category, strings.Fields(category)[0]))
		}
	}
	return nil
}

// Repository files never grant authority, even when intended to be private.
func untrustedLayer(layer string) bool { return layer == "project" || layer == "local" }

func privileged(cfg Config) []Privileged {
	var result []Privileged
	if cfg.Git.AutoCommit {
		p := cfg.Provenance["git.autoCommit"]
		result = append(result, Privileged{Key: "git.autoCommit", Trusted: !untrustedLayer(p.Layer), Origin: p})
	}
	for _, group := range []string{"hooks", "actions", "sources"} {
		count := len(cfg.Hooks)
		if group == "actions" {
			count = len(cfg.Actions)
		}
		if group == "sources" {
			count = len(cfg.Sources)
		}
		for i := 0; i < count; i++ {
			if group == "sources" {
				s := cfg.Sources[i]
				if s.Type == "native" || s.Type == "backlog-md" {
					continue
				}
			}
			key := fmt.Sprintf("%s[%d]", group, i)
			p := cfg.Provenance[key]
			result = append(result, Privileged{Key: key, Trusted: !untrustedLayer(p.Layer), Origin: p})
		}
	}
	return result
}
