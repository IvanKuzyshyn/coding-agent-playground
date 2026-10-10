// Package testutil holds shared test helpers: isolated Postgres databases and in-memory fakes
// for the sandbox provider and sandboxd.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"terrarium/internal/store"
)

// EnvDatabaseURL names a Postgres URL whose role may CREATE DATABASE. Tests never write to the
// database it points at; each test gets its own database cloned from a migrated template.
const EnvDatabaseURL = "TERRARIUM_TEST_DATABASE_URL"

var (
	tmplMu   sync.Mutex
	tmplName string
	tmplErr  error
)

// Main runs the package's tests and drops the template database afterwards. Use it from TestMain.
func Main(m *testing.M) {
	code := m.Run()
	tmplMu.Lock()
	if tmplName != "" {
		if err := adminExec(context.Background(), `DROP DATABASE IF EXISTS `+pgx.Identifier{tmplName}.Sanitize()+` WITH (FORCE)`); err != nil {
			fmt.Fprintln(os.Stderr, "drop template database:", err)
		}
	}
	tmplMu.Unlock()
	os.Exit(code)
}

// NewStore returns a store on a fresh, migrated database that is dropped when the test ends.
// It skips the test when EnvDatabaseURL is unset.
func NewStore(t testing.TB) *store.Store {
	t.Helper()
	if os.Getenv(EnvDatabaseURL) == "" {
		t.Skip(EnvDatabaseURL + " not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	name := "terrarium_test_" + randomHex(6)
	// CREATE DATABASE ... TEMPLATE fails if another session uses the template, so serialise.
	tmplMu.Lock()
	err := ensureTemplate(ctx)
	if err == nil {
		err = adminExec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()+` TEMPLATE `+pgx.Identifier{tmplName}.Sanitize())
	}
	tmplMu.Unlock()
	if err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		if err := adminExec(context.Background(), `DROP DATABASE IF EXISTS `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})

	st, err := store.Open(ctx, dbURL(name))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// ensureTemplate creates and migrates the package's template database once. Caller holds tmplMu.
func ensureTemplate(ctx context.Context) error {
	if tmplName != "" || tmplErr != nil {
		return tmplErr
	}
	name := "terrarium_test_tmpl_" + randomHex(6)
	if tmplErr = adminExec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()); tmplErr != nil {
		return tmplErr
	}
	tmplName = name
	st, err := store.Open(ctx, dbURL(name))
	if err != nil {
		tmplErr = err
		return err
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		tmplErr = fmt.Errorf("migrate: %w", err)
	}
	return tmplErr
}

// dbURL points the admin URL at another database with a small pool, so parallel tests stay well
// under the server's connection limit.
func dbURL(name string) string {
	u, err := url.Parse(os.Getenv(EnvDatabaseURL))
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	q := u.Query()
	q.Set("pool_max_conns", "6")
	u.RawQuery = q.Encode()
	return u.String()
}

func adminExec(ctx context.Context, sql string) error {
	conn, err := pgx.Connect(ctx, os.Getenv(EnvDatabaseURL))
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	_, err = conn.Exec(ctx, sql)
	return err
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
