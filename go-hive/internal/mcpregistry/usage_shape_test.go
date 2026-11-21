package mcpregistry

// Parity: core/framework/loader/mcp_registry_cli.py::register_mcp_commands
//
// The `hive mcp` group is an argparse tree (subparsers dest="mcp_command",
// required=True), measured against the Python side at COLUMNS=100. A parse
// failure prints the responsible page's usage block, then
// "<prog>: error: <message>", on stderr with exit 2:
//
//   * the group's required subcommand wins over a leading unknown flag, and
//     prints the GROUP page;
//   * an unknown flag on a verb, or a positional the verb has no slot for, is
//     an argparse extra: the ROOT page reports it (supplied by the caller);
//   * a missing required verb argument prints the VERB's page.

import (
	"bytes"
	"strings"
	"testing"
)

const (
	testRootPage = "usage: hive [-h] [--version] [--model MODEL]\n" +
		"            {serve,open,queen,colony,session,chat,skill,debugger,mcp,janitor} ...\n"
	testGroupUsage = "usage: hive mcp [-h]\n" +
		"                {install,add,mode,run,remove,enable,disable,list,info,config,search,health,init,update}\n" +
		"                ...\n"
)

func newShapeCLI(t *testing.T) (*CLI, *bytes.Buffer) {
	t.Helper()
	t.Setenv("COLUMNS", "100")
	stderr := &bytes.Buffer{}
	cli := &CLI{
		Out: &bytes.Buffer{}, Err: stderr, In: strings.NewReader(""),
		RootProg:  "hive",
		RootUsage: func() string { return strings.TrimRight(testRootPage, "\n") },
	}
	cli.RegistryFunc = func() (*Registry, error) { return newDefaultRegistry(t), nil }
	return cli, stderr
}

// Parity: core/framework/loader/mcp_registry_cli.py::register_mcp_commands
func TestMCPUsageErrorShape(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{
			name: "group unknown flag with no verb",
			argv: []string{"--bogus"},
			want: testGroupUsage + "hive mcp: error: the following arguments are required: mcp_command\n",
		},
		{
			name: "group bare",
			argv: nil,
			want: testGroupUsage + "hive mcp: error: the following arguments are required: mcp_command\n",
		},
		{
			name: "verb unknown flag prints the root page",
			argv: []string{"list", "--bogus"},
			want: testRootPage + "hive: error: unrecognized arguments: --bogus\n",
		},
		{
			name: "verb extra positional prints the root page",
			argv: []string{"install", "a", "b"},
			want: testRootPage + "hive: error: unrecognized arguments: b\n",
		},
		{
			name: "verb missing required argument prints the verb page",
			argv: []string{"info", "--bogus"},
			want: "usage: hive mcp info [-h] [--json] name\n" +
				"hive mcp info: error: the following arguments are required: name\n",
		},
		{
			name: "partially given positionals list only the missing one",
			argv: []string{"mode", "a"},
			want: "usage: hive mcp mode [-h] name {cli,tools}\n" +
				"hive mcp mode: error: the following arguments are required: mode\n",
		},
		{
			name: "group invalid choice",
			argv: []string{"nosuchverb"},
			want: testGroupUsage +
				"hive mcp: error: argument mcp_command: invalid choice: 'nosuchverb' " +
				"(choose from 'install', 'add', 'mode', 'run', 'remove', 'enable', 'disable', 'list', " +
				"'info', 'config', 'search', 'health', 'init', 'update')\n",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cli, stderr := newShapeCLI(t)
			if code := cli.Main(testCase.argv); code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr %q)", code, stderr.String())
			}
			if stderr.String() != testCase.want {
				t.Fatalf("stderr = %q, want %q", stderr.String(), testCase.want)
			}
		})
	}
}

// Parity-exempt: no upstream test drives an mcp verb's help through a parse
// failure; measured against register_mcp_commands' argparse tree.
func TestMCPVerbVerifyOverGroupPage(t *testing.T) {
	cli, stderr := newShapeCLI(t)
	if code := cli.Main([]string{"run"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	want := "usage: hive mcp run [-h] name ...\n" +
		"hive mcp run: error: the following arguments are required: name, mcp2cli_args\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}
