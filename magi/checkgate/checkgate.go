// Package checkgate keeps the check command of a code change out of reach of
// the apprentice. See the notes below.
package checkgate

import (
	"path"
	"sort"
	"strconv"
	"strings"
)

// The check command comes from Magi configuration (MAGI_CHECK_COMMAND), but
// its default, ./tools/check.sh, is a file of the repository, and the
// apprentice can write to the working copy. Without a guard the apprentice
// would edit the script that gates its own work, and both the finish gate of
// the apprentice and the check step would run the edited script.
//
// Guarded wraps the command so that it refuses to run when the script
// that the command names differs from the base commit (changed, staged,
// deleted, or an untracked or ignored file in its place). The refusal exit
// code is ExitCode. A person who has reviewed the change to the script
// approves it with the run variable check_script_approved=true; only Magi
// sets run variables, the apprentice cannot.
//
// Limit: the guard covers the script file the command names. Files that the
// script calls (for example the Go tests it runs) are ordinary code of the
// change and are reviewed in the diff.

// ExitCode is the exit code of a refused check.
const ExitCode = 97

// ApprovedVar is the run variable that lets a changed script run.
const ApprovedVar = "check_script_approved"

// ScriptPath gives the repository path of the script that the command
// runs, or "" when the command names no file of the repository (for example
// "go test ./...", or an absolute path outside the repository).
func ScriptPath(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return ""
	}
	p := fields[0]
	if strings.ContainsAny(p, "$`\"'\\;&|<>()*?[]{}~") || path.IsAbs(p) || !strings.Contains(p, "/") {
		return ""
	}
	p = path.Clean(p)
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}

// Guarded returns the shell script that the check runs in the work
// directory. base is the commit that the work started from.
func Guarded(command, base string, approved bool) string {
	p := ScriptPath(command)
	if p == "" || approved {
		return command
	}
	if base == "" {
		base = "HEAD"
	}
	// Any failure of git counts as a change: the guard fails closed.
	return `p=` + quote(p) + `; b=` + quote(base) + `
if ! git diff --quiet "$b" -- "$p" 2>/dev/null || [ -n "$(git status --porcelain --ignored -- "$p" 2>&1)" ]; then
  echo "CHECK SCRIPT CHANGED: $p differs from the base commit. It is not run. A person must approve the change to it." >&2
  exit ` + strconv.Itoa(ExitCode) + `
fi
` + command
}

// Exports gives the shell lines that set the placeholders for one
// script. Values are placeholders, not secrets.
func Exports(vars map[string]string) string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString("export " + k + "=" + quote(vars[k]) + "\n")
	}
	return b.String()
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
