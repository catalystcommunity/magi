"""Make note-taker cases (README.md): the notes that Magi shows the note
taker, one message of the person, and the items that a right answer has.

  python3 -I evals/notetaker/gen.py OUT_DIR [--n PER_CATEGORY] [--seed S]
          [--split NAME] [--per-file N]

Writes OUT_DIR/<split>-000.jsonl, ... with --per-file cases each. The
generator is deterministic for a seed. Use one seed for the test split and
other seeds for training data, so a model never trains on a test case.

A case:
  id, category, when (local time, RFC 3339), self (the person's name),
  notes (the text of the notes, the format of describeKnowledge), text,
  expect: items that must be in the answer; each has op, and some of
  subject, relation (a list of allowed names), object, value (text that
  the field must contain), id, also;
  forbid: items that must not be in the answer (the same fields);
  allow_extra: true when other items do not make the answer wrong.
"""

import argparse
import datetime as dt
import json
import random
from pathlib import Path

FIRST = ["Jamie", "Morgan", "Riley", "Casey", "Jordan", "Avery", "Quinn", "Rowan", "Sasha", "Taylor", "Robin", "Drew",
         "Elliot", "Harper", "Kai", "Logan", "Noa", "Parker", "Reese", "Skyler"]
OTHER = ["Teodora", "Rafael", "Nadia", "Sven", "Bea", "Wen", "Marco", "Priya", "Hiro", "Ingrid", "Luis", "Mara", "Olga",
         "Tomas", "Uma", "Viktor", "Zoe", "Amara", "Bruno", "Celia", "Dario", "Esme", "Felix", "Greta", "Hassan", "Imani",
         "Joel", "Kira", "Lars", "Mina", "Omar", "Pia", "Rhea", "Sol", "Tariq", "Vera", "Yara", "Ari", "Ben", "Cleo"]
LAST = ["Vance", "Okonjo", "Kerr", "Holt", "Moreau", "Li", "Rossi", "Raman", "Sato", "Lindqvist", "Garza", "Petrov",
        "Abara", "Brandt", "Castillo", "Dunn", "Eklund", "Foster", "Haas", "Iqbal", "Jensen", "Kowalski", "Nakamura", "Osei"]
ORGS = ["Juniper Labs", "Orbital Freight", "Copperleaf", "Northwind Health", "Bluepeak Energy", "Kestrel Bank",
        "Larch Software", "Meridian Foods", "Pinecone Analytics", "Quarry Robotics"]
CLINICS = ["Highland Dental", "Pearl Dental", "Cherry Creek Dental", "Elm Clinic", "Riverside Family Medicine", "Oak Street Clinic"]
VETS = ["Platte Street Vet", "Boulder Paws Clinic", "Sloan Animal Hospital", "Mile High Vets"]
STREETS = ["Pine Street", "Zuni Street", "Elm Avenue", "Maple Court", "Juniper Road", "Aspen Lane", "Birch Way"]
CITIES = ["Boulder", "Denver", "Lakewood", "Golden", "Aurora", "Fort Collins", "Longmont"]
CARS = ["2018 Mazda CX-5", "2026 Kia EV9", "2021 Subaru Outback", "2019 Honda Civic", "2023 Toyota RAV4", "2020 VW Golf"]
GYMS = ["Movement RiNo", "CorePower Highland", "the YMCA on Colfax", "Earth Treks Golden"]
INSURERS = ["Front Range Mutual", "Summit Insurance", "Clearwater Assurance"]
PETS = [("Pepper", "dog"), ("Biscuit", "dog"), ("Miso", "cat"), ("Juniper", "cat"), ("Captain", "dog")]
TEAMS = ["the data team", "the platform team", "the mobile team", "the security team", "the payments team"]
WIFI = ["Larkspur-5G", "Saguaro-Guest", "Bluebird", "Quiet-Owl"]
SMALL = ["Long day. Too many meetings.", "It snowed about six inches overnight.", "We had tacos for dinner, the kids loved it.",
         "Traffic on I-25 was terrible today.", "We watched a movie as a family tonight.", "Alex made pancakes this morning.",
         "My code review queue is huge this week.", "The kitchen is a mess.", "Pepper ate a sock again. She's fine.",
         "Feeling tired today.", "Great run this morning!", "The coffee machine at work broke again."]
