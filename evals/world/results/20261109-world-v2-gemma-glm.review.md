# Review: world-v2, gemma and glm (run Mon 9 Nov 2026, America/Denver)

Runs:
- `20261109T164904Z-world-v2-gemma.json`: persona model gemma-4-12b.
- `20261109T170120Z-world-v2-glm.json`: persona model glm-5.3.

Both runs use the same knowledge graph. The note taker (gemma-4-12b) made it in one import. The import log is `20261109-import-world-v2-gemma.jsonl` (326 messages). `20261109-import2-world-v2-gemma.jsonl` is a later, partial import (70 messages). The runs did not use it.

I checked each reply against `world.py` (KEY, `calendar()`, `longhouse()`), the import log, and `magi/compose/work.go`.

Marks: **WP** = wrong pass (the scorer passed a reply that is wrong or partly wrong). **WF** = wrong failure (the scorer failed a correct reply). **CNF** = "I could not finish my answer" (token budget).

## 1. Per item

| id | Truth | gemma | glm |
|---|---|---|---|
| t01 | Rafael (since 3 Mar 2025; Porto 11–19 Apr 2025) | wrong: Teodora. The graph has the change; the model did not use the date | right |
| t02 | Teodora | right | right |
| t03 | Dr. Ito, Boulder Paws | right | right |
| t04 | Dr. Malik | right | right |
| t05 | 2018 Mazda CX-5, sold 22 Nov 2025 | right ("stopped driving" on 22 Nov: the graph has an end, not a sale) | **wrong, WP**: "Marco bought it on 1 December 2025, so that's when it left the family". Marco bought his own CX-5 |
| t06 | BookBar now; Tattered Cover before | right | right |
| t07 | 9 Dec 2025, shoulder | right | right |
| t08 | Teodora (Wed, last 19 Feb 2025) | right | right (gives the last date) |
| t09 | 15 Jun 2024 | right | wrong: CNF |
| t10 | Boulder (Pine St) | right | **partly, WP**: "that's still your home". They live on Zuni St, Denver |
| t11 | Nadia, Sven | **partly, WP**: adds Wen Li. Wen Li is on the platform team | wrong: CNF |
| t12 | Porto 11–19 Apr 2025 | **partly, WP**: Porto "April 10th through April 11th"; birthday "April 1st" (truth: 2 Apr) | **partly, WP**: trip "starting Thursday, April 10"; birthday "on the 1st". Both errors come from the all-day bug (section 4, T1) |
| t13 | 9 Jul 2026, $38k to $44k | right | right |
| k01 | Sven, Bea | right | **wrong, WP**: adds "Priya Raman (her manager is you, since July 2025)". Priya is Rafael's manager |
| k02 | 15 Jan 2027 | right | right |
| k03 | Jen Parks | right | right |
| k04 | Bea, 1 Jun 2026 | right | right |
| k05 | Wen Li | right | right |
| k06 | Cantinho do Avillez | right | right |
| k07 | Mon, Fri | right | right |
| k08 | Engineering lead, Juniper Labs | wrong: "You work at Orbital Freight", no title | **wrong, WP**: "engineering lead at Orbital Freight ... since February 2025". "juniper" matched in "Before the move you were ... at Juniper Labs" |
| k09 | 12 years | right | right (13th anniversary Mon 27 Sep 2027 is correct) |
| k10 | 8, Sun 21 Feb 2027 | right | **partly, WP**: answer right, but it says "the family calendar has the birthday event on Saturday the 20th, likely the celebration day". The 20th is the all-day bug; the explanation is invented |
| k11 | 7 | wrong: 8 (arithmetic) | right |
| k12 | 39 | wrong: 37 (graph: "birthday 37") | wrong: CNF |
| n01 | EV9 | right | right |
| n02 | No, Dr. Shaw | right | right |
| n03 | No, platform team | right | right |
| n04 | No, U12 | right | right |
| c01 | Mon: PT, standup, Harbor sync; Alex WFH. Tue: standup, Rafael, soccer. Wed: standup, Alex yoga. Thu: standup, Sven 1:1, soccer, book club. Fri: standup, all-hands; Alex WFH. Sat: swim, Hornets party. Sun: nothing | **partly, WP**: "Sunday: Alex: work from home; Alex: Chicago work trip". Both are Monday 16 items, one day early. Alex WFH on Mon 9 and Fri 13 is missing | **partly, WP**: all of the person's events are right. "Alex flies out [Sunday] for a work trip": the trip starts Mon 16; the day comes from the bug |
| c02 | Thu 12 Nov, BookBar | right | right |
| c03 | Twice (10 Feb, 11 Aug) | right | right (with dates) |
| c04 | Tue 9 Feb 2027 | right | right |
| c05 | Standup, all-hands | right | right |
| c06 | Fri 20 Nov 10:00 | right | right |
| c07 | Ruby soccer Tue 17 Nov (Chicago Mon 16–Wed 18) | wrong: "Alex is in Chicago on Sunday, November 15th ... no events" (all-day bug) | wrong: CNF |
| c08 | Mon 21 Dec 2026 | right | right |
| c09 | Thu 16 Jul 2026 | right | right |
| c10 | Three | **partly, WP**: the reply is cut: "Pepper has had 3 vet". 32 s | right (with all three dates) |
| c11 | No; the last 1:1 was 1 Oct 2026 | **right, WF**: "No ... after she moved on October 5th". The extra (Bea 1:1 on 8 Oct) is also true. The check requires "October 1" | right |
| c12 | Not free: standup 9:30–9:45 (Rafael 1:1 starts at 10:00) | **right, WF**: "commitment from 09:30 to 09:45". It does not name the event | right. Small error: "the rest of that hour is spoken for" (9:45–10:00 is free) |
| l01 | $44k (was $38k); spend unknown; paint (me, 15 Nov), pendants (Alex, 18 Nov), final payment (me, 25 Nov) | partly: "on budget at $44,000" has no support (no spend data); tasks right; no assignees | **right, WF**: says the spend is not known, gives $44k "since the cabinet change", all tasks, the inspection. Failed for no "38" and for "no record" in the first sentence |
| l02 | Collect money (Alex, due Tue 10 Nov, 11/14 paid); gift (me, Thu 12); tables (me, Sat 14) | partly: "was due yesterday (Nov 10)", "due tomorrow (Nov 12)": wrong relative days; assignees "My Admin", "My User" | partly: all right, but the assignee is "My Admin" (system fault T2) |
| l03 | Rack 6 Dec, 240 V quote 30 Nov | right | right |
| l04 | Alex, 18 Nov | partly: date right; "the electrician from Ortega and Sons" (from the description) | partly: same; adds "wiring and installation" |
| l05 | Nothing is overdue | wrong: "Collect money ... was due yesterday, November 10th" | right. It also reports a stray "CLI smoke task A" (due 20 Sep) in the house, a test leftover |
| e01 | Standup, Rafael 1:1 | wrong: "no events on your work calendar" | right |
| e02 | Wed 18 Nov 14:00, bring the January 15 plan | right | right |
| a01–a03 | Abstain | right | right |
| w01 | Alex cal, Sat 21 Nov 08:00 MST, Carla Madison | right (verified) | right (verified). Good note: Theo's lesson is at 9 the same morning |
| w02 | Family, `FREQ=MONTHLY;BYDAY=1SU`, 6 Dec 19:00 MST | right (verified) | right (verified) |
| w03 | Task "bike pump", Garage cleanout, me, due 20 Nov | right for title and due. The project is not verified (the task JSON has no project field) | same as gemma |
| w04 | Alex cal, Wed 11 Nov 17:30 MST | right (verified) | wrong: CNF, no event |

