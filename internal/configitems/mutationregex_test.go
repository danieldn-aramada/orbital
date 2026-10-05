package configitems

import (
	"context"
	"testing"
	"time"
)

// The mutation matcher gates three things: whether the approval gate runs,
// whether an audit event is recorded, and which type a before-fetch reads. Its
// two failure modes are wildly asymmetric, and that asymmetry is the design:
//
//   - MISSING a type is catastrophic and silent — an ungated write, and an audit
//     hole nobody can see.
//   - MATCHING too much is harmless — the audit path runs only on mutations
//     DGraph ACCEPTED, so the type exists; the gate only ever refuses or passes.
//
// So it must never under-match, including when the schema is unreadable.
func TestMutationRegex_DerivesFromTheDeployedSchema(t *testing.T) {
	f := sample() // Server, DataCenter, Rack, IdracSettings
	r := NewResolver(f, time.Minute).WithViews(sampleViews(t), "", "")

	re := r.MutationRegex(context.Background())
	for _, op := range []string{"addServer", "updateDataCenter", "deleteIdracSettings", "UpdateRack"} {
		if !re.MatchString(op) {
			t.Errorf("%s is in the deployed schema and must match", op)
		}
	}
	// A type the DEPLOYED schema does not declare does not match — which is the
	// precision a hand-maintained list could only approximate.
	if re.MatchString("updateStorageDevice") {
		t.Error("updateStorageDevice is not in this deployed schema and must not match")
	}
	// Prefix anchoring: `addServerThing` is a different mutation.
	if re.MatchString("addServerThing") {
		t.Error("the match must be word-anchored, or a longer name would be audited as a shorter one")
	}
}

// A new type in the DEPLOYED schema is gated and audited with no release.
//
// This is the failure the hand-maintained list could not cover: it was keyed to
// the SHIPPED schema while the gate operates on the deployed one, so a
// deployment whose DGraph carried an extra ConfigItem type got ungated,
// unaudited writes on it — and no build-time test can see that, because the
// drift is between an environment and a binary.
func TestMutationRegex_FollowsTheSchemaWithoutARelease(t *testing.T) {
	f := sample()
	now := time.Now()
	r := NewResolver(f, time.Minute).WithViews(sampleViews(t), "", "")
	r.now = func() time.Time { return now }
	ctx := context.Background()

	if r.MutationRegex(ctx).MatchString("updatePvBackup") {
		t.Fatal("precondition: PvBackup is not in the schema yet")
	}

	f.sdl = "type Server { hostname: String } type PvBackup { enabled: Boolean }"
	f.types["PvBackup"] = TypeInfo{Fields: []DerivedField{{Name: "enabled", Editable: true}}}
	now = now.Add(2 * time.Minute)

	if !r.MutationRegex(ctx).MatchString("updatePvBackup") {
		t.Error("a type added to the deployed schema must be gated and audited with no code change")
	}
}

// Unreadable schema: the matcher degrades to over-matching, never to silence.
func TestMutationRegex_DegradesToOverMatchingNotSilence(t *testing.T) {
	f := sample()
	f.err = errContextCanceled
	r := NewResolver(f, time.Minute).WithViews(sampleViews(t), "", "")

	re := r.MutationRegex(context.Background())
	for _, op := range []string{"updateServer", "addPvBackup", "deleteSomethingNobodyDeclared"} {
		if !re.MatchString(op) {
			t.Errorf("with no schema, %s must still match — an unrecognised mutation has to be "+
				"gated and audited, not waved through", op)
		}
	}
	// It is still a MUTATION matcher: a read must not be mistaken for a write,
	// or every query would drag the approval gate and an audit write behind it.
	//
	// `updatedAt` is the case that bites. With a case-INSENSITIVE type half it
	// parses as `update` + `dAt` and matches — and `updatedAt` is in the
	// selection set of essentially every query, so every read would have become
	// an audited, gated "mutation".
	for _, notAMutation := range []string{
		"queryServer", "getServer",
		"{ queryServer { orbId updatedAt createdAt } }",
		"{ getDataCenter(orbId: \"x\") { updatedBy } }",
	} {
		if re.MatchString(notAMutation) {
			t.Errorf("%q is a READ and must not match — matching it would run the approval "+
				"gate and the audit pipeline on every query", notAMutation)
		}
	}
}

var errContextCanceled = context.Canceled