QUESTIONS = ["What's a good recipe for banana bread?", "How long should I boil an egg?", "Who is my dentist?",
             "When is book club this month?", "What's the weather tomorrow?", "Where do we live?", "What car do I drive?",
             "How old is Theo?", "Can you summarize my week?"]
REQUESTS = ["Remind me to call {o} tomorrow.", "Put the swim meet on {o}'s calendar on Saturday at 8.",
            "Add a task to buy a bike pump by Friday.", "I need to renew my passport.", "Add milk to my shopping list.",
            "Schedule a dentist appointment for next month.", "Set up a monthly budget review with {o}."]
PLANS = ["We might get a second dog someday. Not now.", "Thinking about switching dentists, but probably not.",
         "Maybe we'll move to {city} in a few years.", "I'd like to learn to climb one day.", "{o} might move to {team} next year, she said."]


# Training data uses all phrasings except the last of each list; the test
# split uses all, so it also shows how a model does on unseen wording.
TRAIN = False


def pick(r, options):
    """One phrasing (the last one is held out of training data)."""
    return r.choice(options[:-1] if TRAIN and len(options) > 1 else options)


class W:
    """A household: the person, people, things, and their facts as notes."""

    def __init__(self, r):
        self.r = r
        self.self = r.choice(FIRST)
        self.n = 0
        self.ents = {}   # label -> (id, kind, aliases)
        self.facts = []  # (claim id, subject label, relation, other, note)

    def eid(self, label, kind="person", aliases=None):
        if label not in self.ents:
            self.n += 1
            self.ents[label] = ("ent_" + self.code(), kind, aliases or [])
        return self.ents[label][0]

    def code(self):
        return "".join(self.r.choice("ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz0123456789") for _ in range(20))

    def fact(self, subj, rel, other, kind="thing", since=None):
        self.eid(self.self, "person", ["me", "myself"])
        self.eid(subj, "person" if subj == self.self else kind)
        cid = "clm_" + self.code()
        self.facts.append((cid, subj, rel, other, since))
        return cid

    def person(self):
        return self.r.choice(OTHER) + " " + self.r.choice(LAST)

    def notes(self):
        """The text of the notes (describeKnowledge): each thing, then its facts."""
        if not self.facts:
            return ""
        by = {}
        for f in self.facts:
            by.setdefault(f[1], []).append(f)
        out = []
        order = [self.self] + [k for k in self.ents if k != self.self]
        for label in order:
            eid, kind, aliases = self.ents[label]
            line = f"- [{eid}] {label} ({kind})"
            if aliases:
                line += ", also called " + ", ".join(aliases)
            out.append(line)
            for cid, subj, rel, other, since in by.get(label, []):
                note = f" (since {since})" if since else ""
                out.append(f"  - [{cid}] {subj} {rel} {other}{note}")
        return "\n".join(out)


def day(r):
    d = dt.date(2024, 1, 1) + dt.timedelta(days=r.randint(0, 1000))
    return dt.datetime(d.year, d.month, d.day, r.randint(7, 22), r.choice([0, 15, 30, 45]),
                       tzinfo=dt.timezone(dt.timedelta(hours=-7)))


def since(r):
    d = dt.date(2023, 1, 1) + dt.timedelta(days=r.randint(0, 700))
    return d.strftime("%-d %B %Y")


def me():
    return "me"


# Each maker gives (world, text, expect, forbid, allow_extra).

