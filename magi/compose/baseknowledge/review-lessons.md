# Review lessons

This file keeps the lessons of code reviews. The repository is the source of
truth. When magi-server starts, it copies each lesson into the knowledge
graph of that installation, so that Magi can search it. Production gets the
same lessons from the same release.

To add a lesson, add a section at the end. Use the next free id: "rl-" and
four digits. Write a title, the area, and the source (the run id of the
review and its date). Then write the lesson in one to four short sentences.
To change a lesson, change its text: the next start fixes the copy in the
knowledge graph. To retract a lesson, remove its section: the next start
retracts the copy. Do not reuse an id.

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

## rl-0004: A diff for review shows new files

Area: primitives/apprentice, magi/runner
Source: run_yhW2tjw7Ap8YNproHNE0 (review 2026-11-14)

git diff does not show untracked files. In the same command as git diff, run
git add --intent-to-add --all before it and git reset -q after it. The reset
is necessary: entries that stay in the index break git stash. Do not add an
exec for this when the task asks for one exec.

## rl-0005: A test of an error fails for the reason that it names

Area: all code
Source: run_WSRXhief4PHJIEeoWMd0 (review 2026-11-14)

An error test must reach the code path that it names, and check the error
text. A test input that fails for an other reason passes and tests nothing.

## rl-0006: A test calls the code that it tests

Area: all code
Source: coding eval runs run_QcvQXqxVAJLInUH-35sn and run_yeHz6X31YeA34f-h4o0v (review 2026-11-15)

A test must call the real function or step, for example Runner.work or
run_task. Do not write a copy of its logic in the test. A test of a copy
passes when the code is wrong. If the real code is hard to call, use the
fakes that the other tests of the package use.

## rl-0007: One helper for a repeated block

Area: all code
Source: coding eval runs run_ZxwL_MGQ99CRMRUjhj7- and run_QcvQXqxVAJLInUH-35sn (review 2026-11-15)

When the same block of several lines appears two or more times, put it in
one helper or one constant. A message text and a usage sum are examples.
This lesson is about a block of lines, and rl-0002 is about one expression.

## rl-0008: Keep the columns of command output

Area: all code
Source: coding eval runs run_4YA8WjIpQmBGrUtn_btn and two more runs of the same task (review 2026-11-22)

Do not remove the space at the start of command output when the columns
have a meaning. A line " M file" of git status --short is a different
state from "M file". Remove only the space at the end (rstrip in Python,
TrimRight in Go). Test with an input that has a space in the first column.

## rl-0009: A new model call goes the same way as the other model calls

Area: primitives/apprentice
Source: coding eval runs run_ZI7vpPWbEAFoBMXvFF7_ and three more runs of the same task (review 2026-11-22)

When a change adds a model call, the call counts its tokens in the usage of
the result and records its answer in the transcript, as the model call of
the run loop does. A failed call keeps the text that the result had before
the call. Test the usage, the transcript, and the failed call.

## rl-0010: Test each number of the task at the number and past it

Area: all code
Source: coding eval runs of the same task with the label v86 (review 2026-11-22)

When the task text gives a number (a limit of characters, a count of lines,
a step), write a test with an input past that number and a test at that
number. Check the exact result: the length of the cut text, the step of the
note. A test with a small input does not reach the limit.
