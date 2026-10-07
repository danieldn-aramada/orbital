//go:build integration

package db_test

// Acceptance tests for database-enforced append-only audit tables. Names were
// transcribed from the acceptance list before any body was written.
//
// What this guards is a claim orbital publishes to API consumers — the audit
// trail is append-only — which until now rested entirely on "no code path calls
// Update or Delete". That is true and unenforced, and the generated ent mutators
// exist and work. A convention is not a control.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/armada/orbital/ent/auditevent"
	orbitaldb "github.com/armada/orbital/internal/db"
	"github.com/google/uuid"
)

// seedAuditEvent writes one row through the normal path, so the tests below
// attack a record that was created exactly the way production creates them.
func seedAuditEvent(t *testing.T, dsn string) uuid.UUID {
	t.Helper()
	raw, client := openClient(t, dsn)
	ctx := context.Background()
	if err := orbitaldb.Migrate(ctx, raw, client, time.Minute, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := orbitaldb.InstallAuditGuard(ctx, raw); err != nil {
		t.Fatalf("install guard: %v", err)
	}
	ev := client.AuditEvent.Create().
		SetActor("guard@test").
		SetEventCategory("data").
		SetOperations([]string{"updateServer"}).
		SetDetails(json.RawMessage(`{"operationName":"updateServer"}`)).
		SaveX(ctx)
	client.AuditEventResource.Create().SetOrbID("ns:srv-1").SetAuditEventID(ev.ID).SaveX(ctx)
	client.AuditEventResourceType.Create().SetResourceType("Server").SetAuditEventID(ev.ID).SaveX(ctx)
	return ev.ID
}

// Acceptance 1 and 2: the parent row cannot be edited or deleted, and the
// refusal comes from PostgreSQL rather than from orbital choosing not to ask.
// Driven through raw SQL deliberately — going through ent would only prove ent
// does not call Update, which was never in doubt.
func TestAuditGuard_ParentRowCannotBeUpdatedOrDeleted(t *testing.T) {
	dsn := freshDatabase(t, "orbital_auditguard_parent")
	id := seedAuditEvent(t, dsn)
	raw, _ := openClient(t, dsn)

	if _, err := raw.Exec(`UPDATE audit_events SET actor = 'someone-else' WHERE id = $1`, id); err == nil {
		t.Error("UPDATE on audit_events succeeded — the trail is editable")
	}
	if _, err := raw.Exec(`DELETE FROM audit_events WHERE id = $1`, id); err == nil {
		t.Error("DELETE on audit_events succeeded — the trail is erasable")
	}

	// And the record is still there and unchanged, which is the property the
	// refusals exist to produce.
	_, client := openClient(t, dsn)
	ev := client.AuditEvent.Query().Where(auditevent.ID(id)).OnlyX(context.Background())
	if ev.Actor != "guard@test" {
		t.Errorf("actor = %q after a refused UPDATE, want %q", ev.Actor, "guard@test")
	}
}

// Acceptance 3: the children are guarded too. A record's orbIds and resource
// types live in these tables, so leaving them writable would let someone make
// an event unfindable without touching the event.
func TestAuditGuard_ChildRowsCannotBeUpdatedOrDeleted(t *testing.T) {
	dsn := freshDatabase(t, "orbital_auditguard_children")
	seedAuditEvent(t, dsn)
	raw, _ := openClient(t, dsn)

	for _, c := range []struct{ table, set string }{
		{"audit_event_resources", "orb_id = 'ns:somewhere-else'"},
		{"audit_event_resource_types", "resource_type = 'NotAServer'"},
	} {
		if _, err := raw.Exec("UPDATE " + c.table + " SET " + c.set); err == nil {
			t.Errorf("UPDATE on %s succeeded", c.table)
		}
		if _, err := raw.Exec("DELETE FROM " + c.table); err == nil {
			t.Errorf("DELETE on %s succeeded", c.table)
		}
	}
}

// Acceptance 4, the negative that matters most: the guard must not break the
// write path. A control that stops audit records being written is worse than
// the gap it closes.
func TestAuditGuard_InsertStillWorks(t *testing.T) {
	dsn := freshDatabase(t, "orbital_auditguard_insert")
	seedAuditEvent(t, dsn)
	_, client := openClient(t, dsn)
	ctx := context.Background()

	before := client.AuditEvent.Query().CountX(ctx)
	client.AuditEvent.Create().
		SetActor("second@test").
		SetEventCategory("management").
		SetDetails(json.RawMessage(`{}`)).
		SaveX(ctx)
	if after := client.AuditEvent.Query().CountX(ctx); after != before+1 {
		t.Errorf("event count %d -> %d, want one more — the guard is blocking writes", before, after)
	}
}

// Acceptance 5: installing twice is a no-op. Orbital installs on every boot, so
// a restart must not fail and a half-applied state must heal rather than persist.
func TestAuditGuard_InstallIsIdempotent(t *testing.T) {
	dsn := freshDatabase(t, "orbital_auditguard_idem")
	seedAuditEvent(t, dsn)
	raw, _ := openClient(t, dsn)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := orbitaldb.InstallAuditGuard(ctx, raw); err != nil {
			t.Fatalf("install %d: %v", i+1, err)
		}
	}
	if un, err := orbitaldb.VerifyAuditGuard(ctx, raw); err != nil || len(un) != 0 {
		t.Errorf("after 3 installs: unprotected=%v err=%v, want none", un, err)
	}
}

