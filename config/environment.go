package config

import (
	"os"
	"strings"
)

// Environment reads a MAGI_ setting. OPAL_ remains a compatibility alias.
// An explicit empty MAGI_ value overrides the old value.
func Environment(name string) string {
	if !strings.HasPrefix(name, "MAGI_") {
		panic("expected a MAGI_ environment setting")
	}
	if value, present := os.LookupEnv(name); present {
		return value
	}
	return os.Getenv("OPAL_" + strings.TrimPrefix(name, "MAGI_"))
}
