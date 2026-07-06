package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// PublishedReport is a historical record of one divergence report orb
// published to S3. New — the reason for this migration. Append-only.
type PublishedReport struct {
	ent.Schema
}

func (PublishedReport) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("dc_orb_id"),
		field.Time("published_at").Default(time.Now),
		field.String("s3_key"),
		field.String("s3_endpoint").Optional(),
		field.Int("entry_count").Default(0),
		field.Enum("status").
			Values("published", "superseded").
			Default("published"),
		field.Text("summary_json").Optional(),
	}
}

func (PublishedReport) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("dc_orb_id", "published_at"),
	}
}

func (PublishedReport) Edges() []ent.Edge {
	return nil
}
