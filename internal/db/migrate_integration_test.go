//go:build integration

package db_test

// Acceptance tests for schema migration at multiple replicas. Test names were
// transcribed from the acceptance list before any body was written.
//
// The first test is the reproduction that motivated the lock: it FAILS against
// a bare db.Schema.Create — deterministically, 5 runs out of 5 — with
//
//	create "orbs" table: pq: duplicate key value violates unique
//	constraint "pg_type_typname_nsp_index" (23505)
//
// In cmd/orbital that error is log.Fatalf, so the pod crashloops. Keep this
// test if the lock is ever replaced by a migration Job; it pins the guarantee,
// not the mechanism.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	orbitaldb "github.com/armada/orbital/internal/db"
	_ "github.com/lib/pq"
)

const (
	adminDSN  = "postgres://orbital:orbital-local-dev-secret@localhost:5432/postgres?sslmode=disable"
	raceDBFmt = "postgres://orbital:orbital-local-dev-secret@localhost:5432/%s?sslmode=disable"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// freshDatabase creates an EMPTY database — the state a new adopter's first
// deploy starts from, and the worst case for concurrent migration because
// every table has to be created rather than one column added.
func freshDatabase(t *testing.T, name string) string {
	t.Helper()
	adm, err := sql.Open("postgres", adminDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { adm.Close() }) //nolint:errcheck

	adm.Exec("DROP DATABASE IF EXISTS " + name) //nolint:errcheck
	if _, err := adm.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() { adm.Exec("DROP DATABASE IF EXISTS " + name) }) //nolint:errcheck
	return fmt.Sprintf(raceDBFmt, name)
}

func openClient(t *testing.T, dsn string) (*sql.DB, *ent.Client) {
	t.Helper()
	raw, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { raw.Close() }) //nolint:errcheck
	c := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, raw)))
	t.Cleanup(func() { c.Close() }) //nolint:errcheck
	return raw, c
}

// Acceptance 1: two replicas booting simultaneously against an empty database
// both migrate cleanly. This is what an adopter does on their first deploy —
// they have one deploy, so the "scale to 1 first" workaround is not available
// to them.
func TestMigrate_FreshInstallAtTwoReplicasBothSucceed(t *testing.T) {
	dsn := freshDatabase(t, "orbital_migrace")

	const replicas = 2
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range replicas {
		raw, client := openClient(t, dsn)
		wg.Add(1)
		go func(i int, raw *sql.DB, client *ent.Client) {
			defer wg.Done()
			<-start
			errs[i] = orbitaldb.Migrate(context.Background(), raw, client, time.Minute, quietLogger())
		}(i, raw, client)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("replica %d failed to migrate: %v — in cmd/orbital this is log.Fatalf, i.e. a crashloop", i, err)
		}
	}

	// Both replicas must end up able to serve against a complete schema, not
	// merely avoid erroring.
	_, verify := openClient(t, dsn)
	if _, err := verify.ExportJob.Query().Count(context.Background()); err != nil {
		t.Fatalf("schema not usable after concurrent migration: %v", err)
	}
}

// The loser must WAIT and then migrate, never skip. A replica that gave up on
// the lock and carried on would serve requests against a schema that is not
// there yet.
func TestMigrate_ReplicaThatLosesTheLockStillEndsUpMigrated(t *testing.T) {
	dsn := freshDatabase(t, "orbital_migwait")
	ctx := context.Background()

	// Hold the lock on an independent session, release it shortly after the
	// caller starts waiting.
	holder, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	defer holder.Close() //nolint:errcheck
	hc, err := holder.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	var got bool
	if err := hc.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", orbitaldb.MigrationAdvisoryLockKey).Scan(&got); err != nil || !got {
		t.Fatalf("holder could not take the lock: got=%v err=%v", got, err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(500 * time.Millisecond)
		hc.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", orbitaldb.MigrationAdvisoryLockKey) //nolint:errcheck
		hc.Close()                                                                               //nolint:errcheck
		close(released)
	}()

	raw, client := openClient(t, dsn)
	if err := orbitaldb.Migrate(ctx, raw, client, 30*time.Second, quietLogger()); err != nil {
		t.Fatalf("waiting replica failed to migrate: %v", err)
	}
	<-released

	if _, err := client.ExportJob.Query().Count(ctx); err != nil {
		t.Fatalf("replica that waited did not end up migrated: %v", err)
	}
}

// Acceptance 2: a replica that cannot acquire the lock exits with a diagnostic
// rather than hanging. A blocking pg_advisory_lock would wait on a dead
// holder's lingering connection until TCP keepalives fire — 7200s by default
// on Linux — which presents as a silent hang.
func TestMigrate_TimesOutWithDiagnosticRatherThanHanging(t *testing.T) {
	dsn := freshDatabase(t, "orbital_migtimeout")
	ctx := context.Background()

	// A holder that never lets go, standing in for a pod that died mid-migration.
	holder, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	defer holder.Close() //nolint:errcheck
	hc, err := holder.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer hc.Close() //nolint:errcheck
	var got bool
	if err := hc.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", orbitaldb.MigrationAdvisoryLockKey).Scan(&got); err != nil || !got {
		t.Fatalf("holder could not take the lock: got=%v err=%v", got, err)
	}

	raw, client := openClient(t, dsn)
	began := time.Now()
	err = orbitaldb.Migrate(ctx, raw, client, 2*time.Second, quietLogger())
	elapsed := time.Since(began)

	if err == nil {
		t.Fatal("expected a timeout error while another session held the lock")
	}
	if !errors.Is(err, orbitaldb.ErrMigrationLockTimeout) {
		t.Fatalf("error is not ErrMigrationLockTimeout, so a caller cannot tell "+
			"'someone else is migrating' from 'the migration failed': %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("took %s to give up on a 2s timeout — this is the hang the retry contract exists to prevent", elapsed)
	}
}

// Acceptance 3: the lock is released on success, so the next startup is not
// blocked by the previous one. A leaked session lock would make every
// subsequent deploy wait out the full timeout and then fail.
func TestMigrate_ReleasesTheLockSoTheNextStartupIsNotBlocked(t *testing.T) {
	dsn := freshDatabase(t, "orbital_migrelease")
	ctx := context.Background()

	raw, client := openClient(t, dsn)
	if err := orbitaldb.Migrate(ctx, raw, client, time.Minute, quietLogger()); err != nil {
		t.Fatalf("first migration: %v", err)
	}

	// A second startup against the already-migrated database must not wait.
	raw2, client2 := openClient(t, dsn)
	began := time.Now()
	if err := orbitaldb.Migrate(ctx, raw2, client2, 3*time.Second, quietLogger()); err != nil {
		t.Fatalf("second startup was blocked by a leaked lock: %v", err)
	}
	if elapsed := time.Since(began); elapsed > 2*time.Second {
		t.Fatalf("second startup waited %s — the lock was not released", elapsed)
	}
}
