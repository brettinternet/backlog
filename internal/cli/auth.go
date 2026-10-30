package cli

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	urfave "github.com/urfave/cli/v3"

	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source/remote"
)

func authCommands(directory *string, in io.Reader, out, errOut io.Writer) *urfave.Command {
	login := &urfave.Command{
		Name: "login", Usage: "Authorize an OAuth account from user config",
		ArgsUsage: "PROVIDER ACCOUNT",
		Flags:     []urfave.Flag{&urfave.BoolFlag{Name: "write", Usage: "Also request provider write scopes"}},
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
			manager := oauthManager(cfg)
			interaction := oauth.Interaction{In: in, Out: errOut}
			if cmd.Bool("write") {
				err = manager.EnableWrites(ctx, account, interaction)
			} else {
				err = manager.Login(ctx, account, interaction)
			}
			if err != nil {
				return err
			}
			if cmd.Bool("json") {
				return success(out, "auth login", map[string]any{"provider": account.Provider, "account": account.Name, "writeScopes": cmd.Bool("write")})
			}
			_, err = fmt.Fprintf(out, "OAuth login complete for %s account %s.\n", account.Provider, account.Name)
			return err
		},
	}
	logout := &urfave.Command{
		Name: "logout", Usage: "Delete an account's stored OAuth tokens",
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
			manager := oauthManager(cfg)
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
	status := &urfave.Command{
		Name: "status", Usage: "Show whether user config accounts can authenticate",
		ArgsUsage: "[PROVIDER [ACCOUNT]]",
		Action: func(_ context.Context, cmd *urfave.Command) error {
			if cmd.Args().Len() > 2 {
				return &Error{Reason: "usage", Message: "usage: bl auth status [PROVIDER [ACCOUNT]]"}
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			provider, name := strings.ToLower(cmd.Args().Get(0)), cmd.Args().Get(1)
			manager := oauthManager(cfg)
			accounts := []accountStatus{}
			for _, p := range slices.Sorted(maps.Keys(cfg.Accounts)) {
				for _, account := range cfg.Accounts[p] {
					if (provider == "" || provider == p) && (name == "" || name == account.Name) {
						accounts = append(accounts, accountStatusFor(manager, p, account))
					}
				}
			}
			if provider != "" && len(accounts) == 0 {
				return &Error{Reason: "not-found", Message: "user account is not configured"}
			}
			ready := !slices.ContainsFunc(accounts, func(a accountStatus) bool { return !a.Ready })
			data := map[string]any{"accounts": accounts}
			if cmd.Bool("json") {
				if !ready {
					return &Error{Reason: "rejected", Message: "some accounts can't authenticate", Data: data}
				}
				return success(out, "auth status", data)
			}
			if err := writeAccountStatus(out, accounts); err != nil {
				return err
			}
			if !ready {
				return &Error{Reason: "rejected", Message: "some accounts can't authenticate"}
			}
			return nil
		},
	}
	return &urfave.Command{
		Name: "auth", Usage: "Check and sign in to user config accounts", Category: categoryProject,
		Action:   groupHelp("use bl auth status, bl auth login, or bl auth logout"),
		Commands: []*urfave.Command{status, login, logout},
	}
}

// accountStatus is what bl can tell about an account without running helpers
// or contacting the provider. Reads still verify the principal.
type accountStatus struct {
	Provider string     `json:"provider"`
	Account  string     `json:"account"`
	User     string     `json:"user"`
	Method   string     `json:"method"`
	Ready    bool       `json:"ready"`
	State    string     `json:"state"`
	Writes   bool       `json:"writes,omitempty"`
	Expires  *time.Time `json:"expiresAt,omitempty"`
	Fix      string     `json:"fix,omitempty"`
}

func accountStatusFor(manager *oauth.Manager, provider string, account project.AccountConfig) accountStatus {
	s := accountStatus{Provider: provider, Account: account.Name, User: account.User}
	switch {
	case account.OAuth:
		s.Method = "oauth"
		host := account.Host
		if host == "" && provider == "github" {
			host = "github.com"
		}
		status := manager.Status(oauth.Account{Provider: provider, Name: account.Name, Host: host, User: account.User, OptIn: true})
		if status.Problem != nil {
			s.State, s.Fix = status.Problem.Code, strings.ReplaceAll(status.Problem.Message, "PROVIDER ACCOUNT", provider+" "+account.Name)
			return s
		}
		s.Ready, s.Writes, s.State = true, status.Writes, "signed in, read"
		if status.Writes {
			s.State += "+write"
		}
		if !status.Expires.IsZero() {
			s.Expires = &status.Expires
			s.State += ", expires " + status.Expires.UTC().Format(time.RFC3339)
		}
	case len(account.Credential) > 0:
		s.Method = "credential"
		if _, err := exec.LookPath(account.Credential[0]); err != nil {
			s.State, s.Fix = "helper not found", "Install the credential helper or correct its command in user config"
			return s
		}
		s.Ready, s.State = true, "helper found, not run"
	case account.Env != "":
		s.Method = "env"
		if os.Getenv(account.Env) == "" {
			s.State, s.Fix = account.Env+" unset", "Export "+account.Env
			return s
		}
		s.Ready, s.State = true, account.Env+" set"
	default:
		s.Method, s.State, s.Fix = "none", "no credential", "Set one of credential, env, or oauth: true in user config"
	}
	return s
}

func oauthManager(cfg project.Config) *oauth.Manager {
	return oauth.NewManager(oauth.Store{StateDir: cfg.Paths.StateDir}, cfg.Paths.CacheDir, oauth.DefaultRegistry(), remote.ProcessSecrets)
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