## 2. Real scores

| | Auto | Right | Partly | Wrong | Real (right + ½ partly) | WP | WF |
|---|---|---|---|---|---|---|---|
| gemma questions | 39/51 | 37 | 7 | 7 | **40.5/51** | 4 (t11, t12, c01, c10) | 2 (c11, c12) |
| glm questions | 44/51 | 38 | 6 | 7 | **41/51** | 7 (t05, t10, t12, k01, k08, k10, c01) | 1 (l01) |
| gemma actions | 4/4 | 4 | 0 | 0 | 4/4 (w03 project not verified) | | |
| glm actions | 3/4 | 3 | 0 | 1 | 3/4 | | |

- glm without the 5 CNF: 38 right of 46 answers. Only 3 answers are wrong in content (t05, k01, k08), and all 3 repeat a bad graph claim.
- The automatic score puts glm 5 points ahead. The real gap is 0.5. The scorer passes glm's long answers because a long answer contains the needed words and also wrong claims.
- The gemma summary is not consistent: `"check": "4/4"` but `by_kind["direct check"]: [0, 4]`. Someone rescored the summary by hand and did not update `by_kind`.

## 3. Model comparison

**glm is better at:**
- Time reasoning on the graph: t01 (Rafael at Porto time), t08 (gives the end date 19 Feb 2025), c11 (last 1:1 on 1 Oct, 4 days before the move).
- Arithmetic: k11 (7, with the reason), k09 (the next anniversary day is correct).
- Relative days: l02 "due tomorrow" (Tue 10). gemma acts as if today is Wed 11 (l02, l05).
- Honesty about unknowns: l01 ("no record of what's been spent"); l05 (it marks the smoke task as a test artifact).
- Tool use: e01 (Work calendar on Tue 10). gemma said "no events".
- Useful extras: w01 (conflict with the 9:00 swim lesson), c06 (final payment after the inspection).

