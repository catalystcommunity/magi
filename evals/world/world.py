"""Make the synthetic world of the world evaluation (README.md).

  python3 -I evals/world/world.py OUT_DIR [VERSION]

VERSION 1 (the default) makes world-v1. VERSION 2 has notes that follow
the dates and more near-miss notes (NOISE_V2); the calendar and Longhouse
data are the same.

Writes, for one person ("I") and their partner Alex, from January 2024 to
the day before the run:

  history.json    the messages that the person told Magi, with times
  calendar.json   events for catalystcal: the person's calendars and Alex's
  longhouse.json  projects, tasks, and comments for the household house

All data is synthetic. The generator is deterministic (fixed seed), and
the times are fixed dates, so a run does not depend on the day of the run,
except that nothing after END is made.
"""

import datetime as dt
import json
import random
import sys
from pathlib import Path
from zoneinfo import ZoneInfo

TZ = ZoneInfo("America/Denver")
START = dt.date(2024, 1, 2)
END = dt.date(2026, 11, 8)
rng = random.Random(20261109)


def at(d, h=20, m=0):
    """A local time on date d as Unix milliseconds."""
    if isinstance(d, str):
        d = dt.date.fromisoformat(d)
    return int(dt.datetime(d.year, d.month, d.day, h, m, tzinfo=TZ).timestamp() * 1000)


# ---------------------------------------------------------------- history

# The key facts and their changes, in time order. Each item is
# (date, hour, text). The questions (questions.json) depend on these.
KEY = [
    ("2024-01-03", 19, "New year, new notes. I'm a senior engineer at Juniper Labs and my manager is Teodora Vance."),
    ("2024-01-03", 19, "My partner is Alex. Our kids are Ruby and Theo."),
    ("2024-01-04", 20, "Ruby was born on September 3, 2016 and Theo on February 21, 2019."),
    ("2024-01-06", 10, "Our dog is Pepper, a border collie mix. Her vet is Dr. Hana Ito at Boulder Paws Clinic."),
    ("2024-01-09", 21, "We live on Pine Street in Boulder."),
    ("2024-01-15", 19, "My dentist is Dr. Ellery Shaw at Highland Dental. I go every six months."),
    ("2024-01-20", 9, "I drive a 2018 Mazda CX-5."),
    ("2024-02-01", 22, "I joined a book club called Page Turners. It meets the first Thursday of the month at Tattered Cover on Colfax."),
    ("2024-02-12", 18, "I started climbing at Movement RiNo, Tuesday and Thursday mornings at 6."),
    ("2024-03-05", 20, "At work I'm on the Atlas migration project. Nadia Kerr reports to me."),
    ("2024-03-18", 19, "Ruby plays soccer for the Highland Hornets. Her coach is Jen Park."),
    ("2024-04-02", 21, "It's my birthday today, I turned 37."),
    ("2024-04-20", 9, "Theo has swim lessons at Carla Madison Rec Center on Saturdays at 9."),
    ("2024-05-10", 20, "We're buying a house in Denver, in the Highland neighborhood. We move in June."),
    ("2024-06-15", 21, "We moved today. The new place is on Zuni Street in Highland, Denver."),
    ("2024-07-02", 19, "Pepper has a new vet now that we're in Denver: Dr. Omar Malik at Platte Street Vet."),
    ("2024-07-08", 20, "Our home wifi network is called Larkspur-5G."),
    ("2024-08-14", 18, "Home and auto insurance is with Front Range Mutual. Our agent is Doug Keller."),
    ("2024-09-27", 22, "Today is our wedding anniversary. Alex and I got married on September 27, 2014."),
    ("2024-11-12", 20, "Atlas migration is behind. The new target is October 2025."),
    ("2025-02-21", 19, "Teodora Vance left Juniper Labs to join Orbital Freight."),
    ("2025-03-03", 18, "I got promoted to engineering lead. My new manager is Rafael Okonjo."),
    ("2025-04-10", 21, "Tomorrow we fly to Porto for a week. Back on the 19th."),
    ("2025-04-19", 22, "Back from Porto. The best meal was at Cantinho do Avillez."),
    ("2025-08-04", 19, "Sven Holt joined my team today."),
    ("2025-09-15", 20, "Wen Li is a staff engineer on the platform team; she owns the build system."),
    ("2025-10-31", 18, "We finished the Atlas migration today, right on the new target."),
    ("2025-11-10", 19, "My next big project at work is the Harbor launch. It's due December 1, 2026."),
    ("2025-11-22", 14, "We sold the Mazda."),
    ("2025-12-09", 20, "I hurt my shoulder climbing. No climbing for a while."),
    ("2026-01-12", 19, "Started physical therapy with Corinne Duval, Monday mornings at 7:30."),
    ("2026-01-20", 17, "Picked up our new car today, a 2026 Kia EV9."),
    ("2026-02-05", 21, "Page Turners moved to the second Thursday of the month, and it meets at BookBar now instead of Tattered Cover."),
    ("2026-03-02", 20, "We're starting a kitchen remodel. The contractor is Ortega and Sons and the budget is $38,000."),
    ("2026-03-15", 21, "Off to Montreal for SREcon tomorrow, back on the 19th."),
    ("2026-06-01", 19, "Bea Moreau joined my team today. She's the second engineer who reports to me after Nadia, plus Sven."),
    ("2026-07-09", 20, "Kitchen remodel update: we changed the cabinets, so the budget is now $44,000."),
    ("2026-08-03", 19, "Correction: Ruby's soccer coach is Jen Parks, with an s, not Jen Park."),
    ("2026-09-08", 20, "Harbor launch slipped. The new date is January 15, 2027."),
    ("2026-09-20", 18, "Ruby moved up to the Highland Hornets U12 team this fall. Coach is still Jen Parks."),
    ("2026-10-05", 19, "Nadia Kerr moved to the data team; she doesn't report to me anymore."),
    ("2026-10-21", 20, "Alex's work schedule changed: Alex works from home on Mondays and Fridays now."),
]

