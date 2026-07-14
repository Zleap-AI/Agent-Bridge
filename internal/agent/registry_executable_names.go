package agent

import "strings"

// executableCandidateNames expands a command using platform executable
// extensions. Unix supplies the empty extension; Windows deliberately does not,
// so npm's extensionless POSIX shim cannot mask its runnable .cmd companion.
func executableCandidateNames(name string, extensions []string) []string {
	lowerName := strings.ToLower(name)
	for _, extension := range extensions {
		extension = normalizeExecutableExtension(extension)
		if extension != "" && strings.HasSuffix(lowerName, extension) {
			return []string{name}
		}
	}

	names := make([]string, 0, len(extensions))
	seen := make(map[string]struct{}, len(extensions))
	for _, extension := range extensions {
		extension = normalizeExecutableExtension(extension)
		candidate := name + extension
		key := strings.ToLower(candidate)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, candidate)
	}
	return names
}

func normalizeExecutableExtension(extension string) string {
	extension = strings.ToLower(strings.TrimSpace(extension))
	if extension != "" && !strings.HasPrefix(extension, ".") {
		return "." + extension
	}
	return extension
}
