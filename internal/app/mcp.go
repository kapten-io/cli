package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/kapten-io/cli/internal/contextstore"
	"github.com/kapten-io/cli/internal/credentialstore"
	"github.com/kapten-io/cli/internal/mcphost"
	restish "github.com/rest-sh/restish/v2"
	"github.com/rest-sh/restish/v2/plugin"
	"github.com/spf13/cobra"
)

const defaultMCPTimeout = 30
const defaultMCPResultLimit = 1 << 20

func (a *App) mcpContext(ctx context.Context, args []string, name string) error {
	root := a.mcpCommand(name)
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// mcpCommand is the tree of the mcp commands, for the context named by
// --context (empty: the environment's or the active one).
func (a *App) mcpCommand(name string) *cobra.Command {
	root := &cobra.Command{Use: "kapten", Annotations: map[string]string{cobra.CommandDisplayNameAnnotation: "kapten mcp"},
		Short: "Serve Kapten resource operations over local stdio MCP", SilenceErrors: true, SilenceUsage: true}
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	var operations string
	timeout, resultLimit := defaultMCPTimeout, defaultMCPResultLimit
	serve := &cobra.Command{Use: "serve", Short: "Serve resource tools using the selected authentication context", Args: cobra.NoArgs,
		Long: "Serve CLI-exposed Kapten resource operations, including writes, over stdio.\nAuthenticate with `kapten login` first. The selected context stays fixed until restart.\nDELETE tools are annotated as destructive. No remote listener or interactive login is available.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if timeout < 1 || timeout > 300 {
				return errors.New("--request-timeout must be between 1 and 300 seconds")
			}
			if resultLimit < 1024 || resultLimit > 8<<20 {
				return errors.New("--max-result-bytes must be between 1024 and 8388608")
			}
			var only []string
			if cmd.Flags().Changed("operations") {
				only = strings.Split(operations, ",")
			}
			contract, err := newMCPContract(only)
			if err != nil {
				return err
			}
			selected, err := resourceContext(name)
			if err != nil {
				return err
			}
			u, err := url.Parse(selected.APIURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return errors.New("selected context has an invalid API URL; run `kapten login` again")
			}
			if selected.CredentialKey != "" {
				store := credentialstore.New("kapten", contextstore.CredentialsPath())
				token, backend, err := store.Get(selected.CredentialKey)
				if err != nil || (token.AccessToken == "" && token.RefreshToken == "") {
					return errors.New("selected context has no usable credentials; run `kapten login` first")
				}
				if backend != selected.Storage {
					a.warnStorage(backend)
				}
			}
			helper, err := a.mcpExecutable()
			if err != nil {
				return err
			}
			ids := make([]string, 0, len(contract.operations))
			for id := range contract.operations {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			in := a.In
			if in == nil {
				in = strings.NewReader("")
			}
			output := newMCPFrameWriter(a.Out)
			output.annotations = contract.annotations
			serverApp := *a
			serverApp.Err = &mcpDiagnosticWriter{out: a.Err}
			err = mcphost.Serve(cmd.Context(), mcphost.Config{
				Executable: helper, In: newMCPLineReader(in), Out: output, Err: serverApp.Err, Spec: contract.raw, Operations: ids,
				RequestTimeout: timeout, MaxResultBytes: resultLimit,
				Execute: func(ctx context.Context, msg plugin.HTTPRequestMsg) plugin.HTTPResponseMsg {
					return serverApp.executeMCPRequest(ctx, selected, contract, msg, timeout)
				},
			})
			if err != nil {
				return err
			}
			return output.finish()
		}}
	serve.Flags().StringVar(&operations, "operations", "", "Comma-separated operationId subset of the exposed resource tools")
	serve.Flags().IntVar(&timeout, "request-timeout", defaultMCPTimeout, "Per-tool HTTP timeout in seconds (1–300)")
	serve.Flags().IntVar(&resultLimit, "max-result-bytes", defaultMCPResultLimit, "Maximum tool result payload in bytes (1024–8388608)")
	root.AddCommand(serve)
	return root
}

func (a *App) mcpExecutable() (string, error) {
	path := a.MCPExecutable
	if path == "" {
		executable, err := os.Executable()
		if err != nil {
			return "", errors.New("locate Kapten executable for MCP")
		}
		executable, err = filepath.EvalSymlinks(executable)
		if err != nil {
			return "", errors.New("resolve Kapten executable for MCP")
		}
		name := "restish-mcp"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path = filepath.Join(filepath.Dir(executable), name)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("invalid MCP helper path")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return "", errors.New("MCP helper is missing or not executable; install the complete Kapten release or run `mise run build`")
	}
	return path, nil
}

func (a *App) executeMCPRequest(ctx context.Context, selected contextstore.Context, contract *mcpContract, msg plugin.HTTPRequestMsg, timeout int) plugin.HTTPResponseMsg {
	response := plugin.HTTPResponseMsg{Type: plugin.MsgTypeHTTPResponse, RequestID: msg.RequestID}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	body, err := contract.validate(ctx, msg)
	if err != nil {
		response.Error = err.Error()
		return response
	}
	cli, err := a.newResourceRuntime(ctx, selected)
	if err != nil {
		response.Error = "initialize Kapten request runtime failed"
		return response
	}
	// Generic HTTP commands are available only inside this guarded delegate,
	// never on Kapten's public command surface. Keep the normal Restish auth,
	// refresh and normalization path, but disable cache/pagination for MCP.
	cli.SetCommandSurface(restish.CommandSurface{})
	output := &mcpOutputBuffer{limit: 12 << 20}
	cli.Stdin, cli.Stdout = bytes.NewReader(body), output
	args := []string{"kapten", strings.ToLower(msg.Method), msg.URI,
		"--rsh-profile=default", "--rsh-no-cache", "--rsh-no-paginate", "--rsh-retry=0",
		"--rsh-timeout=" + fmt.Sprint(timeout) + "s", "--rsh-max-body-size=8",
		"--rsh-output-format=json", "--rsh-filter=."}
	if len(body) > 0 {
		args = append(args, "--rsh-content-type=application/json")
	}
	for header, value := range msg.Headers {
		args = append(args, "--rsh-header", header+": "+value)
	}
	runErr := cli.Run(args)
	var rendered struct {
		Status  int                 `json:"status"`
		Headers map[string][]string `json:"headers_all"`
		Body    any                 `json:"body"`
		Links   map[string]any      `json:"links"`
	}
	if err := json.Unmarshal(output.Bytes(), &rendered); err != nil || rendered.Status == 0 {
		if ctx.Err() != nil {
			response.Error = "MCP API request cancelled or timed out"
		} else {
			response.Error = "MCP API request failed; check credentials and API connectivity"
		}
		return response
	}
	if runErr != nil && restishExitDescription(runErr) == "" {
		response.Error = "MCP API request failed"
		return response
	}
	for header := range rendered.Headers {
		if strings.EqualFold(header, "Set-Cookie") || strings.EqualFold(header, "Authorization") || strings.EqualFold(header, "Proxy-Authorization") {
			delete(rendered.Headers, header)
		}
	}
	response.Status, response.Headers, response.Body, response.Links = rendered.Status, rendered.Headers, rendered.Body, rendered.Links
	return response
}

var _ io.Writer = (*mcpOutputBuffer)(nil)
