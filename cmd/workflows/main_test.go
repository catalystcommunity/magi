package main

import (
	"errors"
	"strings"
	"testing"
)

func TestStartupErrorRemovesDatabaseCredentials(t *testing.T) {
	message := startupError(errors.New("postgres://user:secret@example/db: secret refused"), "secret", "postgres://user:secret@example/db")
	if strings.Contains(message, "secret") || strings.Contains(message, "postgres://") || !strings.Contains(message, "refused") {
		t.Fatal("startup diagnostic must retain the reason without credentials")
	}
}
