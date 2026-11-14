package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	restish "github.com/rest-sh/restish/v2"
)

// humanFormatter renders responses for people. API errors go to errOut
// (stderr), so a redirected stdout only ever holds results.
type humanFormatter struct{ errOut io.Writer }

type renderedAPIError struct{ cause error }

func (e renderedAPIError) Error() string { return e.cause.Error() }
func (e renderedAPIError) Unwrap() error { return e.cause }

func IsRenderedAPIError(err error) bool {
	var rendered renderedAPIError
	return errors.As(err, &rendered)
}

func (f humanFormatter) Format(w io.Writer, response *restish.Response, _ bool) error {
	if response == nil {
		return nil
	}
	if response.Status >= http.StatusBadRequest {
		if f.errOut != nil {
			w = f.errOut
		}
		_, err := fmt.Fprintf(
			w,
			"Error: %s (HTTP %d %s)\n",
			humanErrorMessage(response),
			response.Status,
			http.StatusText(response.Status),
		)
		if err == nil && response.Status == http.StatusUnauthorized {
			// The token was already refreshed once for this request.
			_, err = fmt.Fprintln(w, "Your session is no longer valid: run `kapten login` again.")
		}
		return err
	}
	if response.Body == nil {
		return nil
	}
	switch body := response.Body.(type) {
	case string:
		_, err := fmt.Fprintln(w, body)
		return err
	case []byte:
		_, err := w.Write(body)
		return err
	default:
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(body)
	}
}

func humanErrorMessage(response *restish.Response) string {
	if body, ok := response.Body.(map[string]any); ok {
		if message, ok := body["message"].(string); ok && strings.TrimSpace(message) != "" {
			return message
		}
	}
	if message, ok := response.Body.(string); ok && strings.TrimSpace(message) != "" {
		return strings.TrimSpace(message)
	}
	if len(response.Raw) > 0 && strings.TrimSpace(string(response.Raw)) != "" {
		return strings.TrimSpace(string(response.Raw))
	}
	if message := http.StatusText(response.Status); message != "" {
		return message
	}
	return "request failed"
}

func usesHumanOutput(args []string) bool {
	format := os.Getenv("RSH_OUTPUT_FORMAT")
	args = beforeDelimiter(args)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case (arg == "--rsh-output-format" || arg == "-o") && i+1 < len(args):
			i++
			format = args[i]
		case strings.HasPrefix(arg, "--rsh-output-format="):
			format = strings.TrimPrefix(arg, "--rsh-output-format=")
		case strings.HasPrefix(arg, "-o") && !strings.HasPrefix(arg, "--"):
			format = strings.TrimPrefix(strings.TrimPrefix(arg, "-o"), "=")
		}
	}
	return format == "kapten"
}

func withHumanOutputDefault(args []string) []string {
	if !outputOptionsSet(args) &&
		strings.TrimSpace(os.Getenv("RSH_OUTPUT_FORMAT")) == "" &&
		strings.TrimSpace(os.Getenv("RSH_FILTER")) == "" {
		args = addDefaults(args, "--rsh-output-format", "kapten")
	}
	if !optionSet(args, "--rsh-print") {
		args = addDefaults(args, "--rsh-print", "b")
	}
	return args
}
