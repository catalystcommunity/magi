// Package testenv gives tests a disposable database.
package testenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Database creates a uniquely named database and returns its URL. The
// database is dropped when the test ends. The test is skipped when
// MAGI_TEST_DATABASE_URL is not set.
//
// The URL connects as a new role that owns the database and is not a
// superuser, as in production. A superuser would skip row-level security.
func Database(t testing.TB) string {
	t.Helper()
	base := os.Getenv("MAGI_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("MAGI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	b := make([]byte, 6)
	rand.Read(b)
	name := "magi_test_" + hex.EncodeToString(b)
	pw := make([]byte, 12)
	rand.Read(pw)
	password := hex.EncodeToString(pw)
	for _, q := range []string{
		"CREATE ROLE " + name + " LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD '" + password + "'",
		"CREATE DATABASE " + name + " OWNER " + name,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			admin.Close(ctx)
			t.Fatalf("create test database: %v", err)
		}
	}
	t.Cleanup(func() {
		admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Exec(ctx, "DROP ROLE IF EXISTS "+name)
		admin.Close(ctx)
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	u.User = url.UserPassword(name, password)
	return u.String()
}