# Small talk and daily notes: they make the history long, and some repeat
# key facts in other words (as people do). None of them changes a fact.
NOISE = [
    "Ruby scored two goals today for the Hornets.",
    "Theo finally swam a full length without stopping.",
    "Pepper ate a sock again. She's fine.",
    "Long day. Too many meetings.",
    "It snowed about six inches overnight.",
    "We had tacos for dinner, the kids loved it.",
    "I read most of the book club book on the train.",
    "Climbing was great this morning, sent a new V4.",
    "The Atlas migration standup ran long again.",
    "Alex made pancakes this morning.",
    "Theo lost his first tooth!",
    "Ruby has a spelling test on Friday.",
    "Need to remember to water the tomatoes.",
    "Traffic on I-25 was terrible today.",
    "We watched a movie as a family tonight.",
    "Pepper loves the dog park on 32nd.",
    "My code review queue is huge this week.",
    "Wen Li helped me debug the build cache today.",
    "Had a good one-on-one with my manager.",
    "The kitchen is a mess with the remodel going on.",
    "Ortega and Sons said the countertops arrive next week.",
    "Theo wants a birthday party at the climbing gym.",
    "Ruby wants to try out for the school play.",
    "The EV9 charges fine on the garage outlet.",
    "Physical therapy was tough today, but the shoulder feels better.",
    "Book club picked a mystery novel for next month.",
    "What's a good recipe for banana bread?",
    "How long should I boil an egg?",
    "Remind me what the weather is supposed to be tomorrow.",
    "Sven shipped his first feature today.",
    "Bea is ramping up fast.",
    "Nadia gave a great demo at the all-hands.",
    "The Harbor launch planning doc is finally done.",
    "Alex is traveling for work this week.",
    "We walked Pepper around Sloan's Lake.",
]


