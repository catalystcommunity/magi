package config

import (
	"os"
	"path/filepath"
	"strings"
)

// ProfilePath locates operator profiles outside the source checkout.
func ProfilePath(component, name string) string {
	if directory := Environment("MAGI_" + strings.ToUpper(component) + "_PROFILE_DIR"); directory != "" {
		return filepath.Join(directory, name)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "magi", "profiles", component, name)
}
