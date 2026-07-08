package divergence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/armada/orbital/internal/blobstore"
	"github.com/armada/orbital/internal/orb/store"
	"github.com/armada/orbital/internal/orb/store/pendingoverride"
	"github.com/armada/orbital/internal/orb/store/publishedreport"
)

// OverrideEntry is a single field-level divergence between orbital's intent and
// a locally observed override. This is the canonical format orb accepts and
// orbital displays.
type OverrideEntry struct {
	OrbID         string `json:"orbId"`
	Field         string `json:"field"`
	Type          string `json:"type,omitempty"` // orbital GraphQL type name (e.g. "Server"); empty for legacy producers
	IntendedValue any    `json:"intendedValue"`
	OverrideValue any    `json:"overrideValue"`
	Who           string `json:"who"`
	When          string `json:"when"`
}

// Report is the published divergence state — the full set of currently pending
// overrides at the time of publish. Written to S3 as a single JSON file.
type Report struct {
	PublishedAt string          `json:"publishedAt"`
	Overrides   []OverrideEntry `json:"overrides"`
}

// PublishRecord tracks the last successful publish. Used purely for UI
// display — "last published at X to key Y." Orb does not gate publishes on
// it; orbital's ingester is the only side with authority to decide whether a
// report represents a state change (see `internal/divergenceingest/store.go`
// applyReport — same content as existing entries short-circuits, content
// differing from existing supersedes). Adding an orb-side dedup hides
// recurrences of identical drift after orbital has already resolved them.
type PublishRecord struct {
	PublishedAt time.Time `json:"publishedAt"`
	S3Key       string    `json:"s3Key"`
}

// PublishHistoryRow is one row returned by LoadPublishHistory. Includes the
// extra fields needed for the publish-history UI beyond PublishRecord's
// display-only pair. Entries is the decoded summary_json (the actual field-
// level divergences at publish time); empty when the row predates summary
// capture or the publish had zero entries.
type PublishHistoryRow struct {
	PublishedAt time.Time       `json:"publishedAt"`
	S3Key       string          `json:"s3Key"`
	DCOrbID     string          `json:"dcOrbId,omitempty"`
	EntryCount  int             `json:"entryCount"`
	Status      string          `json:"status"`
	Entries     []OverrideEntry `json:"entries,omitempty"`
}

// Store manages divergence reports in orb's SQLite database.
type Store struct {
	db *store.Client
}

// NewStore returns a Store backed by the given ent client. Data is persisted
// in the orb SQLite database.
func NewStore(db *store.Client) *Store {
	return &Store{db: db}
}

// Save replaces the current set of pending override entries. Snapshot
// semantics: prior rows are deleted and the new set inserted in one
// transaction, matching the pre-migration current.json overwrite behavior.
func (s *Store) Save(entries []OverrideEntry) error {
	ctx := context.Background()
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("divergence store begin tx: %w", err)
	}
	if _, err := tx.PendingOverride.Delete().Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("divergence store delete: %w", err)
	}
	for i := range entries {
		e := entries[i]
		iv, err := marshalValue(e.IntendedValue)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("divergence store marshal intended[%d]: %w", i, err)
		}
		ov, err := marshalValue(e.OverrideValue)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("divergence store marshal override[%d]: %w", i, err)
		}
		if _, err := tx.PendingOverride.Create().
			SetTypeName(e.Type).
			SetEntryOrbID(e.OrbID).
			SetField(e.Field).
			SetIntendedValue(iv).
			SetOverrideValue(ov).
			SetWho(e.Who).
			SetWhenStr(e.When).
			SetFirstSeenAt(time.Now().UTC()).
			Save(ctx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("divergence store insert[%d]: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("divergence store commit: %w", err)
	}
	return nil
}

// Load returns the current set of pending override entries. Returns empty
// slice if none are held. Rows are returned in first_seen_at ascending order
// (stable snapshot ordering, matches what tests assert).
func (s *Store) Load() ([]OverrideEntry, error) {
	ctx := context.Background()
	rows, err := s.db.PendingOverride.Query().
		Order(store.Asc(pendingoverride.FieldFirstSeenAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("divergence store load: %w", err)
	}
	entries := make([]OverrideEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, OverrideEntry{
			OrbID:         r.EntryOrbID,
			Field:         r.Field,
			Type:          r.TypeName,
			IntendedValue: unmarshalValue(r.IntendedValue),
			OverrideValue: unmarshalValue(r.OverrideValue),
			Who:           r.Who,
			When:          r.WhenStr,
		})
	}
	return entries, nil
}

// SavePublishRecord appends a new record of a successful publish. Unlike the
// pre-migration overwrite semantics, this now preserves history — the publish
// history endpoint surfaces every row. Callers that have the actual entries
// on hand should use SavePublishRow so summary_json gets populated.
func (s *Store) SavePublishRecord(rec PublishRecord) error {
	ctx := context.Background()
	return s.savePublishRow(ctx, rec, "", nil)
}

// SavePublishRow appends a new publish record with the extra fields carried
// by the publish-history feature (dcOrbId, entryCount, entries). Entries are
// stored as JSON in summary_json so the publish-history UI can render the
// per-entry detail without a round-trip to blob storage. Callers that don't
// know the DC (e.g. legacy schedulers) should use SavePublishRecord.
func (s *Store) SavePublishRow(rec PublishRecord, dcOrbID string, entries []OverrideEntry) error {
	ctx := context.Background()
	return s.savePublishRow(ctx, rec, dcOrbID, entries)
}

