package app

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// Pure unit tests are restricted to argument parsing and output-option handling.
func TestOwnedContextBoundary(t *testing.T) {
	for _, test := range []struct {
		args []string
		name string
		rest []string
		fail bool
	}{
		{[]string{"--context", "one", "projects", "list", "-ojson"}, "one", []string{"projects", "list", "-ojson"}, false},
		{[]string{"projects", "get", "--", "--context", "literal"}, "", []string{"projects", "get", "--", "--context", "literal"}, false},
		{[]string{"--context=one", "--context=two", "projects", "get", "-fexpr"}, "two", []string{"projects", "get", "-fexpr"}, false},
		{[]string{"--context"}, "", nil, true},
		{[]string{"--context", "--help"}, "", nil, true},
	} {
		opts, rest, err := parseKaptenArgs(test.args)
		if (err != nil) != test.fail || opts.Context != test.name || !reflect.DeepEqual(rest, test.rest) {
			t.Fatalf("parse %q: name=%q rest=%q err=%v", test.args, opts.Context, rest, err)
		}
	}
}

func TestOutputShorthandAndDelimiter(t *testing.T) {
	t.Setenv("RSH_OUTPUT_FORMAT", "")
	t.Setenv("RSH_FILTER", "")
	for _, flag := range []string{"-ojson", "-o=json", "-fexpr", "--rsh-output-format=json", "--rsh-filter=expr", "--rsh-status", "--rsh-headers", "--rsh-silent", "--rsh-status=true", "--rsh-headers=false", "--rsh-silent=true"} {
		args := []string{"projects", "list", flag}
		got := withHumanOutputDefault(withPaginatedOutputDefaults(args))
		if optionSet(got, "--rsh-output-format") && flag != "--rsh-output-format=json" {
			t.Fatalf("overrode %s: %q", flag, got)
		}
		if usesHumanOutput(got) {
			t.Fatalf("human format overrode %s", flag)
		}
	}
	args := []string{"projects", "get", "--", "--rsh-output-format=json", "--rsh-print=x"}
	got := withHumanOutputDefault(args)
	want := []string{"projects", "get", "--rsh-output-format", "kapten", "--rsh-print", "b", "--", "--rsh-output-format=json", "--rsh-print=x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults crossed --: %q", got)
	}
	if !usesHumanOutput(got) {
		t.Fatal("positional output option changed effective format")
	}
	if usesHumanOutput([]string{"-okapten", "-ojson"}) {
		t.Fatal("did not honor last override")
	}
}

func TestAuthCobraValidation(t *testing.T) {
	t.Setenv("KAPTEN_CONFIG_DIR", t.TempDir())
	for _, args := range [][]string{
		{"auth", "login", "--unknown"}, {"auth", "login", "--name"},
		{"auth", "list", "--output=xml"}, {"auth", "use"},
		{"auth", "logout", "one", "two"}, {"auth", "logout", "one", "--all"},
		{"auth", "unknown"}, {"--version=garbage"}, {"--help=garbage"},
	} {
		if err := New(&bytes.Buffer{}, &bytes.Buffer{}).Run(append([]string{"kapten"}, args...)); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	for _, args := range [][]string{
		{"auth", "login", "--help"}, {"auth", "list", "-h"}, {"--context=missing", "auth", "logout", "--help"}, {"--help", "auth"}, {"--version", "auth", "--help"}, {"auth", "login", "--version", "--help"},
	} {
		var out bytes.Buffer
		if err := New(&out, &bytes.Buffer{}).Run(append([]string{"kapten"}, args...)); err != nil {
			t.Fatalf("%q: %v", args, err)
		}
		if !strings.Contains(out.String(), "Usage:") {
			t.Fatalf("help not captured for %q: %s", args, out.String())
		}
	}
}

func TestWithPaginatedOutputDefaults(t *testing.T) {
	t.Setenv("RSH_OUTPUT_FORMAT", "")
	t.Setenv("RSH_FILTER", "")
	t.Setenv("RSH_COLUMNS", "")

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "list command",
			args: []string{"projects", "list"},
			want: []string{"projects", "list", "--rsh-filter", paginatedRecordsFilter, "--rsh-output-format", "table", "--rsh-columns", "name,id,description"},
		},
		{
			name: "cluster list",
			args: []string{"clusters", "list"},
			want: []string{"clusters", "list", "--rsh-filter", paginatedRecordsFilter, "--rsh-output-format", "table", "--rsh-columns", "name,id,cloud_provider,state"},
		},
		{
			name: "list subresource command",
			args: []string{"applications", "list-runs", "project", "environment", "application"},
			want: []string{"applications", "list-runs", "project", "environment", "application", "--rsh-filter", paginatedRecordsFilter, "--rsh-output-format", "table", "--rsh-columns", "component_name,id,status,action,trigger,started_at,finished_at,message"},
		},
		{
			name: "explicit columns override defaults",
			args: []string{"projects", "list", "--rsh-columns=name,id"},
			want: []string{"projects", "list", "--rsh-columns=name,id", "--rsh-filter", paginatedRecordsFilter, "--rsh-output-format", "table"},
		},
		{
			name: "explicit JSON preserves response envelope",
			args: []string{"projects", "list", "-o", "json"},
			want: []string{"projects", "list", "-o", "json"},
		},
		{
			name: "explicit filter is preserved",
			args: []string{"projects", "list", "--rsh-filter=body.records.{id,name}"},
			want: []string{"projects", "list", "--rsh-filter=body.records.{id,name}"},
		},
		{
			name: "non-paginated command",
			args: []string{"projects", "get", "project-id"},
			want: []string{"projects", "get", "project-id"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := withPaginatedOutputDefaults(append([]string(nil), test.args...))
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("withPaginatedOutputDefaults() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestWithPaginatedOutputDefaultsHonorsEnvironment(t *testing.T) {
	t.Setenv("RSH_OUTPUT_FORMAT", "yaml")
	t.Setenv("RSH_FILTER", "")
	args := []string{"projects", "list"}

	if got := withPaginatedOutputDefaults(args); !reflect.DeepEqual(got, args) {
		t.Fatalf("withPaginatedOutputDefaults() = %#v, want %#v", got, args)
	}
}

func TestWithHumanOutputDefault(t *testing.T) {
	t.Setenv("RSH_OUTPUT_FORMAT", "")
	t.Setenv("RSH_FILTER", "")
	t.Setenv("RSH_PRINT", "hb")

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "human output by default",
			args: []string{"projects", "create", "name: test, description: test"},
			want: []string{"projects", "create", "name: test, description: test", "--rsh-output-format", "kapten", "--rsh-print", "b"},
		},
		{
			name: "explicit kapten format",
			args: []string{"projects", "create", "body", "--rsh-output-format=kapten"},
			want: []string{"projects", "create", "body", "--rsh-output-format=kapten", "--rsh-print", "b"},
		},
		{
			name: "explicit JSON",
			args: []string{"projects", "create", "body", "-o", "json"},
			want: []string{"projects", "create", "body", "-o", "json", "--rsh-print", "b"},
		},
		{
			name: "explicit print controls",
			args: []string{"projects", "create", "body", "--rsh-print=hb"},
			want: []string{"projects", "create", "body", "--rsh-print=hb", "--rsh-output-format", "kapten"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := withHumanOutputDefault(test.args); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("withHumanOutputDefault() = %#v, want %#v", got, test.want)
			}
		})
	}
}
