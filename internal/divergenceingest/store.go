package divergenceingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/divergenceentry"
	"github.com/armada/orbital/ent/divergenceresolution"
	"github.com/armada/orbital/internal/divergence"
)

// applyReport writes the report's overrides for one DC to the ent store:
//   - UPSERT each entry (insert new, update last_seen_at / last_report_published_at on existing)
//   - DELETE entries previously stored for this DC that are not present in this report
//
// Single read + per-entry UPSERT + bulk-delete-by-key — not transactional.
// If the process dies between the UPSERT phase and the delete phase, the next
// poll cycle is idempotent and converges to the right state.
//
// Resolved entries are write-protected: once an admin has decided an entry,
// re-ingesting later reports refreshes only "we saw this again" timestamps
// (last_seen_at, last_report_published_at, type_name). The intended/override
// values stay frozen at resolution time. Orb's reports reflect what orb
// believed intent was at report time — that's a stale view until orb imports
// a fresh bundle, and we must not let it rewrite the history the admin
// already decided against.
func (i *Ingester) applyReport(ctx context.Context, dc dcRef, publishedAt time.Time, overrides []divergence.OverrideEntry) error {
	// Track which (entry_orb_id, field) pairs appear in this report so we
	// can delete the others below.
	present := make(map[string]bool, len(overrides))

	// Pre-fetch resolved (entryOrbId, field) pairs whose entryOrbIds appear in
	// this report. Narrowed by EntryOrbIDIn first to avoid scanning the
	// whole resolutions table; field equality is checked in-memory below
	// since orbital resolutions don't carry the DC identifier.
	resolved := make(map[string]bool, len(overrides))
	if len(overrides) > 0 {
		orbIDs := make([]string, 0, len(overrides))
		for _, ov := range overrides {
			orbIDs = append(orbIDs, ov.OrbID)
		}
		rows, err := i.db.DivergenceResolution.Query().
			Where(divergenceresolution.EntryOrbIDIn(orbIDs...)).
			All(ctx)
		if err != nil {
			return fmt.Errorf("query resolutions for DC %s: %w", dc.id, err)
		}
		for _, r := range rows {
			resolved[r.EntryOrbID+"|"+r.Field] = true
		}
	}

	for _, ov := range overrides {
		when, _ := time.Parse(time.RFC3339, ov.When) // best-effort; zero time if unparseable
		intended, _ := json.Marshal(ov.IntendedValue)
		override, _ := json.Marshal(ov.OverrideValue)
		entryKey := ov.OrbID + "|" + ov.Field
		isResolved := resolved[entryKey]

		// UPSERT: find existing by (dc, orbId, field), update if present, insert otherwise.
		existing, err := i.db.DivergenceEntry.Query().
			Where(
				divergenceentry.DcOrbID(dc.id),
				divergenceentry.EntryOrbID(ov.OrbID),
				divergenceentry.Field(ov.Field),
			).
			Only(ctx)

		if ent.IsNotFound(err) {
			// New entry — no prior decision frozen, take values from the report.
			// IntendedAtVersion comes from the report itself (orb captured it
			// at intake from its local read-only DGraph). Nil means "MVCC
			// unavailable for this row" — orbital's Accept handler degrades to
			// a value-based stale check.
			creator := i.db.DivergenceEntry.Create().
				SetDcOrbID(dc.id).
				SetEntryOrbID(ov.OrbID).
				SetField(ov.Field).
				SetTypeName(ov.Type).
				SetIntendedValue(intended).
				SetOverrideValue(override).
				SetWho(ov.Who).
				SetFirstSeenAt(when).
				SetLastSeenAt(when).
				SetLastReportPublishedAt(publishedAt)
			if ov.IntendedAtVersion != nil {
				creator = creator.SetIntendedAtVersion(*ov.IntendedAtVersion)
			}
			if _, err := creator.Save(ctx); err != nil {
				return fmt.Errorf("insert %s/%s.%s: %w", dc.id, ov.OrbID, ov.Field, err)
			}
		} else if err != nil {
			return fmt.Errorf("query %s/%s.%s: %w", dc.id, ov.OrbID, ov.Field, err)
		} else {
			// Resolved entries follow one of two paths:
			//   (1) New override == stored override → loop hasn't closed yet,
			//       orb is echoing the same divergence we already decided on.
			//       Freeze values; only refresh "we saw it again" timestamps.
			//   (2) New override != stored override → edge state drifted to a
			//       NEW value since the admin's decision. The decision was for
			//       a different value and is now stale. Delete the resolution
			//       so the entry reappears as pending and admin re-decides.
			// The history of the prior decision remains in the audit events
			// table (resolveDivergence + updateIdracSettings).
			shouldUpdateValues := !isResolved
			if isResolved && !bytes.Equal(override, existing.OverrideValue) {
				if _, err := i.db.DivergenceResolution.Delete().
					Where(
						divergenceresolution.EntryOrbID(ov.OrbID),
						divergenceresolution.Field(ov.Field),
					).Exec(ctx); err != nil {
					return fmt.Errorf("supersede resolution %s/%s.%s: %w", dc.id, ov.OrbID, ov.Field, err)
				}
				i.logger.Info("divergence ingester: superseded resolution — edge override changed since decision",
					"dc", dc.id, "orbId", ov.OrbID, "field", ov.Field,
					"priorOverride", string(existing.OverrideValue),
					"newOverride", string(override),
				)
				shouldUpdateValues = true
			}

			u := existing.Update().
				SetTypeName(ov.Type).
				SetLastSeenAt(when).
				SetLastReportPublishedAt(publishedAt)
			if shouldUpdateValues {
				u = u.SetIntendedValue(intended).
					SetOverrideValue(override).
					SetWho(ov.Who)
			}
			if _, err := u.Save(ctx); err != nil {
				return fmt.Errorf("update %s/%s.%s: %w", dc.id, ov.OrbID, ov.Field, err)
			}
		}
		present[entryKey] = true
	}

	// DELETE entries for this DC that are no longer in the report.
	stale, err := i.db.DivergenceEntry.Query().
		Where(divergenceentry.DcOrbID(dc.id)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("list %s entries: %w", dc.id, err)
	}
	var staleIDs []string
	for _, e := range stale {
		if !present[e.EntryOrbID+"|"+e.Field] {
			staleIDs = append(staleIDs, e.ID.String())
		}
	}
	if len(staleIDs) == 0 {
		return nil
	}

	// Sanity log if more than half the existing entries are about to be deleted —
	// could indicate cb-controller transiently produced an empty set. Doesn't
	// block the delete; just visible in logs for investigation.
	if len(staleIDs) > 0 && len(stale) > 0 {
		ratio := float64(len(staleIDs)) / float64(len(stale))
		if ratio > 0.5 && len(stale) >= 4 {
			i.logger.Warn("divergence ingester: large drop in entry count",
				"dc", dc.id,
				"existing", len(stale),
				"deleting", len(staleIDs),
				"ratio", ratio,
			)
		}
	}

	deleted := 0
	for _, e := range stale {
		if present[e.EntryOrbID+"|"+e.Field] {
			continue
		}
		// Loop closure: orb stopped reporting this divergence. Delete the
		// entry AND any attached resolution — the resolution row is bound
		// 1:1 with the active entry, never outlives it. Audit of the
		// decision lives in the Event log. If local admin later re-applies
		// the same override, orb reports a fresh divergence; the operator
		// re-decides from a clean slate.
		//
		// Pre-fetch the resolution's action (if any) BEFORE the delete so we
		// can attribute the closure in audit. action=ignore loop-closure is
		// load-bearing semantic: per configbundle ADR-009, it signals that
		// the edge admin released their SSA claim and cb-controller's
		// ReclaimController restored intent. The cloud admin should see this
		// closure as an explicit edge-driven action (closeIgnoreOnHandback),
		// not silent disappearance.
		var prevAction string
		if r, err := i.db.DivergenceResolution.Query().
			Where(
				divergenceresolution.EntryOrbID(e.EntryOrbID),
				divergenceresolution.Field(e.Field),
			).
			Only(ctx); err == nil {
			prevAction = string(r.Action)
		}

		// Transactional delete: resolution + entry as a single unit. If
		// either fails, nothing is deleted and the next ingest tick retries.
		// Avoids the orphan-resolution case where a deleted entry leaves
		// behind a resolution that silently re-attaches to a future
		// re-divergence with the same (entry_orb_id, field) key.
		if err := i.deleteEntryWithResolution(ctx, e); err != nil {
			i.logger.Warn("divergence ingester: transactional cleanup failed",
				"dc", dc.id, "orbId", e.EntryOrbID, "field", e.Field, "err", err)
			continue
		}
		deleted++

		if prevAction == "ignore" {
			i.writeHandbackAuditEvent(ctx, dc.id, e)
		}
	}
	if deleted > 0 {
		i.logger.Info("divergence ingester: closed loop on resolved entries",
			"dc", dc.id, "deleted", deleted)
	}
	return nil
}