def c_new_fact(r):
    w = W(r)
    k = r.choice(["dentist", "vet", "home", "work", "car", "gym", "insurance", "wifi", "partner", "sister"])
    if k == "dentist":
        doc, clinic = "Dr. " + r.choice(OTHER) + " " + r.choice(LAST), r.choice(CLINICS)
        t = pick(r, [f"My dentist is {doc} at {clinic}.", f"I go to {doc} at {clinic} for my teeth.", f"Dentist: {doc}, {clinic}.",
                     f"I see {doc} at {clinic} for cleanings.", f"For the record, my dentist is {doc}; the office is {clinic}."])
        return w, t, [{"op": "remember", "subject": me(), "relation": ["dentist"], "object": doc},
                      {"op": "remember", "subject": doc, "relation": ["works-at", "located-at", "practice", "clinic"], "object": clinic}], [], True
    if k == "vet":
        pet, kind = r.choice(PETS)
        doc, clinic = "Dr. " + r.choice(OTHER) + " " + r.choice(LAST), r.choice(VETS)
        w.fact(w.self, kind, pet, "animal")
        t = f"{pet}'s vet is {doc} at {clinic}."
        return w, t, [{"op": "remember", "subject": pet, "relation": ["vet", "veterinarian"], "object": doc}], [], True
    if k == "home":
        st, city = r.choice(STREETS), r.choice(CITIES)
        t = pick(r, [f"We live on {st} in {city}.", f"Our house is on {st} in {city}.", f"Home is {st}, {city}.",
                     f"Our address is on {st} in {city}.", f"We've lived on {st} in {city} for a while now."])
        return w, t, [{"op": "remember", "subject": me(), "relation": ["lives-in", "lives-on", "home", "address", "lives-at"], "value_or_object": st}], [], True
    if k == "work":
        org = r.choice(ORGS)
        t = pick(r, [f"I work at {org}.", f"I'm a senior engineer at {org}.", f"My employer is {org}.",
                     f"I've been at {org} for two years.", f"Work is at {org}, on the infrastructure side."])
        return w, t, [{"op": "remember", "subject": me(), "relation": ["works-at", "employer", "works-for"], "object": org}], [], True
    if k == "car":
        car = r.choice(CARS)
        t = f"I drive a {car}."
        return w, t, [{"op": "remember", "subject": me(), "relation": ["car", "drives", "vehicle", "owns"], "value_or_object": car.split(" ", 1)[1]}], [], True
    if k == "gym":
        g = r.choice(GYMS)
        t = f"I joined a gym: {g}, Tuesday and Thursday mornings."
        return w, t, [{"op": "remember", "subject": me(), "relation": ["gym", "member-of", "goes-to", "climbs-at", "trains-at"], "value_or_object": g.replace("the ", "")}], [], True
    if k == "insurance":
        ins, agent = r.choice(INSURERS), w.person()
        t = f"Home and auto insurance is with {ins}. Our agent is {agent}."
        return w, t, [{"op": "remember", "subject": me(), "relation": ["insurance", "insurer", "home-and-auto-insurance", "insured-by", "home-insurance", "auto-insurance"], "value_or_object": ins},
                      {"op": "remember", "relation": ["agent", "insurance-agent"], "value_or_object": agent, "t": {"subject": "me", "relation": "insurance-agent", "object": agent}}], [], True
    if k == "wifi":
        name = r.choice(WIFI)
        t = f"Our home wifi network is called {name}."
        return w, t, [{"op": "remember", "relation": ["name", "wifi", "network", "wifi-name", "called", "ssid"], "value_or_object": name, "t": {"subject": "home wifi", "relation": "name", "value": name}}], [], True
    if k == "partner":
        p = r.choice(OTHER)
        t = f"My partner is {p}."
        return w, t, [{"op": "remember", "subject": me(), "relation": ["partner", "spouse", "husband", "wife"], "object": p}], [], True
    s = r.choice(OTHER)
    city = r.choice(CITIES)
    t = f"My sister {s} lives in {city}."
    return w, t, [{"op": "remember", "subject": me(), "relation": ["sister", "sibling"], "object": s},
                  {"op": "remember", "subject": s, "relation": ["lives-in", "lives"], "value_or_object": city}], [], True