**gemma is better at:**
- Short answers that do not repeat bad graph claims. On k01 gemma did not add Priya. On t05 it did not add Marco. On t10 it did not add "still your home".
- Reliability: no CNF. glm had 5 CNF (t09, t11, k12, c07, w04).
- Speed: median 7.0 s, p90 15.9 s, max 32.2 s. glm: median 9.0 s, p90 21.6 s, max 33.4 s. The glm CNF replies took 13.7–32.8 s and gave nothing.

**Busy signal:** 55/55 in both runs, but this number does not show much. Every turn shows "reading the message" and "thinking". A tool step showed in only 5/55 gemma turns (t09, n01, t12, l05, w02) and 2/55 glm turns (t02, c11). w01, w03, and w04 created items with no tool step shown. Many turns show "waiting for its turn", so another job holds the persona queue.

**Robustness with the graph:** neither model is robust. Both trust each claim that they get. gemma uses fewer claims, so fewer graph faults reach the user, but it makes more reasoning errors. glm reasons well over correct claims. It also adds each claim that it finds, including bad ones (Priya, Orbital Freight "since February 2025", Marco, Pine Street). It also invents a reason when a tool output is strange (k10 "celebration day"). It does not check a claim against other evidence: in t10 glm keeps Pine Street as the home, although the same context shows the move to Denver.

**Verdict:** use glm as the persona model after the token budget fix, and measure again. Its errors come from the graph and the tools; gemma's errors come from the model itself. The graph faults are now the limit for both models. Keep gemma as the fast local fallback for simple look-ups.

## 4. System problems (not model problems), ranked

### Tool and context problems

**T1. All-day events show one day early, and a multi-day event shows only its first day.** `describeOcc` (`magi/compose/work.go`) converts `o.Start` to Denver time. For an all-day occurrence, `o.Start` is at UTC midnight, so it shows the day before. The line also shows no end day. Only `find_events` and the single-event part of `calendarContext` parse `StartDate` in the zone. `calendar_events` and the occurrences in `calendarContext` do not. This causes t12 (both), c01 (both), c07 (gemma), and k10 (glm).
- Test: "When is Alex's Chicago trip?" Expected wrong answer: "Sunday 15 November". Truth: Mon 16 to Wed 18 Nov.
- Test: "What day is my birthday on the calendar this year?" Expected wrong answer: 1 April.

**T2. Longhouse shows members by their account name ("My Admin", "My User"), not "Alex".** l02 and l04 fail in both runs. In l04 both models took "the electrician" from the description because no person is named.
- Test: "Which tasks does Alex have open?" Expected wrong answer: "none", or tasks of "My Admin". Truth: collect money (10 Nov), pendant lights (18 Nov), Pepper's dental cleaning (30 Nov).

**T3. Replies end without a full answer, with no error.** glm: 5 CNF (the budget fix is not yet measured). gemma c10: the reply stops at "Pepper has had 3 vet" after 32 s. The scorer passed it.
- Test: run c10 and c07 five times. Expected wrong result: at least one cut reply or CNF.