# Version 2: each note is true only in its window (from, to), so a note
# never says what the key facts deny (no climbing after the injury). Some
# notes are near misses: other people's facts, plans that did not happen,
# and questions, which must not become facts of the person.
NOISE_V2 = [
    ("Ruby scored two goals today for the Hornets.", "2024-03-18", None),
    ("Theo finally swam a full length without stopping.", "2024-04-20", None),
    ("Pepper ate a sock again. She's fine.", None, None),
    ("Long day. Too many meetings.", None, None),
    ("It snowed about six inches overnight.", None, None),
    ("We had tacos for dinner, the kids loved it.", None, None),
    ("I read most of the book club book on the train.", "2024-02-01", None),
    ("Climbing was great this morning, sent a new V4.", "2024-02-12", "2025-12-08"),
    ("The Atlas migration standup ran long again.", "2024-03-05", "2025-10-30"),
    ("Alex made pancakes this morning.", None, None),
    ("Theo lost a tooth!", "2025-01-01", None),
    ("Ruby has a spelling test on Friday.", None, None),
    ("Need to remember to water the tomatoes.", None, None),
    ("Traffic on I-25 was terrible today.", None, None),
    ("We watched a movie as a family tonight.", None, None),
    ("Pepper loves the dog park on 32nd.", "2024-06-15", None),
    ("We walked Pepper around Sloan's Lake.", "2024-06-15", None),
    ("My code review queue is huge this week.", None, None),
    ("Wen Li helped me debug the build cache today.", "2025-09-15", None),
    ("Had a good one-on-one with my manager.", None, None),
    ("The kitchen is a mess with the remodel going on.", "2026-03-02", None),
    ("Ortega and Sons said the countertops arrive next week.", "2026-07-01", "2026-07-25"),
    ("The EV9 charges fine on the garage outlet, just slowly.", "2026-01-20", None),
    ("Physical therapy was tough today, but the shoulder feels better.", "2026-01-12", None),
    ("Book club picked a mystery novel for next month.", "2024-02-01", None),
    ("What's a good recipe for banana bread?", None, None),
    ("How long should I boil an egg?", None, None),
    ("Sven shipped his first feature today.", "2025-08-20", "2025-09-30"),
    ("Bea is ramping up fast.", "2026-06-10", "2026-08-31"),
    ("Nadia gave a great demo at the all-hands.", "2024-04-01", "2026-09-30"),
    ("The Harbor launch planning doc is finally done.", "2025-11-20", "2026-01-31"),
    ("We walked around the Boulder farmers market.", None, "2024-06-14"),
    # Near misses.
    ("My friend Marco just bought a Mazda CX-5, same as my old one.", "2025-12-01", None),
    ("Alex's sister Dana moved to Chicago.", "2025-05-01", "2025-06-30"),
    ("Thinking about switching dentists, but probably not.", "2025-03-01", "2025-03-31"),
    ("Rafael said his own manager is Priya Raman.", "2025-04-01", None),
    ("We might get a second dog someday. Not now.", None, None),
    ("Sven's daughter is also on a soccer team, the Lakewood Lions.", "2025-09-01", None),
    ("Ruby asked if she can quit soccer. We said finish the season first.", "2026-04-01", "2026-05-31"),
    ("Theo wants a party at the climbing gym for his birthday next year.", "2025-03-01", "2025-04-30"),
    ("Is the kitchen budget usually 10 percent over? Just wondering.", "2026-03-10", "2026-06-30"),
    ("Wen Li might move to the data team next year, she said.", "2026-08-01", None),
    ("Our neighbor Pat has a border collie too.", "2024-07-01", None),
]


def history(version=1):
    items = [{"text": t, "at_ms": at(d, h), "key": True} for d, h, t in KEY]
    day = START
    while day <= END:
        # About two notes each week.
        if rng.random() < 0.29:
            if version == 1:
                text = rng.choice(NOISE)
            else:
                iso = day.isoformat()
                ok = [t for t, a, b in NOISE_V2 if (a is None or a <= iso) and (b is None or iso <= b)]
                text = rng.choice(ok)
            items.append({"text": text, "at_ms": at(day, rng.randint(7, 22), rng.choice([0, 15, 30, 45])), "key": False})
        day += dt.timedelta(days=1)
    items.sort(key=lambda x: x["at_ms"])
    return items


# --------------------------------------------------------------- calendar

def ev(title, start, end=None, *, minutes=60, rrule=None, location=None, notes=None, cal="main", all_day=False, attendees=None):
    """An event. start and end are local 'YYYY-MM-DD HH:MM' or a date."""
    e = {"title": title, "start": start, "calendar": cal}
    if end:
        e["end"] = end
    elif not all_day:
        e["minutes"] = minutes
    if all_day:
        e["all_day"] = True
    for k, v in (("rrule", rrule), ("location", location), ("description", notes), ("attendees", attendees)):
        if v:
            e[k] = v
    return e


