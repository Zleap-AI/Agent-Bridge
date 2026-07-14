package infra

import (
	"reflect"
	"strings"
	"testing"
)

func TestPrepareWindowsCommandKeepsNativeExecutableDirect(t *testing.T) {
	args := []string{"acp", "--stdio"}
	got := prepareWindowsCommand(`C:\Program Files\Agent\agent.exe`, args, `C:\Windows\System32\cmd.exe`)

	if got.command != `C:\Program Files\Agent\agent.exe` {
		t.Fatalf("command = %q", got.command)
	}
	if !reflect.DeepEqual(got.args, args) {
		t.Fatalf("args = %#v, want %#v", got.args, args)
	}
	if got.commandLine != "" {
		t.Fatalf("native executable received cmd.exe command line %q", got.commandLine)
	}
}

func TestPrepareWindowsCommandWrapsBatchShim(t *testing.T) {
	got := prepareWindowsCommand(
		`C:\Users\Test User\AppData\Roaming\npm\codex-acp.cmd`,
		[]string{"acp", "argument with spaces"},
		`C:\Windows\System32\cmd.exe`,
	)

	if got.command != `C:\Windows\System32\cmd.exe` {
		t.Fatalf("command = %q", got.command)
	}
	if len(got.args) != 4 || !reflect.DeepEqual(got.args[:3], []string{"/d", "/s", "/c"}) {
		t.Fatalf("cmd.exe args = %#v", got.args)
	}
	if !strings.HasPrefix(got.commandLine, `"C:\Windows\System32\cmd.exe" /d /s /c "`) {
		t.Fatalf("command line does not use the expected cmd.exe boundary: %q", got.commandLine)
	}
	if !strings.Contains(got.args[3], `codex-acp.cmd`) || !strings.Contains(got.args[3], `^"argument^ with^ spaces^"`) {
		t.Fatalf("batch command or escaped argument missing from %q", got.args[3])
	}
}

func TestPrepareWindowsCommandEscapesShellMetacharacters(t *testing.T) {
	got := prepareWindowsCommand(
		`C:\Tools & Stuff\agent.cmd`,
		[]string{`value & whoami`, `100%`, `a|b`},
		"",
	)

	if got.command != "cmd.exe" {
		t.Fatalf("default command processor = %q, want cmd.exe", got.command)
	}
	for _, unsafe := range []string{` & `, `"a|b"`} {
		if strings.Contains(got.args[3], unsafe) {
			t.Fatalf("unescaped shell metacharacter %q in %q", unsafe, got.args[3])
		}
	}
	for _, escaped := range []string{`^&`, `^%`, `^|`} {
		if !strings.Contains(got.args[3], escaped) {
			t.Fatalf("escaped metacharacter %q missing from %q", escaped, got.args[3])
		}
	}
}

func TestPrepareWindowsCommandDoubleEscapesNodeModulesShimArguments(t *testing.T) {
	got := prepareWindowsCommand(
		`C:\npm\node_modules\.bin\agent.cmd`,
		[]string{`value & more`},
		"cmd.exe",
	)
	if !strings.Contains(got.args[3], `^^^&`) {
		t.Fatalf("node_modules shim argument was not double escaped: %q", got.args[3])
	}
}
