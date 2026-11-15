# Review lessons

This file keeps the lessons of code reviews. The repository is the source of
truth. When magi-server starts, it copies each lesson into the knowledge
graph of that installation, so that Magi can search it. Production gets the
same lessons from the same release.

To add a lesson, add a section at the end. Use the next free id: "rl-" and
four digits. Write a title, the area, and the source (the run id of the
review and its date). Then write the lesson in one to four short sentences.
Do not remove a lesson: retract it in the knowledge graph by removing the
section, and write a new lesson when a lesson changes.

## rl-0001: Keep the state of one run in the run method

Area: primitives/apprentice
Source: run_DnUog7zOwB5NxHpymwkz (review 2026-11-14)

Set the flags and counters of one run at the start of the run method, not in
the constructor. An object can run more than once. Add a test that runs one
object two times.

## rl-0002: No helper for one expression

Area: all code
Source: run_DnUog7zOwB5NxHpymwkz (review 2026-11-14)

Do not add a function that only wraps one expression, such as a limit that
the code multiplies by 0.8. Write the expression where the code uses it. A
name must add meaning.

## rl-0003: No renames that the task did not ask for

Area: all code
Source: run_e3YKczzyVxNChcfkMPpI (review 2026-11-14)

Do not rename an existing function or variable to use it from a new place.
Use the existing name, or make the rename a separate task. A rename makes
the diff larger and harder to review.
