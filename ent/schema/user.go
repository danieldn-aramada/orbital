package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// User holds the schema definition for the User entity.
type User struct {
	ent.Schema
}

// Fields of the User.
func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("email").NotEmpty().Unique(),
		field.String("name").NotEmpty(),
		field.String("preferred_username").NotEmpty(),
		field.String("password_hash").Sensitive().Optional().Nillable(),
		field.Bool("verified").Default(false),
		field.Enum("role").Values("readonly", "dev", "admin").Default("readonly"),
		// issuer records which identity provider last resolved this user, and is
		// how the UI knows a role is provider-owned (Spike 26 mode B) and must be
		// shown read-only rather than as an edit the next login would discard.
		//
		// Distinct from usernamePrefix, which namespaces identities so two
		// providers cannot collide on one row. Collision and provenance are
		// different questions: a single-provider deployment legitimately has no
		// prefix and still needs to know who owns the role.
		//
		// Nillable: rows predating this, and local password accounts, have none.
		field.String("issuer").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

// Edges of the User.
func (User) Edges() []ent.Edge {
	return nil
}
