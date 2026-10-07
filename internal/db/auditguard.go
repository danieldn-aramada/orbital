package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// AuditTables are the three tables that make up one audit record. All three are
// guarded: hashing or protecting only the parent would leave the orbIds and
// resource types a row points at freely rewritable, which is most of what makes
// an event findable.
var AuditTables = []string{
	"audit_events",
	"audit_event_resources",
	"audit_event_resource_types",
}

// auditGuardFn is the trigger body. One function, reused by every table, so the
// three can never drift apart.
//
// RAISE EXCEPTION rather than silently skipping the row: a write that looks
// like it worked and did nothing is the failure mode this whole subsystem
// exists to avoid.
const auditGuardFn = `
CREATE OR REPLACE FUNCTION orbital_audit_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION
        'audit records are append-only: % on % is refused', TG_OP, TG_TABLE_NAME
        USING ERRCODE = 'integrity_constraint_violation',
              HINT = 'Audit history is evidence. Correct a mistaken record by writing a new event, never by editing or deleting one.';
END;
$$ LANGUAGE plpgsql;`

// InstallAuditGuard makes the audit tables append-only in the DATABASE rather
// than by convention.
//
// WHY A TRIGGER AND NOT GRANTS. Revoking UPDATE/DELETE does not bind the table
// OWNER, and orbital owns its schema — it runs ent's auto-migration, so it must.
// A trigger fires regardless of privilege, so it binds the role orbital actually
// connects as without needing a role split, a second credential, or any change
// to how orbital is deployed. Grants are still worth adding later as a second
// layer; this is the one that works today.
//
// WHAT IT DOES NOT STOP. A superuser can set session_replication_role = replica
// and bypass every trigger, and anyone with filesystem access to the data
// directory is outside the database's reach entirely. This is prevention against
// the application, a compromised application credential, and mistakes — not
// against a determined DBA. Detecting that class needs the hash chain and an
// external anchor; see .local/audit-integrity-design.md.
//
// Idempotent: CREATE OR REPLACE for the function, drop-then-create for each
// trigger, so a restart is a no-op and a partially-applied state self-heals.
func InstallAuditGuard(ctx context.Context, rawDB *sql.DB) error {
	if rawDB == nil {
		return nil
	}
	if _, err := rawDB.ExecContext(ctx, auditGuardFn); err != nil {
		return fmt.Errorf("creating audit guard function: %w", err)
	}
	for _, t := range AuditTables {
		// Dropped first because CREATE TRIGGER has no OR REPLACE before
		// PostgreSQL 14, and orbital does not pin a server version.
		if _, err := rawDB.ExecContext(ctx,
			fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s", guardName(t), t)); err != nil {
			return fmt.Errorf("dropping stale audit guard on %s: %w", t, err)
		}
		if _, err := rawDB.ExecContext(ctx, fmt.Sprintf(
			"CREATE TRIGGER %s BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION orbital_audit_append_only()",
			guardName(t), t)); err != nil {
			return fmt.Errorf("installing audit guard on %s: %w", t, err)
		}
	}
	return nil
}

// VerifyAuditGuard reports which audit tables are NOT protected.
//
// This is the detection half, and it is the point rather than a nicety. A
// prevention control nobody checks is indistinguishable from one that was
// dropped: `DROP TRIGGER` is a single statement, it leaves the data looking
// exactly as it did, and every subsequent edit then succeeds in silence. So
// orbital asks the database what is actually installed, rather than trusting
// that it installed it.
//
// Returns the unprotected table names, empty when all are guarded.
func VerifyAuditGuard(ctx context.Context, rawDB *sql.DB) ([]string, error) {
	if rawDB == nil {
		return nil, nil
	}
	var unprotected []string
	for _, t := range AuditTables {
		// tgenabled 'D' is a trigger that exists but is DISABLED — it reads as
		// present to anything that only checks for the row, and fires never.
		var ok bool
		err := rawDB.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_trigger tg
				JOIN pg_class c ON c.oid = tg.tgrelid
				WHERE c.relname = $1 AND tg.tgname = $2 AND NOT tg.tgisinternal
				  AND tg.tgenabled <> 'D'
			)`, t, guardName(t)).Scan(&ok)
		if err != nil {
			return nil, fmt.Errorf("verifying audit guard on %s: %w", t, err)
		}
		if !ok {
			unprotected = append(unprotected, t)
		}
	}
	return unprotected, nil
}

// EnsureAuditGuard installs the guard and verifies it took, logging the outcome.
//
// NOT fatal. Orbital serving without the guard is worse than orbital not
// serving only if you believe audit integrity outranks availability, and that
// is the adopter's call to make, not a default to impose — the same reasoning
// that keeps audit-write failures from failing a request. What is NOT optional
// is that the condition is loud: ERROR, named tables, and a metric an alert can
// watch.
func EnsureAuditGuard(ctx context.Context, rawDB *sql.DB, logger *slog.Logger, onUnprotected func(int)) {
	if rawDB == nil {
		return
	}
	if err := InstallAuditGuard(ctx, rawDB); err != nil {
		logger.Error("audit append-only guard could not be installed — audit tables are editable", "err", err)
	}
	unprotected, err := VerifyAuditGuard(ctx, rawDB)
	if err != nil {
		logger.Error("audit append-only guard could not be verified — treat as unprotected", "err", err)
		if onUnprotected != nil {
			onUnprotected(len(AuditTables))
		}
		return
	}
	if onUnprotected != nil {
		onUnprotected(len(unprotected))
	}
	if len(unprotected) > 0 {
		logger.Error("AUDIT TABLES ARE NOT APPEND-ONLY — records can be edited or deleted without trace",
			"unprotected", strings.Join(unprotected, ","))
		return
	}
	logger.Info("audit append-only guard verified", "tables", strings.Join(AuditTables, ","))
}

func guardName(table string) string { return table + "_append_only" }