def c_change(r):
    w = W(r)
    k = r.choice(["home", "manager", "vet", "work", "insurance", "budget", "due"])
    w.fact(w.self, "dentist", "Dr. " + r.choice(LAST), "person", since(r))
    if k == "home":
        old, new, city = r.choice(STREETS), r.choice(STREETS), r.choice(CITIES)
        while new == old:
            new = r.choice(STREETS)
        cid = w.fact(w.self, "lives-in", f"{old} in {r.choice(CITIES)}", since=since(r))
        t = pick(r, [f"We moved today. The new place is on {new} in {city}.", f"We moved to {new} in {city} this weekend.",
                     f"Moving day is done: we live on {new} in {city} now.", f"New address: {new}, {city}. The move went fine.",
                     f"Finally settled into the new house on {new} in {city}."])
        return w, t, [{"op": "update", "id": cid, "value_or_object": new, "t": {"value": f"{new} in {city}"}}], [{"op": "remember", "subject": me(), "relation": ["lives-in"], "value_or_object": old}], True
    if k == "manager":
        old, new = w.person(), w.person()
        w.eid(old)
        cid = w.fact(w.self, "manager", old, "person", since(r))
        t = pick(r, [f"My new manager is {new}.", f"I got promoted to engineering lead. My new manager is {new}.",
                     f"I report to {new} now.", f"Reorg at work: {new} is my manager from today.",
                     f"{new} took over as my manager this week."])
        return w, t, [{"op": "update", "id": cid, "value_or_object": new.split()[0], "t": {"value": new}}], [], True
    if k == "vet":
        pet, kind = r.choice(PETS)
        old, new = "Dr. " + r.choice(LAST), "Dr. " + r.choice(OTHER) + " " + r.choice(LAST)
        w.fact(w.self, kind, pet, "animal")
        cid = w.fact(pet, "vet", old, "animal", since(r))
        t = f"{pet} has a new vet now: {new} at {r.choice(VETS)}."
        return w, t, [{"op": "update", "id": cid, "value_or_object": new.split()[-1], "t": {"value": new}}], [], True
    if k == "work":
        old, new = r.sample(ORGS, 2)
        cid = w.fact(w.self, "works-at", old, since=since(r))
        t = pick(r, [f"I left {old}. I start at {new} on Monday.", f"I switched jobs: I work at {new} now.",
                     f"Last day at {old} today; next week I join {new}.", f"I accepted the offer from {new}, so I'm leaving {old}.",
                     f"New job! I'm at {new} now instead of {old}."])
        return w, t, [{"op": "update", "id": cid, "value_or_object": new}], [], True
    if k == "insurance":
        old, new = r.sample(INSURERS, 2)
        cid = w.fact(w.self, "insurance", old, since=since(r))
        t = f"We switched our insurance from {old} to {new}."
        return w, t, [{"op": "update", "id": cid, "value_or_object": new}], [], True
    if k == "budget":
        a, b = r.choice([30, 38, 40, 52]), r.choice([44, 46, 55, 61])
        w.eid("kitchen remodel", "project")
        cid = w.fact("kitchen remodel", "budget", f"${a},000", "project", since(r))
        t = f"Kitchen remodel update: we changed the cabinets, so the budget is now ${b},000."
        return w, t, [{"op": "update", "id": cid, "value_or_object": str(b), "t": {"value": f"${b},000"}}], [], True
    w.eid("Harbor launch", "project")
    cid = w.fact("Harbor launch", "due", "December 1, 2026", "project", since(r))
    t = "Harbor launch slipped. The new date is January 15, 2027."
    return w, t, [{"op": "update", "id": cid, "value_or_object": "January 15", "t": {"value": "January 15, 2027"}}], [], True


def c_fix(r):
    w = W(r)
    k = r.choice(["spelling", "misspoke", "always"])
    kid = r.choice(["Ruby", "Theo", "Ivy", "Max"])
    w.fact(w.self, "child", kid, "person")
    if k == "spelling":
        last = r.choice(LAST)
        cid = w.fact(kid, "coach", f"Jen {last}", "person", since(r))
        t = f"Correction: {kid}'s soccer coach is Jen {last}s, with an s, not Jen {last}."
        return w, t, [{"op": "fix", "id": cid, "value_or_object": last + "s", "t": {"value": f"Jen {last}s", "field": "object"}}], [{"op": "update", "id": cid}], True
    if k == "misspoke":
        a, b = "Dr. " + r.choice(LAST), "Dr. " + r.choice(LAST)
        while a == b:
            b = "Dr. " + r.choice(LAST)
        cid = w.fact(w.self, "dentist", a, "person", since(r))
        t = f"I misspoke earlier: my dentist is {b}, not {a}."
        return w, t, [{"op": "fix", "id": cid, "value_or_object": b.split()[-1], "t": {"value": b, "field": "object"}}], [], True
    d1, d2 = "September 3", "September 13"
    cid = w.fact(kid, "birthday", d1 + ", 2016", "person", since(r))
    t = f"{kid}'s birthday was always {d2}, 2016; I had it wrong."
    return w, t, [{"op": "fix", "id": cid, "value_or_object": "13", "t": {"value": f"{d2}, 2016", "field": "value"}}], [], True