// Acceptance 6: orbital notices when the guard is gone. This is the detection
// half, and the reason it exists is that DROP TRIGGER is one statement, leaves
// the data looking identical, and makes every later edit succeed in silence.
//
// The DISABLE case is the subtle one: the trigger still exists, so anything
// checking only for its presence reads as protected while it fires never.
func TestAuditGuard_VerifyDetectsRemovalAndDisabling(t *testing.T) {
	dsn := freshDatabase(t, "orbital_auditguard_detect")
	seedAuditEvent(t, dsn)
	raw, _ := openClient(t, dsn)
	ctx := context.Background()

	if _, err := raw.Exec(`DROP TRIGGER audit_events_append_only ON audit_events`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	un, err := orbitaldb.VerifyAuditGuard(ctx, raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(un) != 1 || un[0] != "audit_events" {
		t.Errorf("after DROP: unprotected=%v, want [audit_events]", un)
	}

	// Reinstall, then disable rather than drop.
	if err := orbitaldb.InstallAuditGuard(ctx, raw); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if _, err := raw.Exec(`ALTER TABLE audit_event_resources DISABLE TRIGGER audit_event_resources_append_only`); err != nil {
		t.Fatalf("disable trigger: %v", err)
	}
	un, err = orbitaldb.VerifyAuditGuard(ctx, raw)
	if err != nil {
		t.Fatalf("verify after disable: %v", err)
	}
	if len(un) != 1 || un[0] != "audit_event_resources" {
		t.Errorf("after DISABLE: unprotected=%v, want [audit_event_resources] — a disabled trigger reads as present but never fires", un)
	}
}

// Acceptance 7, the negative: a healthy installation reports nothing. An alert
// that fires when all is well is worse than no alert — it trains whoever reads
// it to ignore the one time it matters.
func TestAuditGuard_HealthyInstallReportsNothingUnprotected(t *testing.T) {
	dsn := freshDatabase(t, "orbital_auditguard_healthy")
	seedAuditEvent(t, dsn)
	raw, _ := openClient(t, dsn)

	un, err := orbitaldb.VerifyAuditGuard(context.Background(), raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(un) != 0 {
		t.Errorf("healthy install reports %v unprotected, want none", un)
	}

	seen := -1
	orbitaldb.EnsureAuditGuard(context.Background(), raw, quietLogger(), func(n int) { seen = n })
	if seen != 0 {
		t.Errorf("metric callback got %d, want 0 on a healthy install", seen)
	}
}
