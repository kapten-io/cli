package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/kapten-io/cli/internal/contextstore"
	"github.com/kapten-io/cli/internal/credentialstore"
	"github.com/kapten-io/cli/internal/spec"
	restish "github.com/rest-sh/restish/v2"
	"github.com/rest-sh/restish/v2/config"
)

// resourceContext is the context resource commands run in: the one named by
// --context, else the environment's OAuth application when KAPTEN_CLIENT_ID
// is set, else the active saved login.
func resourceContext(name string) (contextstore.Context, error) {
	if name == "" {
		creds, err := envCredentials()
		if err != nil {
			return contextstore.Context{}, err
		}
		if creds != nil {
			return contextstore.Context{Name: "environment", Issuer: creds.issuer, APIURL: env("KAPTEN_API_URL", defaultAPIURL)}, nil
		}
	}

	return selectedContext(name)
}

func selectedContext(name string) (contextstore.Context, error) {
	c, err := contextstore.Load()
	if err != nil {
		return contextstore.Context{}, err
	}
	if name == "" {
		name = c.Active
	}
	if name == "" {
		return contextstore.Context{}, errors.New("no active Kapten context; run `kapten login` first, or set KAPTEN_CLIENT_ID and KAPTEN_CLIENT_SECRET")
	}
	selected, ok := c.Find(name)
	if !ok {
		return contextstore.Context{}, fmt.Errorf("Kapten context %q was not found; run `kapten auth list`", name)
	}
	return selected, nil
}

// Both resource commands and MCP delegation use the same auth/request runtime.
func (a *App) newResourceRuntime(ctx context.Context, selected contextstore.Context) (*restish.CLI, error) {
	path, err := spec.Path()
	if err != nil {
		return nil, err
	}
	cfg := &restish.Config{APIs: map[string]*restish.APIConfig{"kapten": {
		BaseURL: selected.APIURL, SpecFiles: []string{path}, CommandLayout: "tags",
		Profiles: map[string]*config.ProfileConfig{"default": {Auth: &config.AuthConfig{Type: "kapten"}}},
	}}}
	cli := restish.New()
	cli.Stdin, cli.Stdout, cli.Stderr = a.In, a.Out, a.Err
	cli.AddAuthHandler("kapten", &contextAuth{app: a, selected: selected, store: credentialstore.New("kapten", contextstore.CredentialsPath()), runCtx: ctx})
	configPath, err := ensureRestishConfig()
	if err != nil {
		return nil, err
	}
	cli.Paths = config.NewPathsWithConfigFile(configPath)
	cli.SetCommandName("kapten")
	cli.SetCommandDescription("Kapten CLI", "Manage Kapten resources from your terminal.")
	cli.AddFormatter("kapten", humanFormatter{errOut: a.Err})
	cli.SetDefaultConfig(cfg)
	cli.SetCommandSurface(restish.CommandSurface{PromotedAPI: "kapten", HideSupportCommands: true})
	return cli, nil
}
