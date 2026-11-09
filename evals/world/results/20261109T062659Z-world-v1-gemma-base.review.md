# Review: world-v1 gemma-base (run Sun 8 Nov 2026, 23:27 MST)

The scorer reports 20/28 and 3/3 checks. My judgment is about **13 correct, 6 partly correct, 9 wrong**. w01 is wrong but passed. The busy steps show no tool calls, so the causes below come from the notes and the replies.

## Per item

| id | Truth (local Sun 8 Nov) | Reply | Auto | Cause |
|---|---|---|---|---|
| t01 | Rafael | correct | agree | Could be luck: Rafael is also the current manager |
| t02 | Ito, Boulder Paws | correct | agree | Lucky: no Pepper→Malik note exists, so "Pepper vet Ito" is still the current fact |
| t03 | Mazda CX-5 | wrong (abstain) | agree | "Sold the Mazda" **retracted** the claim. The history is gone |
| t04 | Tattered Cover → BookBar | partly: no BookBar | over-scored | |
| t05 | 9 Dec 2025, shoulder | partly: **10 Dec** | over-scored | 20:00 MST = 03:00Z. **UTC date** |
| t06 | Teodora (Wed, until 19 Feb 2025) | wrong: "I don't have a record…" | **false pass** | No calendar-history look-up |
| k01 | Sven, Bea | correct | agree | Lucky: neither "Nadia reports to me" nor "Nadia moved" was recorded |
| k02, k03 | 15 Jan 2027; Jen Parks | correct | agree | |
| k04 | Bea, 1 Jun 2026 | partly: **2 Jun** | over-scored | UTC date |
| k05–k08 | Wen; Avillez; Mon+Fri; 12 years | correct | agree | |
| k09 | turns 8 (21 Feb 2027) | wrong: 7 | agree | Arithmetic |
| c01 | 9–15 Nov: PT, Harbor sync Mon; Rafael Tue; Sven 1:1 + book club Thu 12; all-hands Fri; swim + Hornets Sat 14. Alex WFH Mon/Fri; Chicago starts Mon 16 | partly: invented "Alex WFH Mon, Thu, Sun; Chicago Sunday" | over-scored | Invented detail |
| c02 | Thu 12, BookBar | correct | agree | |
| c03 | twice (10 Feb, 11 Aug) | wrong (abstain) | agree | No past-range look-up |
| c04 | Tue 9 Feb 2027 08:30 | wrong: "none next week" | agree | Look-up window too short |
| c05 | standup 9:30, all-hands 16:00 | partly (no standup) | agree | |
| c06 | Fri 20 Nov 10:00 | wrong: abstains, asks the user | agree | No calendar or Longhouse look-up |
| c07 | Ruby soccer Tue 17 Nov 17:30 | wrong: "none" | agree | Family calendar or multi-day span failed |
| l01 | $44k (was $38k). Paint walls (me, 15 Nov), pendants (Alex, 18 Nov), inspection 20 Nov, final payment (me, 25 Nov) | partly: budget only. Offered to check Longhouse | agree | Did not call Longhouse on a mixed question |
| l02 | Collect money (Alex, 11/14 paid, due **Tue 10**), gift (me, Thu 12), tables (me, Sat 14) | partly: "due **yesterday**"; Alex named "**the admin**" | over-scored | Relative-date error; Longhouse user mapping |
| l03 | rack 6 Dec, 240 V quote 30 Nov | correct | agree | |
| e01 | Mon 9: standup, Harbor sync (the set expects Tue: Rafael) | **correct** for the actual date (also lists PT from the main calendar) | **false fail** | Set's date assumption |
| a01, a02 | abstain | correct | agree | |
| w01 | Sat **14** Nov, Alex calendar | created Sat **7 Nov** (yesterday). Check: "Alex **had**…" | **false pass** | "This Saturday" resolved in the ISO week Mon 2–Sun 8. The check made the same error |
| w02 | 1SU monthly from Sun 6 Dec 19:00, Family | 6 Dec right. RRULE and calendar not verified | weak pass | |
| w03 | pump, Garage, me, Fri 13/20 (ambiguous) | 13 Nov, defensible. Assignee not verified | weak pass | |

