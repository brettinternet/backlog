package cli

import (
	"github.com/brettinternet/backlog/internal/project"
	urfave "github.com/urfave/cli/v3"
)

func loadBrowseConfig(cmd *urfave.Command, directory string, implicit bool) (project.Config, error) {
	cfg, err := loadConfig(cmd, directory)
	if err != nil || !implicit && cmd.String("workspace") == "" {
		return cfg, err
	}
	var contrast *bool
	if cmd.IsSet("high-contrast") {
		value := cmd.Bool("high-contrast")
		contrast = &value
	}
	cfg, err = project.OpenWorkspace(cfg, cmd.String("workspace"), contrast)
	if err != nil {
		return cfg, &Error{Reason: "invalid-config", Message: err.Error()}
	}
	return cfg, nil
}
