package divergenceingest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/divergenceentry"
	"github.com/armada/orbital/internal/divergence"
)

// applySnapshot writes the snapshot's overrides for one DC to the ent store:
//   - UPSERT each entry (insert new, update last_seen_at / last_snapshot_published_at on existing)
//   - DELETE entries previously stored for this DC that are not present in this snapshot
//
// Single read + per-entry UPSERT + bulk-delete-by-key — not transactional.
// If the process dies between the UPSERT phase and the delete phase, the next
// poll cycle is idempotent and converges to the right state.
func (i *Ingester) applySnapshot(ctx context.Context, dc dcRef, publishedAt time.Time, overrides []divergence.OverrideEntry) error {
	// Track which (entry_orb_id, field) pairs appear in this snapshot so we
	// can delete the others below.
	present := make(map[string]bool, len(overrides))

	for _, ov := range overrides {
		when, _ := time.Parse(time.RFC3339, ov.When) // best-effort; zero time if unparseable
		intended, _ := json.Marshal(ov.IntendedValue)
		override, _ := json.Marshal(ov.OverrideValue)

		// UPSERT: find existing by (dc, orbId, field), update if present, insert otherwise.
		existing, err := i.db.DivergenceEntry.Query().
			Where(
				divergenceentry.DcOrbID(dc.id),
				divergenceentry.EntryOrbID(ov.OrbID),
				divergenceentry.Field(ov.Field),
			).
			Only(ctx)

		if ent.IsNotFound(err) {
			// New entry.
			_, err = i.db.DivergenceEntry.Create().
				SetDcOrbID(dc.id).
				SetEntryOrbID(ov.OrbID).
				SetField(ov.Field).
				SetTypeName(ov.Type).
				SetIntendedValue(intended).
				SetOverrideValue(override).
				SetWho(ov.Who).
				SetFirstSeenAt(when).
				SetLastSeenAt(when).
				SetLastSnapshotPublishedAt(publishedAt).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("insert %s/%s.%s: %w", dc.id, ov.OrbID, ov.Field, err)
			}
		} else if err != nil {
			return fmt.Errorf("query %s/%s.%s: %w", dc.id, ov.OrbID, ov.Field, err)
		} else {
			// Update last_seen_at and last_snapshot_published_at; preserve first_seen_at.
			// Values may have changed too if admin changed the override; type_name
			// may also have appeared if the entry was first ingested under a legacy
			// snapshot and a later snapshot includes the type.
			_, err = existing.Update().
				SetTypeName(ov.Type).
				SetIntendedValue(intended).
				SetOverrideValue(override).
				SetWho(ov.Who).
				SetLastSeenAt(when).
				SetLastSnapshotPublishedAt(publishedAt).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("update %s/%s.%s: %w", dc.id, ov.OrbID, ov.Field, err)
			}
		}
		present[ov.OrbID+"|"+ov.Field] = true
	}

	// DELETE entries for this DC that are no longer in the snapshot.
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
		if !present[e.EntryOrbID+"|"+e.Field] {
			if err := i.db.DivergenceEntry.DeleteOne(e).Exec(ctx); err != nil {
				i.logger.Warn("divergence ingester: delete stale entry failed",
					"dc", dc.id, "orbId", e.EntryOrbID, "field", e.Field, "err", err)
				continue
			}
			deleted++
		}
	}
	if deleted > 0 {
		i.logger.Info("divergence ingester: removed resolved entries",
			"dc", dc.id, "deleted", deleted)
	}
	return nil
}