def c_end(r):
    w = W(r)
    k = r.choice(["car", "gym", "report", "club"])
    if k == "car":
        car = r.choice(CARS)
        cid = w.fact(w.self, "car", car, since=since(r))
        t = pick(r, [f"We sold the {car.split(' ', 2)[1]}.", f"We sold our {car.split(' ', 1)[1]} last week.",
                     f"The {car.split(' ', 2)[1]} is gone, we sold it.", f"Someone bought our {car.split(' ', 1)[1]} today.",
                     f"Said goodbye to the {car.split(' ', 2)[1]}; it's sold."])
        return w, t, [{"op": "end", "id": cid}], [{"op": "retract", "id": cid}], True
    if k == "gym":
        g = r.choice(GYMS)
        cid = w.fact(w.self, "gym", g, since=since(r))
        t = f"I cancelled my membership at {g.replace('the ', '')}."
        return w, t, [{"op": "end", "id": cid}], [{"op": "retract", "id": cid}], True
    if k == "report":
        p = w.person()
        w.eid(p)
        cid = w.fact(p, "manager", w.self, "person", since(r))
        t = f"{p} moved to {r.choice(TEAMS)}; {p.split()[0]} doesn't report to me anymore."
        return w, t, [{"op": "end", "id": cid}], [{"op": "update", "id": cid}, {"op": "retract", "id": cid}], True
    club = r.choice(["Page Turners", "the chess club", "the running club"])
    cid = w.fact(w.self, "member-of", club, since=since(r))
    t = f"I left {club}. No more meetings for me."
    return w, t, [{"op": "end", "id": cid}], [{"op": "retract", "id": cid}], True


def c_event(r):
    w = W(r)
    part, act = r.choice(["shoulder", "knee", "ankle", "wrist", "back"]), r.choice(["climbing", "skiing", "running", "playing soccer", "lifting"])
    city = r.choice(["Porto", "Lisbon", "Kyoto", "Oslo", "Montreal", "Austin", "Mexico City", "Chicago"])
    kid = r.choice(["Theo", "Ruby", "Ivy", "Max", "Leo", "Nora"])
    proj = r.choice(["Atlas migration", "Harbor launch", "kitchen remodel", "garage cleanout", "data center move"])
    race = r.choice(["half marathon", "10K", "triathlon", "century ride"])
    t, v, title = pick(r, [
        (f"I hurt my {part} {act}. No {act} for a while.", part, f"hurt my {part} {act}"),
        (f"Back from {city}. What a trip!", city.lower(), f"back from {city}"),
        (f"We finished the {proj} today.", proj.split()[0].lower(), f"finished the {proj}"),
        (f"{kid} lost a first tooth today!", "tooth", f"{kid} lost a first tooth"),
        (f"I ran my first {race} this morning.", race.split()[0].lower(), f"ran my first {race}"),
        (f"Got my second dose of the {r.choice(['shingles', 'tetanus', 'hepatitis'])} vaccine today.", "vaccine", "second dose of the vaccine"),
        (f"We got back from a week in {city} last night.", city.lower(), f"a week in {city}"),
    ])
    return w, t, [{"op": "event", "value": v, "t": {"subject": "me", "value": title}}], [], True


def c_third_person(r):
    w = W(r)
    old, new = r.sample(ORGS, 2)
    mgr = w.person()
    w.eid(mgr)
    my_job = w.fact(w.self, "works-at", old, since=since(r))
    w.fact(w.self, "manager", mgr, "person", since(r))
    t = pick(r, [f"{mgr} left {old} to join {new}.", f"{mgr} has a new job at {new}; they left {old}.",
                 f"Big news at work: {mgr} is leaving {old} for {new}.", f"{mgr} starts at {new} next month.",
                 f"{mgr} quit {old} and went to {new}."])
    return w, t, [{"op": "remember", "subject": mgr, "relation": ["works-at", "employer", "joined", "works-for"], "object": new}], [
        {"op": "remember", "subject": me(), "relation": ["works-at", "employer", "job", "works-for"]},
        {"op": "update", "id": my_job}, {"op": "end", "id": my_job}], True


def c_other_fact(r):
    w = W(r)
    car = r.choice(CARS)
    w.fact(w.self, "car", car, since=since(r))
    f = r.choice(OTHER)
    other = r.choice([c for c in CARS if c != car])
    boss = w.person()
    t, ex, fb = r.choice([
        (f"My friend {f} just bought a {other}.", [{"op": "remember", "subject": f, "relation": ["car", "bought", "drives", "owns"], "t": {"relation": "car", "object": other}}],
         [{"op": "remember", "subject": me(), "relation": ["car"]}]),
        (f"{f} said their own manager is {boss}.", [{"op": "remember", "subject": f, "relation": ["manager"], "t": {"object": boss}}],
         [{"op": "remember", "object": me()}, {"op": "remember", "subject": me(), "relation": ["manager"]}]),
        (f"Our neighbor {f} has a border collie too.", [{"op": "remember", "subject": f, "t": {"relation": "dog", "value": "border collie"}}], [{"op": "remember", "subject": me(), "relation": ["dog", "pet"]}]),
    ])
    return w, t, ex, fb, True


