package cli

import "strings"

// reorderFlags moves flags in front of positionals so FlagSet.Parse accepts
// the documented form `agt approve RUN COMMAND --args-hash HASH`.
// valueFlags names flags that consume the following argument unless it is
// passed as --name=value.
func reorderFlags(args []string, valueFlags map[string]bool) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !isFlagToken(a) {
			pos = append(pos, a)
			continue
		}
		name, hasValue := flagTokenName(a)
		flags = append(flags, a)
		if !hasValue && valueFlags[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, pos...)
}

func isFlagToken(a string) bool {
	return len(a) > 1 && strings.HasPrefix(a, "-")
}

func flagTokenName(a string) (string, bool) {
	a = strings.TrimLeft(a, "-")
	if i := strings.IndexByte(a, '='); i >= 0 {
		return a[:i], true
	}
	return a, false
}