Note-taker defects (in the import file):
- No record for: the move to Zuni St, Pepper's vet change, Teodora leaving, "Nadia reports to me", Nadia leaving, the Atlas finish, the shoulder injury, SREcon, U12.
- The Alex WFH note **overwrote "Jamie works-at engineering lead"** (a subject mix-up).
- Garbage claims: "Atlas … name October 2025", "returns 19th", "shipped-first-feature today".
- The user name "Jamie" is invented.

**Date assumption:** on Sunday, e01's expected answer (Rafael) is wrong. w03's "next Friday" and l02's "due" wording change. The run exposed w01's ISO-week bug.

## (1) Scoring weaknesses and better checks
- No abstain penalty on answerable questions (t06). Fail when an abstain_word is present.
- t04: require "bookbar".
- t05, k04: require the date; add `none: ["december 10"]` or `["june 2"]`.
- c01: an any-group per event lets invented facts pass. Score each event with its day, and add `none` traps for the Alex days.
- c02: "thursday" passes "first Thursday". Require "12" + "bookbar" and add `none: ["tattered"]`.
- c04: "9" matches "9:30". Use `feb(ruary)? 9|2027-02-09`.
- c06: "friday" and "20th" are loose. Use `nov(ember)? 20`.
- t01: the current manager passes. Add a question whose answer is Teodora.
- l01: "44" passes without the increase. Require "38".
- l02: require "alex" with "collect". l03: add `none: ["camping"]`.
- k01/k02 `none_unless` words ("not ", "was") are too generic.
- Actions: model-run checks repeat the model's errors. Read catalystcal and Longhouse directly and assert the start, the calendar, the RRULE (w02: also 3 Jan and 7 Feb), the assignee, and the due date.
- `busy_seen` measures nothing. Log the tool calls and the ranges queried.
- Noise contradicts the facts, although the generator says it changes none. Examples: climbing after Dec 2025, Bea in 2024, EV9 in 2024, Sven in Jan 2024, the shoulder and PT in 2024, Atlas after Oct 2025, the remodel in 2024. Make noise date-aware.
- The actions mutate the world (the pump task), so a rerun changes l03. Reset the world for each run.

## (2) Top system problems (testable)
1. **The note taker destroys or skips changes over time.** "Sold" retracts the claim instead of ending it, and departures are dropped. Test: "Who's Pepper's vet now?" (expect the wrong answer Ito). "When did we sell the Mazda?"
2. **Updates match on predicate, not subject.** Test: "What's my job title?" (expect the wrong answer).
3. **Note dates are in UTC.** Test: "When did we move?" (expect 16 Jun; the truth is 15 Jun).
4. **Calendar look-ups use a short forward window and no past ranges** (c03, c04, c06, t06). Test: "When was Pepper's last vet visit?" Log the range.
5. **"This <weekday>" uses the ISO week**, so on a Sunday it gives a past date. Test: run w01 with the clock pinned to Sunday, then to Monday.
6. **Mixed questions skip Longhouse, and its users show as account names.** Test: "Who's installing the pendant lights?" (expect Alex).
7. **Multi-day all-day events and the Family filter fail.** Test: "What's on the family calendar Tue 17 Nov?"
8. Weak age arithmetic. Test: Ruby's next birthday (11), and the user's age now (39).

## (3) Missing questions
- "Where did we live when Theo started swimming?" (Boulder)
- "Who was my manager when I joined book club?" (Teodora)
- "How many book-club meetings at BookBar this year?" (10)
- "How many Denver vet visits has Pepper had?" (3)
- "Did I meet Nadia 1:1 after she changed teams?" (no; last on 1 Oct)
- "How old was Ruby when we moved?" (7)
- "Who was on my team at Atlas go-live?" (Nadia, Sven)
- "When does PT end?" (21 Dec)
- "Am I free during Alex's dentist appointment?" (24 Nov)
- "What did we do in April 2025?" (Porto)
- "When and how did the kitchen budget change?" (9 Jul, $38k → $44k)
- "What's overdue?" (nothing on 8 Nov)

## Next Actions
- Pin "now" for each run, or recompute the expected answers. Until then, e01 and w01 cannot be trusted.
- Verify the actions against catalystcal and Longhouse directly.
- Add the abstain penalty and the tighter regexes. The real score is about 13/28.
- Fix the note taker first (problems 1–3). It drops changes, overwrites the wrong subject, and stores UTC dates, and these cause most of the time-question failures.
- Make the noise date-aware, and reset the world for each run.