def c_small_talk(r):
    w = W(r)
    w.fact(w.self, "dentist", "Dr. " + r.choice(LAST), "person", since(r))
    return w, pick(r, SMALL), [], [], False


def c_question(r):
    w = W(r)
    w.fact(w.self, "dentist", "Dr. " + r.choice(LAST), "person", since(r))
    return w, pick(r, QUESTIONS), [], [], False


def c_request(r):
    w = W(r)
    return w, pick(r, REQUESTS).format(o=r.choice(OTHER)), [], [], False


def c_plan(r):
    w = W(r)
    return w, pick(r, PLANS).format(o=r.choice(OTHER), city=r.choice(CITIES), team=r.choice(TEAMS)), [], [], False


def c_reports(r):
    w = W(r)
    p = w.person()
    t = pick(r, [f"{p} joined my team today.", f"{p} joined my team today. They report to me.", f"{p} reports to me starting this week.",
                 f"Welcome {p}, the new engineer on my team.", f"I'm now the manager of {p}; they started today.",
                 f"{p} moved onto my team and reports to me now."])
    return w, t, [{"op": "remember", "subject": p, "relation": ["manager", "reports-to", "boss"], "object": me()}], [
        {"op": "remember", "subject": me(), "relation": ["manager"], "object": p}], True


def c_birthday(r):
    w = W(r)
    n = r.randint(25, 60)
    t = pick(r, [f"It's my birthday today, I turned {n}.", f"Turned {n} today. Birthday dinner tonight!",
                 f"Happy birthday to me, {n} years old today.", f"I'm {n} as of today. Cake later."])
    return w, t, [{"op": "remember", "subject": me(), "relation": ["birthday", "born", "birth-date", "date-of-birth"], "value": "__BIRTHDAY__", "n": n}], [], True


def c_multi(r):
    w = W(r)
    a, b = r.sample(["Ruby", "Theo", "Ivy", "Max", "Leo", "Nora"], 2)
    p = r.choice(OTHER)
    t = f"My partner is {p}. Our kids are {a} and {b}."
    return w, t, [{"op": "remember", "subject": me(), "relation": ["partner", "spouse"], "object": p},
                  {"op": "remember", "subject": me(), "relation": ["child", "kid", "son", "daughter"], "object": a},
                  {"op": "remember", "subject": me(), "relation": ["child", "kid", "son", "daughter"], "object": b}], [], True


def c_other_update(r):
    w = W(r)
    p = r.choice(OTHER)
    w.eid(p)
    w.fact(w.self, "partner", p, "person")
    mine = w.fact(w.self, "works-at", r.choice(ORGS), since=since(r))
    t = pick(r, [f"{p}'s work schedule changed: {p} works from home on Mondays and Fridays now.",
                 f"{p} works from home Mondays and Fridays now.", f"New schedule for {p}: home on Mondays and Fridays.",
                 f"From now on {p} is remote on Mondays and Fridays."])
    return w, t, [{"op": "remember", "subject": p, "t": {"relation": "works-from-home", "value": "Mondays and Fridays"}}], [{"op": "update", "id": mine}, {"op": "fix", "id": mine}, {"op": "end", "id": mine}], True