def calendar():
    me = [
        # Work: repeating.
        ev("Team standup", "2024-01-08 09:30", minutes=15, rrule="FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", cal="Work"),
        ev("1:1 with Teodora", "2024-01-10 14:00", minutes=30, rrule="FREQ=WEEKLY;BYDAY=WE;UNTIL=20250219T235959Z", cal="Work"),
        ev("1:1 with Rafael", "2025-03-04 10:00", minutes=30, rrule="FREQ=WEEKLY;BYDAY=TU", cal="Work"),
        ev("1:1 with Nadia", "2024-03-07 15:00", minutes=30, rrule="FREQ=WEEKLY;BYDAY=TH;INTERVAL=2;UNTIL=20261001T235959Z", cal="Work"),
        ev("1:1 with Sven", "2025-08-07 15:00", minutes=30, rrule="FREQ=WEEKLY;BYDAY=TH;INTERVAL=2", cal="Work"),
        ev("1:1 with Bea", "2026-06-04 15:30", minutes=30, rrule="FREQ=WEEKLY;BYDAY=TH;INTERVAL=2", cal="Work"),
        ev("Atlas migration review", "2024-03-12 13:00", rrule="FREQ=MONTHLY;BYDAY=2TU;UNTIL=20251031T235959Z", cal="Work"),
        ev("Harbor launch sync", "2025-11-17 11:00", minutes=45, rrule="FREQ=WEEKLY;BYDAY=MO", cal="Work", location="Juniper Labs, room Aspen"),
        # Work: single.
        ev("Atlas go-live", "2025-10-31 08:00", minutes=240, cal="Work", notes="Cutover window. Wen Li on call for the build system."),
        ev("Harbor launch kickoff", "2025-11-12 10:00", minutes=90, cal="Work", location="Juniper Labs, room Aspen"),
        ev("Harbor launch review with Rafael", "2026-11-18 14:00", cal="Work", notes="Bring the new January 15 plan."),
        ev("SREcon Montreal", "2026-03-16", "2026-03-19", cal="Work", all_day=True, location="Montreal"),
        ev("Juniper Labs all-hands", "2026-11-13 16:00", cal="Work"),
        # Personal: repeating.
        ev("Climbing at Movement RiNo", "2024-02-13 06:00", minutes=90, rrule="FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20251209T235959Z"),
        ev("Page Turners book club", "2024-02-01 19:00", minutes=120, rrule="FREQ=MONTHLY;BYDAY=1TH;UNTIL=20260131T235959Z", location="Tattered Cover, Colfax"),
        ev("Page Turners book club", "2026-02-12 19:00", minutes=120, rrule="FREQ=MONTHLY;BYDAY=2TH", location="BookBar"),
        ev("Physical therapy with Corinne Duval", "2026-01-12 07:30", minutes=45, rrule="FREQ=WEEKLY;BYDAY=MO;UNTIL=20261221T235959Z"),
        ev("Anniversary", "2024-09-27", all_day=True, rrule="FREQ=YEARLY"),
        ev("Ruby's birthday", "2024-09-03", all_day=True, rrule="FREQ=YEARLY", cal="Family"),
        ev("Theo's birthday", "2024-02-21", all_day=True, rrule="FREQ=YEARLY", cal="Family"),
        ev("My birthday", "2024-04-02", all_day=True, rrule="FREQ=YEARLY"),
        ev("Ruby soccer practice", "2024-03-19 17:30", minutes=90, rrule="FREQ=WEEKLY;BYDAY=TU,TH", cal="Family", location="Highland Park field 2"),
        ev("Theo swim lesson", "2024-04-20 09:00", minutes=45, rrule="FREQ=WEEKLY;BYDAY=SA", cal="Family", location="Carla Madison Rec Center"),
        # Personal: single.
        ev("Moving day", "2024-06-15", all_day=True, cal="Family"),
        ev("Porto trip", "2025-04-11", "2025-04-19", all_day=True, cal="Family", location="Porto, Portugal"),
        ev("Flight DEN to OPO", "2025-04-11 16:40", minutes=660, cal="Family", notes="Confirmation TAP-8QX2L"),
        ev("Pick up the Kia EV9", "2026-01-20 15:00", cal="main", location="Kia of Denver"),
        ev("Kitchen remodel: cabinet install", "2026-08-17 08:00", minutes=480, cal="Family", notes="Ortega and Sons"),
        ev("Kitchen remodel: final inspection", "2026-11-20 10:00", cal="Family", notes="City inspector; Ortega and Sons on site"),
        ev("Ruby's 10th birthday party", "2026-09-05 14:00", minutes=180, cal="Family", location="Highland Park pavilion"),
        ev("Hornets end-of-season party", "2026-11-14 12:00", minutes=120, cal="Family", location="Highland Park pavilion"),
    ]
    # Dentist and vet visits (single events in the history).
    for d in ["2024-02-08", "2024-08-13", "2025-02-11", "2025-08-12", "2026-02-10", "2026-08-11"]:
        me.append(ev("Dentist, Dr. Shaw", d + " 08:30", cal="main", location="Highland Dental"))
    me.append(ev("Dentist, Dr. Shaw", "2027-02-09 08:30", cal="main", location="Highland Dental"))
    for d, who, place in [("2024-03-21", "Dr. Ito", "Boulder Paws Clinic"), ("2024-07-18", "Dr. Malik", "Platte Street Vet"),
                          ("2025-07-17", "Dr. Malik", "Platte Street Vet"), ("2026-07-16", "Dr. Malik", "Platte Street Vet")]:
        me.append(ev("Pepper vet visit, " + who, d + " 17:15", minutes=45, cal="Family", location=place))
    alex = [
        ev("Alex: yoga", "2024-01-10 18:00", minutes=75, rrule="FREQ=WEEKLY;BYDAY=WE", cal="Alex", location="CorePower Highland"),
        ev("Alex: work from home", "2026-10-26", all_day=True, rrule="FREQ=WEEKLY;BYDAY=MO,FR", cal="Alex"),
        ev("Alex: Chicago work trip", "2026-11-16", "2026-11-18", all_day=True, cal="Alex", location="Chicago"),
        ev("Alex: dentist", "2026-11-24 09:00", cal="Alex", location="Highland Dental"),
    ]
    return {"me": me, "alex": alex}


