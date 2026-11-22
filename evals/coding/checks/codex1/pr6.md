Acceptance checks

1. "A new file magi/compose/baseknowledge/review-lessons.md." "Put these three lessons in it (rewrite them in ASD-STE100 if necessary):"
   Result: Read the embedded file. The opening paragraph identifies the file and explains how to add a lesson. Compare the three sections against the task for exact IDs, titles, areas, and source lines. Each section has one to four sentences that retain all instructions from its lesson.
   Seam: New parser test in `magi/compose/baseknowledge_test.go`.

2. "Embed the directory in magi-server (go:embed in a new file magi/compose/baseknowledge.go)." "Parse the sections into lessons (id, title, area, source, text)." "An id is \"rl-\" and four digits and is unique; a section with a bad header or a duplicate id is an error that a unit test finds (test the embedded file too, so a bad file fails tools/check.sh)."
   Result: Parse the embedded file with the same parser as the fixtures. With a preamble and a section `## rl-0042: Use the existing name`, `Area: all code`, `Source: run_test (review 2026-11-14)`, and body `Use the existing name.`, compare those five values without the field prefixes. Replace the header with `## rl-042: Use the existing name` and expect an error. Repeat the valid ID with a different title and expect an error.
   Seam: New parser in `magi/compose/baseknowledge.go` and new `baseknowledge_test.go`.

3. "Import at start: a method (*System).ImportBaseKnowledge(ctx) that magi/cmd/magi-server/main.go calls once after the clients are ready, in a goroutine, with a retry (the knowledge service can start later than magi)."
   Result: Start with knowledge unavailable and workflow available. The server can serve `/healthz`. When knowledge becomes available, the import completes once. The same path runs with `-workers=false`.
   Seam: `run` in `magi/cmd/magi-server/main.go`, `compose.New` in `magi/compose/clients.go`, and new `ImportBaseKnowledge`.

4. "For each lesson, call KnowledgeService put-entity with kind \"lesson\", label = title, summary = text, properties {\"lesson_id\": id, \"area\": area, \"source\": source}, external {system: \"magi-repo\", locator: \"magi/compose/baseknowledge/review-lessons.md#<id>\"}, owners [\"service:magi\"], with the service context of the System (AsService)."
   Result: With an empty graph, compare three decoded requests field for field against the sections and mapping above. The verified subject is `service:magi`.
   Seam: New `ImportBaseKnowledge` and the HTTP fixture in the Plan. Use `System.AsService` from `clients.go`.

5. "When the returned entity has a different label, summary, or properties than the file, call change with operation \"fix\" and set the new values."
   Result: Return `ent_42` with stale values. For the fixture in check 2, compare one `ChangeRequest` for `ent_42` with operation `fix` and values `label` = `Use the existing name`, `summary` = `Use the existing name.`, `property.lesson_id` = `rl-0042`, `property.area` = `all code`, and `property.source` = `run_test (review 2026-11-14)`.
   Seam: New import path and HTTP fixture.

6. "Then find the lessons in the knowledge graph (search kind \"lesson\", external system \"magi-repo\", owner service:magi) that are not in the file any more, and retract them (change operation \"retract\", reason \"removed from review-lessons.md\")."
   Result: Remove `rl-0003` from the input after the first import. After the two puts, expect a search and one retract of the stored entity for `rl-0003`, with the exact reason above. Include visible lessons from another system and another owner. Neither receives a change.
   Seam: New import path and HTTP fixture.

7. "Log one line: \"base knowledge\" with the counts (added, fixed, retracted, unchanged)." "Tests in magi/compose: the parser (good file, bad header, duplicate id, the embedded file); the import against a fake KnowledgeService through the real HTTP transport (the pattern of other compose tests): first import adds three, a second import changes nothing, a changed text gives a fix, a removed lesson gives a retract."
   Result: Compare one log per pass. In the stated order, expect `(3,0,0,0)`, `(0,0,0,3)`, `(0,1,0,2)`, `(0,0,1,2)`. The second pass sends zero changes.
   Seam: New `baseknowledge_test.go`, checks 2 and 4–6.

8. "Docs (ASD-STE100): a section \"Review lessons\" in docs/knowledge.md (what they are, where they live, when they are imported, how a reviewer adds one), and one line in AGENTS.md under \"Rules\" or a new short section: \"Read magi/compose/baseknowledge/review-lessons.md: lessons from earlier code reviews.\""
   Result: Inspect both files for the specified heading, four topics, and exact line.
   Seam: `docs/knowledge.md`, `AGENTS.md`.

9. Implied: Preflight performs zero knowledge writes. Retraction keeps history.
   Seam: `preflight` in `main.go`; `ChangeRequest.Erase` stays unset.

Plan

1. Add the Markdown and embedded parser in the specified paths.
2. In new `ImportBaseKnowledge`, parse before RPCs. Search before puts to count existing locators. Use `AsService`, `ptr`, and generated `Search`, `PutEntity`, and `Change`. Search again for removals.
3. In `main.go` `run`, start a goroutine before the workers branch. Follow the existing five-second retry and cancellation pattern.
4. In new `baseknowledge_test.go`, follow `graphSystem` in `graph_test.go` for signed credentials, `rpc.Mux`, and `httptest.NewServer`. Use `KnowledgeServiceOps` and `NewKnowledgeClient` with a new stateful service fake. Reuse the generated transport.
5. Add the docs. Run `(cd magi && GOWORK=off go test -count=1 ./compose/ ./cmd/...)`, then `./tools/check.sh`.

Traps: `SearchRequest` has no owner or external filter. Filter returned entities locally. Search defaults to 50 and caps at 500. `PutEntity` returns no creation flag and updates timestamps on matches. `fix` uses `property.NAME`, with an empty value to remove an extra property.