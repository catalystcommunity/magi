package runner

import (
	"encoding/json"
	"os"
)

// storedPlaceholders reads the placeholder variables of the environment from
// its access file (they are not in the sandbox environment; see startBox).
// A step that needs one adds it to its own script (checkgate.Exports).
func (r *Runner) storedPlaceholders(env string) map[string]string {
	b, err := os.ReadFile(r.accessFile(env))
	if err != nil {
		return nil
	}
	var a egressAccess
	if json.Unmarshal(b, &a) != nil {
		return nil
	}
	return a.Env
}