func (s *Store) savePublishRow(ctx context.Context, rec PublishRecord, dcOrbID string, entries []OverrideEntry) error {
	create := s.db.PublishedReport.Create().
		SetDcOrbID(dcOrbID).
		SetPublishedAt(rec.PublishedAt).
		SetS3Key(rec.S3Key).
		SetEntryCount(len(entries)).
		SetStatus(publishedreport.StatusPublished)
	if len(entries) > 0 {
		summary, err := json.Marshal(entries)
		if err != nil {
			return fmt.Errorf("divergence store marshal summary: %w", err)
		}
		create = create.SetSummaryJSON(string(summary))
	}
	if _, err := create.Save(ctx); err != nil {
		return fmt.Errorf("divergence store publish insert: %w", err)
	}
	return nil
}

// LoadPublishRecord returns the most recent publish record, or nil if none
// exists. Preserves the pre-migration contract for callers that only want to
// know "when did we last publish, and to what key" — the divergence scheduler
// uses this to decide whether a run was missed.
func (s *Store) LoadPublishRecord() (*PublishRecord, error) {
	ctx := context.Background()
	row, err := s.db.PublishedReport.Query().
		Order(store.Desc(publishedreport.FieldPublishedAt)).
		First(ctx)
	if store.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("divergence store load publish: %w", err)
	}
	return &PublishRecord{PublishedAt: row.PublishedAt, S3Key: row.S3Key}, nil
}

// LoadPublishHistory returns published_reports rows newest-first, optionally
// filtered by dcOrbID. limit ≤ 0 returns everything; limit > 0 caps the page.
// Also returns the total row count matching the filter (before pagination) so
// callers can render "N of M".
func (s *Store) LoadPublishHistory(dcOrbID string, limit, offset int) ([]PublishHistoryRow, int, error) {
	ctx := context.Background()
	q := s.db.PublishedReport.Query()
	if dcOrbID != "" {
		q = q.Where(publishedreport.DcOrbID(dcOrbID))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("divergence store publish count: %w", err)
	}
	q = q.Order(store.Desc(publishedreport.FieldPublishedAt))
	if offset > 0 {
		q = q.Offset(offset)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("divergence store publish history: %w", err)
	}
	out := make([]PublishHistoryRow, 0, len(rows))
	for _, r := range rows {
		row := PublishHistoryRow{
			PublishedAt: r.PublishedAt,
			S3Key:       r.S3Key,
			DCOrbID:     r.DcOrbID,
			EntryCount:  r.EntryCount,
			Status:      string(r.Status),
		}
		if r.SummaryJSON != "" {
			// Decode failures leave Entries nil — the row still renders, just
			// without the expand-detail. Individual publish rows shouldn't
			// blow up the whole page.
			_ = json.Unmarshal([]byte(r.SummaryJSON), &row.Entries)
		}
		out = append(out, row)
	}
	return out, total, nil
}

// marshalValue encodes an any (from JSON intake) back to compact JSON text so
// it round-trips through TEXT columns without losing type information. Nil
// stays empty — the DB representation of "no value" is an empty string.
func marshalValue(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// unmarshalValue decodes what marshalValue wrote. Empty string → nil (no
// value). Malformed JSON falls back to the raw string — pre-migration data
// may have stored plain scalars that got escaped when re-read; treating them
// as strings preserves visibility instead of erroring.
func unmarshalValue(s string) any {
	if s == "" {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return s
	}
	return out
}

// Publisher writes divergence reports to S3-compatible or Azure Blob storage.
// The choice of backend is hidden behind blobstore.Store.
type Publisher struct {
	store   blobstore.Store
	ociRepo string // used as path prefix, e.g. "orbital/colo-galleon"
}

type PublisherConfig struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	OCIRepo   string
}

func NewPublisher(ctx context.Context, cfg PublisherConfig) (*Publisher, error) {
	store, err := blobstore.New(ctx, blobstore.Config{
		Endpoint:  cfg.Endpoint,
		Region:    cfg.Region,
		Bucket:    cfg.Bucket,
		AccessKey: cfg.AccessKey,
		SecretKey: cfg.SecretKey,
	})
	if err != nil {
		return nil, fmt.Errorf("divergence publisher: %w", err)
	}
	return &Publisher{store: store, ociRepo: cfg.OCIRepo}, nil
}

// Ping verifies the configured blob store is reachable with current credentials.
// Used by the orb UI's "Test Connection" button on the divergence report page.
func (p *Publisher) Ping(ctx context.Context) error {
	return p.store.Ping(ctx)
}

// Publish writes a report of the given entries to storage and returns the key.
// Overwrite-in-place at a stable key per repoPath (see path.go). Repeated
// publishes replace prior contents rather than accumulating timestamped
// objects — the Terraform remote-state pattern.
func (p *Publisher) Publish(ctx context.Context, entries []OverrideEntry) (string, error) {
	snap := Report{
		PublishedAt: time.Now().UTC().Format(time.RFC3339),
		Overrides:   entries,
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", fmt.Errorf("divergence publish marshal: %w", err)
	}

	key := ReportKey(p.ociRepo)
	if err := p.store.Put(ctx, key, bytes.NewReader(b), "application/json"); err != nil {
		return "", fmt.Errorf("divergence publish put: %w", err)
	}
	return key, nil
}
