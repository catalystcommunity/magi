# Review lessons in the repository, imported into the knowledge graph at start

Review lessons as base knowledge: keep the lessons of code reviews in the repository, and put them into the knowledge graph of each installation when magi-server starts. The repository is the source of truth; the knowledge graph is a copy that Magi can search. Production gets the same lessons from the same release.

1. A new file magi/compose/baseknowledge/review-lessons.md. Format: a short header paragraph that says what the file is and how to add a lesson, then one section for each lesson:

   ## rl-0001: <title>
   Area: <path or topic>
   Source: <run id> (review <date>)

   <the lesson: one to four sentences, ASD-STE100>

   Put these three lessons in it (rewrite them in ASD-STE100 if necessary):
   - rl-0001 "Keep the state of one run in the run method". Area: primitives/apprentice. Source: run_DnUog7zOwB5NxHpymwkz (review 2026-11-14). Flags and counters that belong to one run are set at the start of run(), not in __init__. An object can run more than once. Add a test with two runs on one object.
   - rl-0002 "No helper for one expression". Area: all code. Source: run_DnUog7zOwB5NxHpymwkz (review 2026-11-14). Do not add a function that only wraps one expression, such as limit * 0.8. Write the expression where it is used. A name must add meaning.
   - rl-0003 "No renames that the task did not ask for". Area: all code. Source: run_e3YKczzyVxNChcfkMPpI (review 2026-11-14). Do not rename existing functions or variables to use them from a new place. Use the existing name, or make the rename a separate task. A rename makes the diff larger and harder to review.

2. Embed the directory in magi-server (go:embed in a new file magi/compose/baseknowledge.go). Parse the sections into lessons (id, title, area, source, text). An id is "rl-" and four digits and is unique; a section with a bad header or a duplicate id is an error that a unit test finds (test the embedded file too, so a bad file fails tools/check.sh).

3. Import at start: a method (*System).ImportBaseKnowledge(ctx) that magi/cmd/magi-server/main.go calls once after the clients are ready, in a goroutine, with a retry (the knowledge service can start later than magi). For each lesson, call KnowledgeService put-entity with kind "lesson", label = title, summary = text, properties {"lesson_id": id, "area": area, "source": source}, external {system: "magi-repo", locator: "magi/compose/baseknowledge/review-lessons.md#<id>"}, owners ["service:magi"], with the service context of the System (AsService). When the returned entity has a different label, summary, or properties than the file, call change with operation "fix" and set the new values. Then find the lessons in the knowledge graph (search kind "lesson", external system "magi-repo", owner service:magi) that are not in the file any more, and retract them (change operation "retract", reason "removed from review-lessons.md"). Read primitives/knowledge (contracts/knowledge.csil, primitives/knowledge/README.md) for what the operations accept; use only operations that exist. Log one line: "base knowledge" with the counts (added, fixed, retracted, unchanged).

4. Tests in magi/compose: the parser (good file, bad header, duplicate id, the embedded file); the import against a fake KnowledgeService through the real HTTP transport (the pattern of other compose tests): first import adds three, a second import changes nothing, a changed text gives a fix, a removed lesson gives a retract.

5. Docs (ASD-STE100): a section "Review lessons" in docs/knowledge.md (what they are, where they live, when they are imported, how a reviewer adds one), and one line in AGENTS.md under "Rules" or a new short section: "Read magi/compose/baseknowledge/review-lessons.md: lessons from earlier code reviews."

Verify: cd magi && GOWORK=off go test -count=1 ./compose/ ./cmd/... Then ./tools/check.sh.