**T4. The progress signal does not show most tool calls.** The actions changed catalystcal and Longhouse, but the activity showed "putting it on the calendar" only once (gemma w02). The 1 s poll can miss short steps, or the steps are not emitted after the first round.
- Test: run w01 and compare the server tool log with the activity steps. Expected wrong result: no tool step in the activity.

**T5. The world house has data from other tests.** "CLI smoke task A" (due 20 Sep 2026) is in "Squizzlezig Dev". `magi-world -clean` removes only the action results.
- Test: l05. Expected wrong answer from a weaker model: "Yes, CLI smoke task A is overdue".

### Note-taker and import problems (from `20261109-import-world-v2-gemma.jsonl`)

**N1. A statement about another person becomes a fact of the person (wrong subject).** This is the worst graph fault, because each case gives a confident wrong answer.
- n138 "Teodora Vance left Juniper Labs to join Orbital Freight" ended "Jamie works-at Juniper Labs" and recorded "Jamie works-at Orbital Freight" and "Jamie job Orbital Freight" (k08 in both runs).
- n170 "Rafael said his own manager is Priya Raman" recorded "Priya Raman manager Jamie" (glm k01).
- n216 "Marco just bought a Mazda CX-5, same as my old one" recorded "Marco bought **2018** Mazda CX-5", the person's car entity (glm t05).
- n316 "Alex works from home on Mondays and Fridays" recorded "Alex works-at home" twice.
- Test: "Does Priya Raman report to me?" Expected wrong answer: "yes". Truth: no; she is Rafael's manager.
- Test: "Where do I work?" Expected wrong answer: Orbital Freight. Truth: Juniper Labs.

**N2. Key changes have no record.** n55 "We moved today ... Zuni Street in Highland, Denver" recorded nothing, so "Jamie lives-in Pine Street in Boulder" is still current. n142 (promotion) recorded the manager but not the title "engineering lead". n153 (Avillez) and n307 (U12) recorded nothing. The models found Avillez and U12 from the raw messages, so the message search hides some of these gaps.
- Test: "Where do we live now?" Expected wrong answer: "Pine Street in Boulder". Truth: Zuni Street, Highland, Denver, since 15 Jun 2024.

**N3. A noise message or a change overwrites a value with a bad value.** n225 "The Harbor launch planning doc is finally done" replaced "Harbor launch project-due December 1, 2026" with "project-due done". From Dec 2025 to Sep 2026 the graph had no due date. n312 made Nadia's manager the entity "data team". n100 "Atlas target October 2025" replaced "Jamie project Atlas migration project". n94 "Jamie neighbor Pat" replaced "Pat has border collie".
- Test: "What was the Harbor launch due date in June 2026?" Expected wrong answer: "done" or "no date". Truth: 1 Dec 2026.
- Test: "Who is Nadia's manager now?" Expected wrong answer: "the data team". Truth: not known; not the person.

**N4. Ages become birthdays.** n32 "I turned 37" recorded "Jamie birthday 37" (gemma k12). The partial import2 records "April 2, 1987". That is better, but measure it again.
- Test: k12 "How old am I?" Expected wrong answer: 37. Truth: 39.

**N5. Relative dates in a message are not resolved.** n148 "Tomorrow we fly to Porto" recorded an event on 2025-04-10 (truth: 11 Apr). n246 "Off to Montreal ... tomorrow" recorded 2026-03-15 (truth: 16 Mar).
- Test: "What day did we fly to Porto?" Expected wrong answer: 10 April 2025.

**N6. Facts attach to the wrong thing.** "Jamie budget $38,000" attaches to the person, not to the kitchen remodel. "Wen Li works-at staff engineer on the platform team" uses a role as an employer. "Corinne Duval physical-therapy Monday mornings" uses the therapist as the subject.
- Test: "What's my budget?" Expected wrong answer: "$44,000". Truth: the question is not clear; ask which budget.

Import totals: 39 of 42 key messages recorded something. 38 of 284 noise messages became claims or events (for example, 9 "swam a full length" events and 8 "lost a tooth" events).

## 5. Scoring weaknesses in questions-v2 and run.py, and fixes