// deleteEntryWithResolution removes a DivergenceEntry and its matching
// DivergenceResolution (if any) atomically. Either both deletes succeed or
// neither does — preventing the orphan-resolution case where a stale resolution
// would silently auto-resolve a future re-divergence with the same key.
func (i *Ingester) deleteEntryWithResolution(ctx context.Context, e *ent.DivergenceEntry) error {
	tx, err := i.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if _, err := tx.DivergenceResolution.Delete().
		Where(
			divergenceresolution.EntryOrbID(e.EntryOrbID),
			divergenceresolution.Field(e.Field),
		).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete resolution: %w", err)
	}
	if err := tx.DivergenceEntry.DeleteOne(e).Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete entry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// writeHandbackAuditEvent records that an active Ignore resolution closed via
// the edge-handback path (configbundle ADR-009): local admin released their
// SSA claim, cb-controller reclaimed the field with intent value, the
// divergence reporter stopped emitting the entry, and this ingester is
// removing the stale Ignore directive. Failures are logged and swallowed —
// audit writes must never block or fail an ingest cycle.
func (i *Ingester) writeHandbackAuditEvent(ctx context.Context, dcOrbID string, e *ent.DivergenceEntry) {
	details := map[string]any{
		"entryId":              e.ID.String(),
		"dcOrbId":              dcOrbID,
		"orbId":                e.EntryOrbID,
		"field":                e.Field,
		"prevResolutionAction": "ignore",
		"trigger":              "edge-handback",
	}
	raw, _ := json.Marshal(details)
	ev, err := i.db.Event.Create().
		SetActor("system:ingester").
		SetEventCategory("management").
		SetOperations([]string{"closeIgnoreOnHandback"}).
		SetDetails(raw).
		Save(ctx)
	if err != nil {
		i.logger.Warn("divergence ingester: write handback audit event failed",
			"dc", dcOrbID, "orbId", e.EntryOrbID, "field", e.Field, "err", err)
		return
	}
	// Attach the underlying resource so the event is visible on the resource's
	// audit panel (DC, Server, IdracSettings, etc.) — same pattern as
	// resolveDivergence / dismissDivergence.
	if _, err := i.db.EventResource.Create().
		SetOrbID(e.EntryOrbID).
		SetEvent(ev).
		Save(ctx); err != nil {
		i.logger.Warn("divergence ingester: attach event resource failed",
			"dc", dcOrbID, "orbId", e.EntryOrbID, "field", e.Field, "err", err)
	}
	if _, err := i.db.EventResourceType.Create().
		SetResourceType("DivergenceEntry").
		SetEvent(ev).
		Save(ctx); err != nil {
		i.logger.Warn("divergence ingester: attach event resource_type failed",
			"dc", dcOrbID, "orbId", e.EntryOrbID, "field", e.Field, "err", err)
	}
}
