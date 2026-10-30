package project

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// CanonicalJiraSite is the HTTPS Atlassian Cloud origin of a Jira site, or "".
func CanonicalJiraSite(site string) string {
	u, err := url.Parse(site)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".atlassian.net") {
		return ""
	}
	return "https://" + strings.ToLower(u.Hostname())
}

// SourceAccount is the user account a provider source authenticates as. Its
// User is the provider's own form of an assignee: GitHub login, GitLab
// username, Linear viewer UUID, or Jira accountId.
func (cfg Config) SourceAccount(c SourceConfig) (AccountConfig, error) {
	label := map[string]string{"github": "GitHub", "gitlab": "GitLab", "linear": "Linear", "jira": "Jira"}[c.Type]
	if label == "" {
		return AccountConfig{}, errors.New("source " + c.ID + " has no provider account")
	}
	host := c.Host
	switch c.Type {
	case "github":
		if host == "" {
			host = "github.com"
		}
	case "gitlab":
		if host == "" {
			host = "gitlab.com"
		}
	case "jira":
		host = CanonicalJiraSite(c.Site)
	}
	var account *AccountConfig
	for _, candidate := range cfg.Accounts[c.Type] {
		if candidate.Name != c.Account && candidate.User != c.Account {
			continue
		}
		if (c.Type == "github" || c.Type == "gitlab") && candidate.Host != host || c.Type == "jira" && (host == "" || CanonicalJiraSite(candidate.Host) != host) {
			continue
		}
		if account != nil {
			return AccountConfig{}, errors.New("ambiguous " + label + " account")
		}
		copy := candidate
		account = &copy
	}
	if account == nil {
		return AccountConfig{}, errors.New(label + " user account not configured")
	}
	if c.Type != "jira" {
		account.Provider = c.Type
	}
	return *account, nil
}

// Identities maps each source ID to the caller's assignee value there: the
// source account's user for providers, else me.default. A source without one
// is absent, so nothing in it is yours.
func (cfg Config) Identities() map[string]string {
	out := map[string]string{}
	for _, c := range cfg.Sources {
		me := cfg.Me.Default
		switch c.Type {
		case "github", "linear", "jira":
			account, err := cfg.SourceAccount(c)
			if err != nil {
				continue
			}
			me = account.User
		case "github-pulls":
			account, err := cfg.ForgeAccount(c.PullsForge())
			if err != nil {
				continue
			}
			me = account.User
		}
		if me != "" {
			out[c.ID] = me
		}
	}
	return out
}

// ForgeAccount is the account a forge reads through, as does a github-pulls
// source: the named account, else that of a GitHub source reading the same
// repository, else the only account of the forge's type for its host.
func (cfg Config) ForgeAccount(f ForgeConfig) (AccountConfig, error) {
	if f.Account != "" {
		return cfg.SourceAccount(SourceConfig{ID: "forge", Type: f.Type, Host: f.Host, Account: f.Account})
	}
	for _, s := range cfg.Sources {
		host := s.Host
		if host == "" {
			host = "github.com"
		}
		if f.Type == "github" && s.Type == "github" && host == f.Host && strings.EqualFold(s.Repo, f.Repo) {
			return cfg.SourceAccount(s)
		}
	}
	found := cfg.HostAccounts(f.Type, f.Host)
	if len(found) != 1 {
		return AccountConfig{}, fmt.Errorf("name one of the %s accounts for %s", map[string]string{"github": "GitHub", "gitlab": "GitLab"}[f.Type], f.Host)
	}
	found[0].Provider = f.Type
	return found[0], nil
}

// PullsForge is the forge a github-pulls source reads.
func (c SourceConfig) PullsForge() ForgeConfig {
	host := c.Host
	if host == "" {
		host = "github.com"
	}
	return ForgeConfig{Type: "github", Host: host, Repo: c.Repo, Account: c.Account}
}

// HostAccounts are cfg's accounts of kind for host.
func (cfg Config) HostAccounts(kind, host string) []AccountConfig {
	var out []AccountConfig
	for _, a := range cfg.Accounts[kind] {
		if a.Host == host {
			out = append(out, a)
		}
	}
	return out
}