1. **`re_all` matches words in a "before" clause.** k08 passed with "Orbital Freight" because "juniper" is in "Before the move ...". Fix: k08 `re_none: ["orbital"]`.
2. **No traps for the known graph faults.** Fix: k01 `re_none: ["priya"]`; t11 `re_none: ["wen"]`; t05 `re_none: ["marco (bought|has) (it|your)", "left the family"]`; t10 add a question "Where do we live now?" with `re_none: ["pine", "boulder(?!.*(before|used to|was))"]`.
3. **Dates in a passed reply are not checked.** t12 and c01 passed with dates one day early. Fix: t12 `re_all: ["april 11|11 april"]`, `re_none: ["april 10", "april 1(st)?\\b"]`. c01: one `re_all` for each day ("monday.*harbor", "tuesday.*rafael", "saturday.*swim"), and `re_none: ["sunday[^\\n]*(work from home|chicago)"]`.
4. **A cut reply can pass.** c10 passed with "Pepper has had 3 vet". Fix: run.py marks a reply `truncated` when it does not end with `.`, `!`, `?`, or a list item, and fails it. Count CNF as a separate error class, not as a wrong answer, and ask again once.
5. **Some checks are too strict.** c11 needs "October 1", but "No" is the answer. Fix: `re_any: [["\\bno\\b", "didn't", "did not"]]`, `re_none: ["^yes"]`, and make the date a separate, optional "detail" score. c12 needs the name "standup". Fix: accept `standup|stand-up|9:30|09:30`.
6. **The refusal rule fails a correct partial abstain.** l01 failed on "no record of what's actually been spent". The question has a part that nobody knows (the spend). Fix: use the refusal rule only when the first sentence has no `re_all` match. Better: split l01 into two checks, the budget (`44`, and `38|cabinet|raised|went up`) and the tasks. Add `re_none: ["on budget at"]` against the unsupported claim.
7. **`EXCUSE` is too broad.** "now", "current", "after", and "joined" excuse a `re_none` match anywhere in its sentence. "The Harbor launch is now due December 1" passes k02. "You drive the Mazda now" passes n01. Fix: give each question its own excuse words. Remove "now", "current", "currently", "after", and "joined" from the global list. Require the excuse word within about 6 words of the match.
8. **Action checks do not check all fields.** w03 checks the title and the due date only. Fix: `magi-world -extras` prints the project name of a task (from the project task list) and the assignee name. Then expect `project: "garage"` and `assignee: "<person>"`. w02: also check that "with Alex" gives an attendee, or decide that it does not need one.
9. **The rescored summary is edited by hand.** Fix: add `run.py --rescore FILE`. It recomputes `ok`, `summary`, and `by_kind` from `log` with the current scorer, and it writes the scorer's git commit in the file.
10. **`busy_seen` passes without tool steps.** Fix: count the tool steps (not "reading", "thinking", "putting the answer together") and compare them with a server tool log for each turn.
11. **One run for each model.** Fix: run each question 3 times, and report the mean and the items that change.
12. **The set has regular expressions only, not the truth.** A reviewer must work out each answer again. Fix: add a `truth` field to each question.
13. **Noise repeats one-time events.** "Theo finally swam a full length" appears 9 times, "Theo lost a tooth" 8 times, and the Priya note 7 times. This is not natural, and the repeated near misses push the note taker more than real use does. Fix: in `NOISE_V2`, mark one-time notes and use each one only once.
14. **Abstain questions do not trap invention.** a02 or a03 passes with "I haven't seen it, but it's 9AB-123". Fix: `re_none: ["\\d{3,}"]`. Also, "haven't" in `abstain_words` is too general.
15. **The world is not clean.** Fix: give the world its own house, or make `-clean` remove each task and event that the world files do not have.

## 6. Missing questions for years-long use

Truth from `world.py`. Today is Mon 9 Nov 2026.