def c_other_near(r):
    """A note about another person near the person's own facts (the load
    world): it must not change the person's home, job, or pet."""
    w = W(r)
    home = w.fact(w.self, "lives-in", f"{r.choice(STREETS)} in {r.choice(CITIES)}", since=since(r))
    job = w.fact(w.self, "works-at", r.choice(ORGS), since=since(r))
    p = w.person()
    w.eid(p)
    place, org = r.choice(["Coors Field", "Wash Park", "Union Station", "Red Rocks"]), r.choice(ORGS)
    t, ex = pick(r, [
        (f"Coffee with {p} this morning; they live near {place} now.", [{"op": "remember", "subject": p, "relation": ["lives", "lives-near", "lives-in"], "t": {"relation": "lives-near", "value": place}}]),
        (f"Had lunch with {p} at Sushi Den.", []),
        (f"{p} is moving to {r.choice(TEAMS)} next month, they said.", []),
        (f"Ran into {p} at {place}; they work at {org} these days.", [{"op": "remember", "subject": p, "relation": ["works-at", "employer", "works-for"], "t": {"relation": "works-at", "object": org}}]),
        (f"{p} got a new puppy named Juniper.", [{"op": "remember", "subject": p, "t": {"relation": "puppy", "value": "Juniper"}}]),
    ])
    return w, t, ex, [{"op": "update", "id": home}, {"op": "end", "id": home}, {"op": "fix", "id": home},
                      {"op": "update", "id": job}, {"op": "end", "id": job}, {"op": "fix", "id": job},
                      {"op": "remember", "subject": me(), "relation": ["lives-in", "works-at", "pet", "puppy", "dog"]}], True


MAKERS = {
    "new-fact": c_new_fact, "change": c_change, "fix": c_fix, "end": c_end, "event": c_event,
    "third-person": c_third_person, "other-fact": c_other_fact, "small-talk": c_small_talk, "question": c_question,
    "request": c_request, "plan": c_plan, "reports-to-me": c_reports, "birthday": c_birthday, "multi-fact": c_multi,
    "other-update": c_other_update, "other-near-self": c_other_near,
}


def make(r, cat, i):
    w, text, expect, forbid, extra = MAKERS[cat](r)
    when = day(r)
    for e in expect:
        if e.get("value") == "__BIRTHDAY__":
            n = e.pop("n")
            e["value"] = f"{when.strftime('%B')} {when.day}, {when.year - n}"
    return {"id": f"{cat}-{i:05d}", "category": cat, "when": when.isoformat(), "self": w.self, "notes": w.notes(),
            "text": text, "expect": [{k: v for k, v in e.items() if k != "t"} for e in expect], "forbid": forbid,
            "allow_extra": extra, "target": {"items": [target(e) for e in expect]}}


FIELDS = ["op", "id", "subject", "subject_kind", "relation", "object", "object_kind", "value", "field", "also"]


def looks_like_name(v):
    return bool(v) and v[0].isupper() and not any(ch.isdigit() for ch in v)


def target(e):
    """The answer item of a perfect note taker for an expected item: the
    first relation name, the whole values, and the fields of "t"."""
    it = {k: "" for k in FIELDS}
    it["also"] = False
    it["op"] = e["op"]
    it["id"] = e.get("id", "")
    it["subject"] = e.get("subject", "")
    rel = e.get("relation") or []
    it["relation"] = rel[0] if rel else ""
    if e.get("object"):
        it["object"] = e["object"]
    if e.get("value"):
        it["value"] = e["value"]
    if e.get("value_or_object"):
        v = e["value_or_object"]
        it["object" if looks_like_name(v) and e["op"] == "remember" else "value"] = v
    for k, v in e.get("t", {}).items():
        it[k] = v
    if it["op"] in ("update", "fix", "end", "retract"):
        it["subject"] = it["subject"] or ""
    if it["op"] == "fix" and not it["field"]:
        it["field"] = "value"
    return it


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("out")
    ap.add_argument("--n", type=int, default=25, help="cases for each category")
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--split", default="test")
    ap.add_argument("--per-file", type=int, default=1000)
    ap.add_argument("--train", action="store_true", help="training data: leave out the held-out phrasings")
    ap.add_argument("--only", default="", help="comma-separated categories (default: all)")
    a = ap.parse_args()
    global TRAIN
    TRAIN = a.train
    r = random.Random(a.seed)
    cats = [c for c in MAKERS if not a.only or c in a.only.split(",")]
    cases = [make(r, cat, i) for cat in cats for i in range(a.n)]
    r.shuffle(cases)
    out = Path(a.out)
    out.mkdir(parents=True, exist_ok=True)
    for k in range(0, len(cases), a.per_file):
        with open(out / f"{a.split}-{k // a.per_file:03d}.jsonl", "w") as f:
            for c in cases[k:k + a.per_file]:
                f.write(json.dumps(c, ensure_ascii=False) + "\n")
    print(f"{len(cases)} cases, {len(cats)} categories")


if __name__ == "__main__":
    main()
