package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	urfave "github.com/urfave/cli/v3"

	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source/remote"
)

func authCommands(directory *string, in io.Reader, out, errOut io.Writer) *urfave.Command {
	login := &urfave.Command{
		Name: "login", Usage: "Interactively authorize a user-configured OAuth account",
		ArgsUsage: "PROVIDER ACCOUNT",
		Flags:     []urfave.Flag{&urfave.BoolFlag{Name: "write", Usage: "explicitly request provider write scopes after read-only login"}},
		Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Args().Len() != 2 {
				return &Error{Reason: "usage", Message: "usage: bl auth login PROVIDER ACCOUNT [--write]"}
			}
			if !terminal(in) || !terminal(out) {
				return oauth.Failure(oauth.CodeInteractionRequired, time.Time{})
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			account, err := configuredOAuthAccount(cfg, cmd.Args().Get(0), cmd.Args().Get(1))
			if err != nil {
				return err
			}
			manager := oauth.NewManager(oauth.Store{StateDir: cfg.Paths.StateDir}, cfg.Paths.CacheDir, oauth.DefaultRegistry(), remote.ProcessSecrets)
			interaction := oauth.Interaction{In: in, Out: errOut}
			if err := manager.Login(ctx, account, interaction); err != nil {
				return err
			}
			if cmd.Bool("write") {
				if err := manager.EnableWrites(ctx, account, interaction); err != nil {
					return err
				}
			}
			if cmd.Bool("json") {
				return success(out, "auth login", map[string]any{"provider": account.Provider, "account": account.Name, "writeScopes": cmd.Bool("write")})
			}
			_, err = fmt.Fprintf(out, "OAuth login complete for %s account %s.\n", account.Provider, account.Name)
			return err
		},
	}
	logout := &urfave.Command{
		Name: "logout", Usage: "Delete a user account's stored OAuth tokens",
		ArgsUsage: "PROVIDER ACCOUNT",
		Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Args().Len() != 2 {
				return &Error{Reason: "usage", Message: "usage: bl auth logout PROVIDER ACCOUNT"}
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			account, err := configuredOAuthAccount(cfg, cmd.Args().Get(0), cmd.Args().Get(1))
			if err != nil {
				return err
			}
			manager := oauth.NewManager(oauth.Store{StateDir: cfg.Paths.StateDir}, cfg.Paths.CacheDir, oauth.DefaultRegistry(), remote.ProcessSecrets)
			result, err := manager.Logout(ctx, account)
			if err != nil {
				return err
			}
			if cmd.Bool("json") {
				return success(out, "auth logout", map[string]any{"provider": account.Provider, "account": account.Name, "remoteRevoked": result.RemoteRevoked, "manualRevocation": result.ManualRevocation, "warning": result.Warning})
			}
			message := "OAuth tokens deleted and cached rows purged."
			if result.ManualRevocation {
				message += " Revoke the provider grant in its account settings."
			}
			if result.Warning != nil {
				message += " " + result.Warning.Message
			}
			_, err = fmt.Fprintln(out, message)
			return err
		},
	}
	return &urfave.Command{
		Name: "auth", Usage: "Manage explicitly opted-in user OAuth accounts",
		Action: func(_ context.Context, _ *urfave.Command) error {
			return &Error{Reason: "usage", Message: "use bl auth login or bl auth logout"}
		},
		Commands: []*urfave.Command{login, logout},
	}
}

func configuredOAuthAccount(cfg project.Config, provider, name string) (oauth.Account, error) {
	provider = strings.ToLower(provider)
	if provider == "" || name == "" {
		return oauth.Account{}, &Error{Reason: "usage", Message: "provider and account name are required"}
	}
	var selected *project.AccountConfig
	for _, candidate := range cfg.Accounts[provider] {
		if candidate.Name != name {
			continue
		}
		if selected != nil {
			return oauth.Account{}, &Error{Reason: "usage", Message: "ambiguous user account name"}
		}
		copy := candidate
		selected = &copy
	}
	if selected == nil {
		return oauth.Account{}, &Error{Reason: "not-found", Message: "user account is not configured"}
	}
	host := selected.Host
	if host == "" && provider == "github" {
		host = "github.com"
	}
	return oauth.Account{
		Provider: provider, Name: selected.Name, Host: host, User: selected.User,
		OptIn: selected.OAuth, HasLegacyCredential: len(selected.Credential) > 0 || selected.Env != "",
	}, nil
}