| Question | Expected answer | Kind |
|---|---|---|
| Where do we live now? | Zuni Street, Highland, Denver (since 15 Jun 2024) | change |
| Where do I work? | Juniper Labs | subject |
| Does Priya Raman report to me? | No; she is Rafael's manager | near miss |
| Who has my old Mazda? | Not known; Marco bought his own CX-5 | near miss |
| Where does Alex work? | Not known (Alex works from home Mon and Fri) | subject |
| What was the Harbor launch due date in June 2026? | 1 Dec 2026 | time |
| How much did Harbor slip? | From 1 Dec 2026 to 15 Jan 2027: 45 days | change |
| How long did the Atlas migration take? | Mar 2024 to 31 Oct 2025, about 20 months; target moved to Oct 2025 in Nov 2024 | time |
| Who did I have 1:1s with in March 2025? | Rafael (Tue, from 4 Mar), Nadia (every other Thu). Teodora ended 19 Feb 2025 | calendar+time |
| When was my last 1:1 with Teodora? | Wed 19 Feb 2025 | calendar+time |
| Who was my manager when we got the EV9? | Rafael (EV9 on 20 Jan 2026) | multi-hop |
| How old was Theo when we went to Porto? | 6 | date math |
| When is my next birthday, and how old will I be? | Fri 2 Apr 2027; 40 | date math |
| What day did we fly to Porto? | Fri 11 Apr 2025, 16:40, TAP | time+calendar |
| Was I away at SREcon during one of Theo's swim lessons? | No: SREcon was Mon 16–Thu 19 Mar 2026; lessons are on Saturdays | calendar+calendar |
| How many book club meetings have there been at BookBar? | 9 (12 Feb to 8 Oct 2026); the 10th is Thu 12 Nov | calendar count |
| How many PT sessions are left? | 6 (16, 23, 30 Nov; 7, 14, 21 Dec) | calendar count |
| What was the kitchen budget when the cabinets went in? | $44,000 (changed 9 Jul; install 17 Aug) | time |
| Which tasks does Alex have open? | Collect money (10 Nov, in progress), pendant lights (18 Nov), Pepper's dental cleaning (30 Nov) | longhouse |
| Does Alex have a task due while in Chicago? | Yes: pendant lights, due Wed 18 Nov; Chicago is Mon 16–Wed 18 | calendar+tasks |
| What do I need to do before Alex leaves for Chicago? | Buy the coach gift (Thu 12), bring the tables (Sat 14), paint the walls (Sun 15) | calendar+tasks |
| When can I pay Ortega and Sons? | After the inspection on Fri 20 Nov 10:00; due 25 Nov | calendar+tasks |
| How many families paid for the coach gift, and who is it for? | 11 of 14; Jen Parks | longhouse comments |
| Did Ruby's coach change when she moved up to U12? | No, Jen Parks (only the spelling was fixed, Aug 2026) | change |
| When is Alex's Chicago trip? | Mon 16 to Wed 18 Nov | calendar (T1 trap) |

## 7. Next Actions

1. **Fix the all-day display (T1).** In `describeOcc`, `calendar_events`, and `calendarContext`, use the event's `StartDate` and `EndDate` for an all-day occurrence, and show the end day for a span. It is a small code change, and it causes 5 wrong replies in two models (t12, c01, c07, k10). It will also cause wrong plans later.
2. **Fix the wrong-subject notes (N1) and the missing changes (N2) in the note taker.** These cause the confident wrong answers: Orbital Freight, Priya, Marco, Pine Street. Test with the traps in section 6. Each fault cost glm a wrong pass, and the scorer did not see it.
3. **Stop noise from replacing a value (N3).** Do not let an update replace a date with a word ("done"). Do not let a change of team make a team a manager. Keep a change chain that has the old value.
4. **Show Longhouse members by their names (T2), and measure again.** l02 and l04 cannot pass until this is fixed.
5. **Tighten the scorer (section 5, items 1–7) before the next run.** Make a questions-v3, because the README rule forbids a change to v2. The automatic score now hides 7 glm errors and gives glm a lead of 5 points; the real lead is 0.5.
6. **Run glm again with the token budget fix, 3 times.** Count CNF and cut replies apart from wrong answers. Then decide on the persona model. The current data favors glm.
7. **Clean the world house (T5), and record the tool calls for each turn (T4).** Without a tool log, the review cannot separate "no tool call" from "wrong tool result" (for example, gemma e01).
8. **Re-import with the current note taker** (import2 is partial) and run both models on the same new graph. The birthday fix (N4) and the vet change are in question in import2: n59 there recorded only "Dr. Omar Malik works-at Platte Street Vet" and not the vet change.
