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

// incomingEntry is a marshaled report entry, used for both the equality check
// and the insert phase.
type incomingEntry struct {
	ov       divergence.OverrideEntry
	intended json.RawMessage
	override json.RawMessage
	when     time.Time
}

// applyReport is the supersede-semantics ingest path (ADR 012).
//
// A divergence report's content is the set of (entry_orb_id, field,
// override_value) tuples it carries. Three cases:
//
//   - Content matches AND no resolution has been submitted for any of the
//     DC's entries → no state change. Touch last_seen_at and
//     last_report_published_at on each row so the UI shows freshness, but
//     leave entries intact (operator is mid-decision; staged decisions live
//     client-side until Submit).
//   - Content matches BUT one or more resolutions exist for the DC's entries
//     → atomic supersede. Resolutions are bound to the report that triggered
//     them; once Submit has dispatched a decision, the next identical-content
//     report is a *new* divergence occurrence (cloud-admin and local-admin
//     reproduced the same drift) and must clear prior decisions so the
//     operator sees it as fresh pending.
//   - Content differs (any added, removed, or value-changed tuple) → atomic
//     supersede. Same as before.
//
// All supersede paths drop entries + resolutions in the same tx and emit one
// supersedeDivergenceReport audit event.
func (i *Ingester) applyReport(ctx context.Context, dc dcRef, publishedAt time.Time, overrides []divergence.OverrideEntry) error {
	incoming := make([]incomingEntry, len(overrides))
	for idx, ov := range overrides {
		intended, _ := json.Marshal(ov.IntendedValue)
		override, _ := json.Marshal(ov.OverrideValue)
		when, _ := time.Parse(time.RFC3339, ov.When) // zero time if unparseable
		incoming[idx] = incomingEntry{
			ov:       ov,
			intended: intended,
			override: override,
			when:     when,
		}
	}

	existing, err := i.db.DivergenceEntry.Query().
		Where(divergenceentry.DcOrbID(dc.id)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("query existing entries for %s: %w", dc.id, err)
	}

	if contentEqual(existing, incoming) {
		// Same content as DB. Whether to no-op or supersede depends on
		// whether any resolution has been submitted for this DC's entries.
		hasResolutions, err := i.dcHasResolutions(ctx, existing)
		if err != nil {
			return fmt.Errorf("check resolutions for %s: %w", dc.id, err)
		}
		if !hasResolutions {
			// Operator still mid-decision (or untouched). Preserve entries +
			// touch timestamps so the UI reflects freshness.
			for _, in := range incoming {
				if _, err := i.db.DivergenceEntry.Update().
					Where(
						divergenceentry.DcOrbID(dc.id),
						divergenceentry.EntryOrbID(in.ov.OrbID),
						divergenceentry.Field(in.ov.Field),
					).
					SetLastSeenAt(in.when).
					SetLastReportPublishedAt(publishedAt).
					Save(ctx); err != nil {
					return fmt.Errorf("touch %s/%s.%s: %w", dc.id, in.ov.OrbID, in.ov.Field, err)
				}
			}
			return nil
		}
		// Resolutions exist → fall through to supersede. The recurrence of
		// identical drift after Submit is a new event.
	}

	// Content differs — atomic supersede.
	tx, err := i.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	// Drop resolutions tied to this DC's existing entries. DivergenceResolution
	// has no dc_orb_id column, so we target by (entry_orb_id, field) pairs we
	// already know belong to this DC.
	for _, e := range existing {
		if _, err := tx.DivergenceResolution.Delete().
			Where(
				divergenceresolution.EntryOrbID(e.EntryOrbID),
				divergenceresolution.Field(e.Field),
			).Exec(ctx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("delete resolutions: %w", err)
		}
	}

	if _, err := tx.DivergenceEntry.Delete().
		Where(divergenceentry.DcOrbID(dc.id)).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete entries: %w", err)
	}

	for _, in := range incoming {
		if _, err := tx.DivergenceEntry.Create().
			SetDcOrbID(dc.id).
			SetEntryOrbID(in.ov.OrbID).
			SetField(in.ov.Field).
			SetTypeName(in.ov.Type).
			SetIntendedValue(in.intended).
			SetOverrideValue(in.override).
			SetWho(in.ov.Who).
			SetFirstSeenAt(in.when).
			SetLastSeenAt(in.when).
			SetLastReportPublishedAt(publishedAt).
			Save(ctx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert %s/%s.%s: %w", dc.id, in.ov.OrbID, in.ov.Field, err)
		}
	}

	// One audit event per supersede. Forensic history of individual
	// resolutions dropped lives in the prior resolveDivergence / Accept
	// dispatch events in the event log.
	details := map[string]any{
		"dcOrbId": dc.id,
		"dropped": len(existing),
		"added":   len(incoming),
	}
	raw, _ := json.Marshal(details)
	ev, err := tx.AuditEvent.Create().
		SetActor("system:ingester").
		SetEventCategory("management").
		SetOperations([]string{"supersedeDivergenceReport"}).
		SetDetails(raw).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("audit event: %w", err)
	}
	if _, err := tx.AuditEventResource.Create().SetOrbID(dc.id).SetAuditEvent(ev).Save(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("audit event resource: %w", err)
	}
	if _, err := tx.AuditEventResourceType.Create().SetResourceType("DataCenter").SetAuditEvent(ev).Save(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("audit event resource_type: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	i.logger.Info("divergence ingester: superseded report",
		"dc", dc.id, "dropped", len(existing), "added", len(incoming))
	return nil
}

// dcHasResolutions returns true if any DivergenceResolution row exists for
// the (entry_orb_id, field) pairs of the given DC entries. Used by
// applyReport to distinguish "operator hasn't decided yet (preserve)" from
// "operator already submitted (supersede on recurrence)".
func (i *Ingester) dcHasResolutions(ctx context.Context, existing []*ent.DivergenceEntry) (bool, error) {
	if len(existing) == 0 {
		return false, nil
	}
	orbIDs := make([]string, 0, len(existing))
	seen := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		if _, ok := seen[e.EntryOrbID]; ok {
			continue
		}
		seen[e.EntryOrbID] = struct{}{}
		orbIDs = append(orbIDs, e.EntryOrbID)
	}
	count, err := i.db.DivergenceResolution.Query().
		Where(divergenceresolution.EntryOrbIDIn(orbIDs...)).
		Limit(1).
		Count(ctx)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// contentEqual reports whether the existing entries for a DC match the
// incoming report's content, where "content" is the set of
// (entry_orb_id, field, override_value) tuples. Order-independent.
func contentEqual(existing []*ent.DivergenceEntry, incoming []incomingEntry) bool {
	if len(existing) != len(incoming) {
		return false
	}
	byKey := make(map[string][]byte, len(existing))
	for _, e := range existing {
		byKey[e.EntryOrbID+"|"+e.Field] = bytes.TrimSpace(e.OverrideValue)
	}
	for _, in := range incoming {
		current, ok := byKey[in.ov.OrbID+"|"+in.ov.Field]
		if !ok {
			return false
		}
		if !bytes.Equal(current, bytes.TrimSpace(in.override)) {
			return false
		}
	}
	return true
}