# -------------------------------------------------------------- longhouse

def longhouse():
    p = []

    def project(name, desc, tasks):
        p.append({"name": name, "description": desc, "tasks": tasks})

    def t(title, status="open", due=None, who="me", desc=None, comments=()):
        x = {"title": title, "status": status, "who": who}
        if due:
            x["due"] = due
        if desc:
            x["description"] = desc
        if comments:
            x["comments"] = list(comments)
        return x

    project("Kitchen remodel", "Ortega and Sons. Budget $44,000 after the cabinet change (was $38,000).", [
        t("Pick the cabinet style", "done", "2026-04-15"),
        t("Order the countertops", "done", "2026-07-30", "alex", comments=["Quartz, Calacatta look. Ordered 2026-07-28."]),
        t("Cabinet install", "done", "2026-08-17", comments=["Installed. Two doors need adjustment."]),
        t("Book the final inspection", "done", "2026-11-01", "alex", comments=["Booked for November 20 at 10."]),
        t("Paint the kitchen walls", "in_progress", "2026-11-15", "me"),
        t("Install the pendant lights", "open", "2026-11-18", "alex", desc="The electrician from Ortega and Sons does the wiring."),
        t("Final payment to Ortega and Sons", "open", "2026-11-25", "me", desc="After the inspection passes."),
    ])
    project("Ruby's 10th birthday party", "September 5, 2026 at the Highland Park pavilion.", [
        t("Send the invitations", "done", "2026-08-15", "alex"),
        t("Order the cake", "done", "2026-09-01", "me"),
        t("Rent the pavilion", "done", "2026-07-01", "me"),
    ])
    project("Garage cleanout", "Make room for the EV9 charger and the bikes.", [
        t("Donate the old crib", "done", "2026-02-28", "alex"),
        t("Install the wall rack for the bikes", "open", "2026-12-06", "me"),
        t("Get a quote for a 240 V outlet", "open", "2026-11-30", "me", desc="The EV9 charges slowly on the 120 V outlet."),
        t("Sort the camping gear", "cancelled", None, "alex"),
    ])
    project("Hornets end-of-season party", "November 14, 2026, Highland Park pavilion. We host.", [
        t("Collect money for the coach gift", "in_progress", "2026-11-10", "alex", comments=["11 of 14 families paid."]),
        t("Buy the coach gift for Jen Parks", "open", "2026-11-12", "me"),
        t("Bring the folding tables", "open", "2026-11-14", "me"),
    ])
    loose = [
        t("Renew the car registration for the EV9", "open", "2026-12-31", "me"),
        t("Schedule Pepper's dental cleaning", "open", "2026-11-30", "alex"),
        t("Replace the furnace filter", "done", "2026-10-15", "me"),
    ]
    return {"house": "Squizzlezig Dev", "projects": p, "loose": loose}


def main():
    out = Path(sys.argv[1])
    out.mkdir(parents=True, exist_ok=True)
    h = history(int(sys.argv[2]) if len(sys.argv) > 2 else 1)
    (out / "history.json").write_text(json.dumps({"tz": str(TZ), "messages": h}, indent=1) + "\n")
    (out / "calendar.json").write_text(json.dumps(calendar(), indent=1) + "\n")
    (out / "longhouse.json").write_text(json.dumps(longhouse(), indent=1) + "\n")
    print(f"{len(h)} messages ({sum(m['key'] for m in h)} key), {sum(len(v) for v in calendar().values())} calendar series or events")


if __name__ == "__main__":
    main()
