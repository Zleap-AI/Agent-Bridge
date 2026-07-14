package infra

import (
	"path/filepath"
	"strings"
)

type windowsCommandSpec struct {
	command     string
	args        []string
	commandLine string
}

func prepareWindowsCommand(command string, args []string, commandProcessor string) windowsCommandSpec {
	if !isWindowsBatchFile(command) {
		return windowsCommandSpec{command: command, args: append([]string(nil), args...)}
	}
	if strings.TrimSpace(commandProcessor) == "" {
		commandProcessor = "cmd.exe"
	}

	doubleEscape := isNodeModulesBinShim(command)
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, escapeWindowsBatchCommand(command))
	for _, arg := range args {
		parts = append(parts, escapeWindowsBatchArgument(arg, doubleEscape))
	}

	shellCommand := `"` + strings.Join(parts, " ") + `"`
	cmdArgs := []string{"/d", "/s", "/c", shellCommand}
	return windowsCommandSpec{
		command:     commandProcessor,
		args:        cmdArgs,
		commandLine: quoteWindowsProgram(commandProcessor) + " " + strings.Join(cmdArgs, " "),
	}
}

func isWindowsBatchFile(command string) bool {
	ext := strings.ToLower(filepath.Ext(command))
	return ext == ".cmd" || ext == ".bat"
}

func isNodeModulesBinShim(command string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(command, "/", `\`))
	return strings.Contains(normalized, `node_modules\.bin\`)
}

func quoteWindowsProgram(command string) string {
	return `"` + command + `"`
}

func escapeWindowsBatchCommand(command string) string {
	return escapeWindowsCmdMetacharacters(command)
}

// escapeWindowsBatchArgument follows the quoting used by npm's cross-spawn:
// quote for the batch parser, preserve backslashes before quotes, then escape
// cmd.exe metacharacters. node_modules/.bin shims require a second escape pass
// because their generated batch file parses forwarded arguments again.
func escapeWindowsBatchArgument(arg string, doubleEscapeMetacharacters bool) string {
	var quoted strings.Builder
	quoted.Grow(len(arg) + 2)
	quoted.WriteByte('"')

	backslashes := 0
	for _, char := range arg {
		switch char {
		case '\\':
			backslashes++
		case '"':
			quoted.WriteString(strings.Repeat(`\`, backslashes*2+1))
			quoted.WriteRune(char)
			backslashes = 0
		default:
			quoted.WriteString(strings.Repeat(`\`, backslashes))
			quoted.WriteRune(char)
			backslashes = 0
		}
	}
	quoted.WriteString(strings.Repeat(`\`, backslashes*2))
	quoted.WriteByte('"')

	escaped := escapeWindowsCmdMetacharacters(quoted.String())
	if doubleEscapeMetacharacters {
		escaped = escapeWindowsCmdMetacharacters(escaped)
	}
	return escaped
}

func escapeWindowsCmdMetacharacters(value string) string {
	const metacharacters = `()[]%!^"` + "`" + `<>&|;, *?`
	var escaped strings.Builder
	escaped.Grow(len(value))
	for _, char := range value {
		if strings.ContainsRune(metacharacters, char) {
			escaped.WriteByte('^')
		}
		escaped.WriteRune(char)
	}
	return escaped.String()
}
