# Spike 37 — Parse GraphQL with a real AST

**Status:** Not started — open question under investigation.

*Moved out of `docs/planning/backlog.md` 2026-09-15: the entry had grown past what a
tracker line can carry. The backlog keeps the question; this doc keeps the reasoning.
Fold decisions into the relevant `docs/reference/<DOMAIN>.md` when this closes, then
delete this file.*

---

**The question:** Should the `/graphql` proxy parse mutations with a GraphQL AST parser rather than the hand-rolled regex + string matching it uses today?

**Promoted from `debt.md` 2026-09-14 — it has outgrown a debt row.** The proxy understands every mutation through pattern matching: `knownMutationRe`, `extractOperations`, `stripCommentsAndStrings`, `casFilterRe`, plus the selector/patch resolution added 2026-09-14 (`orbIdEqVarRe`, `orbIdInRe`, `filterVarRe`, `listItemRe`, `setVarRe`). **Why it is now load-bearing, not cosmetic:** the approval gate derives the governing namespace from what these expressions can read, so a miss is no longer a missing audit row — it is a mutation the gate cannot classify. Mitigated by failing closed (unreadable → refused), which is a guard rail, not a fix, and which has already produced one **false refusal** shipped to callers: a perfectly valid variable-form mutation was refused `400 VARIABLE_FORM_REQUIRED` for a year because its variable was not spelled `orbId`.

**What a parser buys, concretely.** Every regex above answers a question an AST answers structurally: *which mutations does this document call* (field selections on the mutation root), *which row does it select* (the `filter` argument's value node), *which variable carries the patch* (the `set` argument's `Variable` node name), *where do I splice the CAS predicate* (an argument node, edited and re-printed — no string splicing, no `$`-expansion hazard). It also removes the whole class of shape the write path refuses today **only because it cannot read it**: an `in:` list, a filter behind a variable, a compound mutation. Those are legitimate GraphQL that orbital 400s.

**Scope:** evaluate `vektah/gqlparser/v2` (**needs dependency sign-off** — already an indirect dep via several GraphQL libs; confirm before assuming) against schema-aware validation vs parse-only. Parse once in `Handle`, thread the document through `writeToDGraph` instead of re-matching per consumer. **The seam already exists:** the six hardcoded lookups were funnelled into `resolveWriteSelector` + `resolveSetMap` on 2026-09-14, so the swap replaces two function bodies rather than six scattered call sites. **Net:** deletes ~8 regexes, the comment-stripping pass, and the inline-selector guard's reason for existing.

**Risks:** it touches the single most load-bearing path in the product (`Handle` → every mutation), and a parser that rejects what the regexes accepted is an outage. **Do it behind the existing tests as the net** — `graphql_cas_test.go`, `graphql_variable_resolution_integration_test.go` and `approval_gate_integration_test.go` between them pin the accepted shapes, the refused shapes, and the reasons. Consider running both implementations in parallel and logging disagreements before switching over.
