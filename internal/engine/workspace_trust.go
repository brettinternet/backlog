package engine

import (
	"fmt"
	"github.com/brettinternet/backlog/internal/project"
)

func workspaceSourceTrusted(cfg project.Config, id string) bool {
	p, local := cfg.SourceProject(id)
	for n, s := range p.Sources {
		if s.ID == local {
			return p.RequireTrusted(fmt.Sprintf("sources[%d]", n)) == nil
		}
	}
	return false
}
