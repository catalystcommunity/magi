"""Make note-taker cases, version 2 (README.md, "Version 2").

  python3 -I evals/notetaker/gen2.py OUT_DIR --part train|dev|test [--n PER_CATEGORY]
          [--seed S] [--split NAME] [--per-file N] [--only CATS]

Each case is scored by the graph that its items make (notes-eval, "graph"
checks; magi/compose/notesim.go): "Bob moved to Austin" is right as an
update of the fact of Robert Holt, wrong as a new person "Bob".

Each phrasing list, each name pool, and each style has three parts: train,
dev, and test. A part uses only its own phrasings, names, and styles, so
the test split has no wording of the training data. The dev split chooses
among checkpoints; report the test split.
"""

import argparse
import datetime as dt
import json
import random
from pathlib import Path

PART = "train"
_IDX = {"train": (0, 1, 2), "dev": (3,), "test": (4,)}


def part(lst):
    """The items of the list for this part: index mod 5 in 0-2 train, 3 dev, 4 test."""
    assert len(lst) >= 5, lst
    return [x for i, x in enumerate(lst) if i % 5 in _IDX[PART]]


def P(r, lst):
    return r.choice(part(lst))


SELF = ["Jamie", "Morgan", "Riley", "Casey", "Jordan", "Avery", "Quinn", "Rowan", "Sasha", "Taylor", "Robin", "Drew",
        "Elliot", "Harper", "Kai", "Logan", "Noa", "Parker", "Reese", "Skyler", "Dakota", "Emerson", "Finley", "Hayden", "Jesse"]
FIRST = ["Teodora", "Rafael", "Nadia", "Sven", "Bea", "Wen", "Marco", "Priya", "Hiro", "Ingrid", "Luis", "Mara", "Olga",
         "Tomas", "Uma", "Viktor", "Zoe", "Amara", "Bruno", "Celia", "Dario", "Esme", "Felix", "Greta", "Hassan", "Imani",
         "Joel", "Kira", "Lars", "Mina", "Omar", "Pia", "Rhea", "Sol", "Tariq", "Vera", "Yara", "Ari", "Ben", "Cleo",
         "Dmitri", "Elena", "Farah", "Gus", "Hana", "Ivan", "Jada", "Kofi", "Lena", "Mateo", "Nia", "Oskar", "Paz", "Ravi", "Sana"]
LAST = ["Vance", "Okonjo", "Kerr", "Holt", "Moreau", "Li", "Rossi", "Raman", "Sato", "Lindqvist", "Garza", "Petrov",
        "Abara", "Brandt", "Castillo", "Dunn", "Eklund", "Foster", "Haas", "Iqbal", "Jensen", "Kowalski", "Nakamura", "Osei",
        "Park", "Quist", "Reyes", "Stone", "Tanaka", "Ueda", "Varga", "Weiss", "Xu", "Yilmaz", "Zamora"]
ORGS = ["Juniper Labs", "Orbital Freight", "Copperleaf", "Northwind Health", "Bluepeak Energy", "Kestrel Bank",
        "Larch Software", "Meridian Foods", "Pinecone Analytics", "Quarry Robotics", "Saltmarsh Media", "Tidewater Logistics",
        "Umber Studio", "Vantage Clinics", "Willow Insurance"]
DENTAL = ["Highland Dental", "Pearl Dental", "Cherry Creek Dental", "Bright Smile Dental", "Summit Dental Care",
          "Aspen Grove Dental", "Riverbend Dentistry", "Lakeside Dental", "Maple Leaf Dental", "Union Dental"]
CLINIC = ["Riverside Family Medicine", "Oak Street Clinic", "Elm Clinic", "Clear Creek Health", "Northside Medical",
          "Larkspur Family Practice", "Front Range Clinic", "Cedar Health", "Bluebell Medical", "Kestrel Clinic"]
VETS = ["Platte Street Vet", "Boulder Paws Clinic", "Sloan Animal Hospital", "Mile High Vets", "Golden Pet Clinic",
        "Wagging Tail Vets", "Highland Animal Care", "Prairie Vet Center", "Cottonwood Animal Clinic", "Paws and Claws"]
STREETS = ["Pine Street", "Zuni Street", "Elm Avenue", "Maple Court", "Juniper Road", "Aspen Lane", "Birch Way", "Cedar Drive",
           "Willow Street", "Oak Terrace", "Spruce Avenue", "Lark Court", "Hollis Road", "Garnet Lane", "Quincy Street"]
CITIES = ["Boulder", "Denver", "Lakewood", "Golden", "Aurora", "Fort Collins", "Longmont", "Arvada", "Littleton", "Louisville",
          "Westminster", "Erie", "Lafayette", "Broomfield", "Parker"]
TRIPS = ["Porto", "Lisbon", "Kyoto", "Oslo", "Montreal", "Austin", "Mexico City", "Chicago", "Seattle", "Reykjavik",
         "Barcelona", "Vancouver", "Santa Fe", "Nashville", "Seoul"]
CARS = ["2018 Mazda CX-5", "2026 Kia EV9", "2021 Subaru Outback", "2019 Honda Civic", "2023 Toyota RAV4", "2020 VW Golf",
        "2017 Ford Focus", "2022 Hyundai Ioniq 5", "2016 Jeep Wrangler", "2024 Volvo XC40"]
GYMS = ["Movement RiNo", "CorePower Highland", "Colfax YMCA", "Earth Treks Golden", "Vital Climbing", "Orangetheory Wash Park",
        "Denver Athletic Club", "Bouldering Project", "Pure Barre Lowry", "Anytime Fitness Erie"]
INSURERS = ["Front Range Mutual", "Summit Insurance", "Clearwater Assurance", "Granite Shield", "Bluebird Mutual",
            "Harborline Insurance", "Peak State Insurance", "Lantern Mutual", "Evergreen Assurance", "Tallgrass Insurance"]
PETS = [("Pepper", "dog"), ("Biscuit", "dog"), ("Miso", "cat"), ("Juniper", "cat"), ("Captain", "dog"), ("Olive", "cat"),
        ("Rocket", "dog"), ("Mochi", "cat"), ("Waffles", "dog"), ("Pickle", "cat")]
TEAMS = ["the data team", "the platform team", "the mobile team", "the security team", "the payments team", "the search team",
         "the billing team", "the infra team", "the design team", "the growth team"]
WIFI = ["Larkspur-5G", "Saguaro-Guest", "Bluebird", "Quiet-Owl", "Fern-Home", "Otter-Net", "Basement-AP", "Comet-5", "Lantern", "Mesa-Mesh"]
KIDS = ["Ruby", "Theo", "Ivy", "Max", "Leo", "Nora", "Iris", "Felix", "June", "Otis", "Hazel", "Milo", "Ada", "Ezra", "Lucy"]
SCHOOLS = ["Lincoln Elementary", "Hill Middle School", "Bear Creek Elementary", "Steele Elementary", "Merrill Middle School",
           "Polaris Elementary", "Slavens K-8", "Cory Elementary", "Skinner Middle School", "Bromwell Elementary"]
CLUBS = ["Page Turners", "the chess club", "the running club", "the choir", "the climbing club", "the board game group",
         "the cycling club", "the pottery class", "the book swap", "the trivia team"]
BANKS = ["Kestrel Bank", "Elevations Credit Union", "First Peak Bank", "Canvas Credit Union", "Alpine Bank",
         "Bellco", "Ent Credit Union", "Prairie Trust", "Cobalt Bank", "Mesa Savings"]
PHONES = ["Verizon", "T-Mobile", "AT&T", "Mint Mobile", "Visible", "Google Fi", "US Mobile", "Cricket", "Boost", "Ting"]
INJURY = [("shoulder", "climbing"), ("knee", "skiing"), ("ankle", "running"), ("wrist", "playing soccer"), ("back", "lifting"),
          ("elbow", "playing tennis"), ("hamstring", "sprinting"), ("hip", "hiking"), ("neck", "surfing"), ("foot", "dancing")]


def name(r):
    return P(r, FIRST) + " " + P(r, LAST)


def doctor(r):
    return "Dr. " + P(r, FIRST) + " " + P(r, LAST)


# Styles: how a person writes. Each takes and gives the text.
def s_plain(t, r): return t
def s_lower_start(t, r): return t[0].lower() + t[1:]
def s_fyi(t, r): return "fyi " + t[0].lower() + t[1:]
def s_okso(t, r): return "ok so " + t[0].lower() + t[1:]
def s_update(t, r): return "Quick update: " + t
def s_nodot(t, r): return t.rstrip(".")
def s_excl(t, r): return t.rstrip(".") + "!"
def s_voice(t, r): return t.replace(",", "").replace(";", "").replace(":", "").rstrip(".!").replace(". ", " ")
def s_chatter(t, r): return r.choice(["Long day. ", "Ugh, Mondays. ", "Coffee first. ", "Hey! ", "Morning. "]) + t
def s_btw(t, r): return "btw " + t[0].lower() + t[1:]
def s_anyway(t, r): return t.rstrip(".") + ". Anyway."
def s_just(t, r): return "Just so you know, " + t[0].lower() + t[1:]
def s_hey(t, r): return "Hey, " + t[0].lower() + t[1:]
def s_lol(t, r): return t.rstrip(".") + " lol"
def s_um(t, r): return "um so " + t[0].lower() + t[1:].rstrip(".")


STYLES = [s_plain, s_lower_start, s_fyi, s_okso, s_update, s_nodot, s_excl, s_voice, s_chatter, s_btw, s_anyway, s_just, s_hey, s_lol, s_um]


class W:
    """A household: the person, people, things, and their facts as notes."""

    def __init__(self, r):
        self.r = r
        self.self = P(r, SELF)
        self.ents = {}
        self.facts = []
        self.eid(self.self, "person", ["me", "myself"])

    def code(self):
        return "".join(self.r.choice("ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz0123456789") for _ in range(20))

    def eid(self, label, kind="person", aliases=None):
        if label not in self.ents:
            self.ents[label] = ("ent_" + self.code(), kind, aliases or [])
        return self.ents[label][0]

    def fact(self, subj, rel, other, kind="thing", since=True, other_kind=None):
        self.eid(subj, kind)
        if other_kind:
            self.eid(other, other_kind)
        cid = "clm_" + self.code()
        self.facts.append((cid, subj, rel, other, sincedate(self.r) if since else None))
        return cid

    def notes(self):
        by = {}
        for f in self.facts:
            by.setdefault(f[1], []).append(f)
        out = []
        for label, (eid, kind, aliases) in self.ents.items():
            if label not in by and label != self.self and not aliases:
                # an entity with no facts shows when another fact names it
                if not any(f[3] == label for f in self.facts):
                    continue
            line = f"- [{eid}] {label} ({kind})"
            if aliases:
                line += ", also called " + ", ".join(aliases)
            out.append(line)
            for cid, subj, rel, other, since in by.get(label, []):
                out.append(f"  - [{cid}] {subj} {rel} {other}" + (f" (since {since})" if since else ""))
        return "\n".join(out) if self.facts else ""


def sincedate(r):
    d = dt.date(2022, 1, 1) + dt.timedelta(days=r.randint(0, 900))
    return d.strftime("%-d %B %Y")


def when(r):
    d = dt.date(2024, 1, 1) + dt.timedelta(days=r.randint(0, 1000))
    return dt.datetime(d.year, d.month, d.day, r.randint(7, 22), r.choice([0, 15, 30, 45]), tzinfo=dt.timezone(dt.timedelta(hours=-7)))


def has(subject, rel, other=""):
    return {"has": {"subject": subject, "relation": rel, "other": other}}


def lacks(subject, rel, other=""):
    return {"lacks": {"subject": subject, "relation": rel, "other": other}}


QUIET = [{"quiet": True}]
HOME = ["lives-in", "lives-on", "home", "address", "lives-at", "lives"]
WORK = ["works-at", "employer", "works-for", "job"]


def filler(w, r, n=None):
    """Facts that a real graph has around the point of the case."""
    for _ in range(n if n is not None else r.randint(1, 3)):
        k = r.choice(["dentist", "kid", "gym", "bank", "phone"])
        if k == "dentist" and not any(f[2] == "dentist" for f in w.facts):
            d = doctor(r)
            w.fact(w.self, "dentist", d, "person", other_kind="person")
        elif k == "kid" and not any(f[2] == "child" for f in w.facts):
            w.fact(w.self, "child", P(r, KIDS), "person", since=False, other_kind="person")
        elif k == "gym" and not any(f[2] == "gym" for f in w.facts):
            w.fact(w.self, "gym", P(r, GYMS), "person")
        elif k == "bank" and not any(f[2] == "bank" for f in w.facts):
            w.fact(w.self, "bank", P(r, BANKS), "person")
        elif k == "phone" and not any(f[2] == "phone-carrier" for f in w.facts):
            w.fact(w.self, "phone-carrier", P(r, PHONES), "person")


# Each maker gives (world, text, checks).

def c_new_fact(r):
    w = W(r)
    filler(w, r, r.randint(0, 2))
    k = r.choice(["dentist", "doctor", "vet", "home", "work", "car", "gym", "insurance", "wifi", "partner", "sister", "school", "bank", "phone", "allergy"])
    if k == "dentist":
        d, c = doctor(r), P(r, DENTAL)
        w.facts = [f for f in w.facts if f[2] != "dentist"]
        t = P(r, [f"My dentist is {d} at {c}.", f"I go to {d} at {c} for my teeth.", f"Dentist: {d}, {c}.",
                  f"Found a new dentist, {d} over at {c}.", f"I see {d} at {c} for cleanings.",
                  f"For the record my dentist is {d}, the office is {c}.", f"{d} at {c} is my dentist now that I'm settled here.",
                  f"My teeth guy is {d} at {c}.", f"Booked with {d} at {c}; that's my dentist.", f"Dentist is {d} ({c})."])
        return w, t, [has("me", ["dentist"], d.split()[-1]), has(d, ["works-at", "located-at", "practice", "clinic", "office"], c.split()[0])]
    if k == "doctor":
        d, c = doctor(r), P(r, CLINIC)
        t = P(r, [f"My doctor is {d} at {c}.", f"My GP is {d} at {c}.", f"Primary care: {d}, {c}.", f"I switched my care to {c}; my doctor there is {d}.",
                  f"I see {d} at {c} for checkups.", f"{d} at {c} is my family doctor.", f"Family doctor is {d}, at {c}.",
                  f"My physician is {d} ({c}).", f"Went to my new doctor today, {d} at {c}.", f"Doctor: {d} over at {c}."])
        return w, t, [has("me", ["doctor", "physician", "gp", "primary-care"], d.split()[-1])]
    if k == "vet":
        pet, kind = P(r, PETS)
        d, c = doctor(r), P(r, VETS)
        w.fact(w.self, kind, pet, "person", since=False, other_kind="animal")
        t = P(r, [f"{pet}'s vet is {d} at {c}.", f"We take {pet} to {d} at {c}.", f"{pet} sees {d} at {c}.",
                  f"Vet for {pet}: {d}, {c}.", f"{d} at {c} is {pet}'s vet.", f"{pet} goes to {c}; {d} is the vet there.",
                  f"Took {pet} to {d} at {c}, that's the vet from now on.", f"Our vet for {pet} is {d} ({c}).",
                  f"{pet}'s doctor is {d} at {c}.", f"New vet for {pet} is {d} over at {c}."])
        return w, t, [has(pet, ["vet", "veterinarian", "doctor"], d.split()[-1])]
    if k == "home":
        st, city = P(r, STREETS), P(r, CITIES)
        t = P(r, [f"We live on {st} in {city}.", f"Our house is on {st} in {city}.", f"Home is {st}, {city}.",
                  f"Our address is on {st} in {city}.", f"We've lived on {st} in {city} for a while now.",
                  f"I live on {st} in {city}.", f"Our place is on {st}, {city}.", f"We're on {st} in {city}.",
                  f"Home base is {st} in {city}.", f"My address: {st}, {city}."])
        return w, t, [has("me", HOME, st)]
    if k == "work":
        org = P(r, ORGS)
        t = P(r, [f"I work at {org}.", f"I'm a senior engineer at {org}.", f"My employer is {org}.", f"I've been at {org} for two years.",
                  f"Work is at {org}, on the infrastructure side.", f"I'm with {org}.", f"I do product design for {org}.",
                  f"My job is at {org}.", f"I work for {org} downtown.", f"{org} is where I work."])
        return w, t, [has("me", WORK, org)]
    if k == "car":
        car = P(r, CARS)
        t = P(r, [f"I drive a {car}.", f"My car is a {car}.", f"We have a {car}.", f"Our car: {car}.", f"I've got a {car}.",
                  f"My ride is a {car}.", f"We bought a {car} last year.", f"The family car is a {car}.", f"I drive a {car}, it's great.", f"Car: {car}."])
        return w, t, [has("me", ["car", "drives", "vehicle", "owns"], car.split(" ", 1)[1].split()[0])]
    if k == "gym":
        g = P(r, GYMS)
        w.facts = [f for f in w.facts if f[2] != "gym"]
        t = P(r, [f"I joined a gym: {g}, Tuesday and Thursday mornings.", f"I'm a member at {g} now.", f"I work out at {g}.",
                  f"Signed up at {g}.", f"My gym is {g}.", f"I train at {g} three days a week.", f"Got a membership at {g}.",
                  f"I go to {g} after work.", f"Gym: {g}.", f"I started going to {g}."])
        return w, t, [has("me", ["gym", "member", "goes-to", "climbs-at", "trains-at", "works-out", "membership"], g.split()[0])]
    if k == "insurance":
        ins, agent = P(r, INSURERS), name(r)
        t = P(r, [f"Home and auto insurance is with {ins}. Our agent is {agent}.", f"We're insured with {ins}; {agent} is our agent.",
                  f"Insurance: {ins}, agent {agent}.", f"Our insurer is {ins} and the agent is {agent}.", f"{ins} has our home and car policies. Agent: {agent}.",
                  f"We have {ins} for insurance, through {agent}.", f"Our policies are with {ins}. {agent} handles them.",
                  f"Insurance company is {ins}; our agent there is {agent}.", f"We use {ins} for insurance. The agent is {agent}.", f"{agent} at {ins} is our insurance agent."])
        # "X at Summit is our agent": the insurer can be the agent's employer.
        return w, t, [{"any_has": {"relation": ["insur", "works-at", "agent"], "other": ins.split()[0]}}]
    if k == "wifi":
        n = P(r, WIFI)
        t = P(r, [f"Our home wifi network is called {n}.", f"The wifi is {n}.", f"Home wifi name: {n}.", f"Our network name is {n}.",
                  f"The wifi at home is named {n}.", f"Wifi SSID is {n}.", f"Our wifi network: {n}.", f"The house wifi is {n}.",
                  f"Our wifi's called {n}.", f"Network at home is {n}."])
        return w, t, [{"any_has": {"relation": ["wifi", "name", "network", "ssid", "called"], "other": n}}]
    if k == "partner":
        p = P(r, FIRST)
        t = P(r, [f"My partner is {p}.", f"I'm married to {p}.", f"{p} is my wife.", f"{p} and I have been together for ten years.",
                  f"My husband is {p}.", f"My spouse is {p}.", f"I live with my partner {p}.", f"{p} is my partner.", f"My wife {p} says hi.", f"My partner's name is {p}."])
        return w, t, [has("me", ["partner", "spouse", "husband", "wife", "married"], p)]
    if k == "sister":
        s, city = P(r, FIRST), P(r, CITIES)
        t = P(r, [f"My sister {s} lives in {city}.", f"{s}, my sister, lives in {city}.", f"My sister is {s}; she lives in {city}.",
                  f"I have a sister, {s}, in {city}.", f"My sister {s} is in {city}.", f"{s} is my sister. She's in {city}.",
                  f"My sister {s} lives over in {city}.", f"My little sister {s} lives in {city}.", f"Sister: {s}, lives in {city}.", f"My older sister {s} lives in {city}."])
        return w, t, [has("me", ["sister", "sibling"], s), has(s, ["live", "in", "city"], city)]
    if k == "school":
        kid, sch = P(r, KIDS), P(r, SCHOOLS)
        w.fact(w.self, "child", kid, "person", since=False, other_kind="person")
        t = P(r, [f"{kid} goes to {sch}.", f"{kid} started at {sch} this fall.", f"{kid}'s school is {sch}.", f"{kid} is in second grade at {sch}.",
                  f"{kid} attends {sch}.", f"School for {kid}: {sch}.", f"{kid} is at {sch} now.", f"We enrolled {kid} at {sch}.",
                  f"{kid} goes to school at {sch}.", f"{sch} is {kid}'s school."])
        return w, t, [has(kid, ["school", "attends", "goes-to", "student", "enrolled"], sch.split()[0])]
    if k == "bank":
        b = P(r, BANKS)
        w.facts = [f for f in w.facts if f[2] != "bank"]
        t = P(r, [f"We bank with {b}.", f"My bank is {b}.", f"Our checking account is at {b}.", f"I use {b} for banking.",
                  f"We moved our accounts to {b}.", f"Banking: {b}.", f"Our bank is {b}.", f"I have my accounts at {b}.",
                  f"We're with {b} for checking and savings.", f"{b} is our bank."])
        return w, t, [has("me", ["bank", "account"], b.split()[0])]
    if k == "phone":
        p = P(r, PHONES)
        w.facts = [f for f in w.facts if f[2] != "phone-carrier"]
        t = P(r, [f"My phone carrier is {p}.", f"We're on {p} for phones.", f"I use {p} for my phone.", f"Our cell plan is with {p}.",
                  f"Phone service: {p}.", f"I have {p} as my carrier.", f"My cell provider is {p}.", f"We have {p} for our phones.",
                  f"My mobile carrier is {p}.", f"Cell service is {p}."])
        return w, t, [has("me", ["phone", "carrier", "cell", "mobile"], p)]
    a = r.choice(["peanuts", "penicillin", "shellfish", "cats", "bee stings", "latex", "pollen", "sesame", "dairy", "eggs"])
    t = P(r, [f"I'm allergic to {a}.", f"I have an allergy to {a}.", f"Allergies: {a}.", f"Note that I can't have {a}, I'm allergic.",
              f"I've got a {a} allergy.", f"{a.capitalize()} allergy here.", f"I react badly to {a}; allergic.", f"I'm really allergic to {a}.",
              f"Allergic to {a}, just so you know.", f"My allergy is {a}."])
    return w, t, [has("me", ["allerg"], a.split()[0])]


def c_change(r):
    w = W(r)
    filler(w, r)
    k = r.choice(["home", "manager", "vet", "work", "insurance", "budget", "due", "phone", "bank", "school"])
    if k == "home":
        old, new, city = r.sample(part(STREETS), 2) + [P(r, CITIES)]
        cid = w.fact(w.self, "lives-in", f"{old} in {P(r, CITIES)}", "person")
        t = P(r, [f"We moved today. The new place is on {new} in {city}.", f"We moved to {new} in {city} this weekend.",
                  f"Moving day is done: we live on {new} in {city} now.", f"New address: {new}, {city}. The move went fine.",
                  f"Finally settled into the new house on {new} in {city}.", f"We just moved, we're on {new} in {city} now.",
                  f"Our new place is on {new} in {city}.", f"We live on {new} in {city} now, moved last week.",
                  f"Closed on the house! We're on {new} in {city} now.", f"New home: {new} in {city}."])
        return w, t, [has("me", HOME, new), lacks("me", HOME, old)]
    if k == "manager":
        old, new = name(r), name(r)
        w.fact(w.self, "manager", old, "person", other_kind="person")
        t = P(r, [f"My new manager is {new}.", f"I got promoted to engineering lead. My new manager is {new}.", f"I report to {new} now.",
                  f"Reorg at work: {new} is my manager from today.", f"{new} took over as my manager this week.", f"{new} is my boss now.",
                  f"New boss: {new}.", f"Starting today I report to {new} instead of {old}.", f"{old} handed me off; {new} is my manager now.",
                  f"My manager changed to {new}."])
        return w, t, [has("me", ["manager", "boss", "reports-to"], new.split()[-1]), lacks("me", ["manager", "boss", "reports-to"], old)]
    if k == "vet":
        pet, kind = P(r, PETS)
        old, new = doctor(r), doctor(r)
        w.fact(w.self, kind, pet, "person", since=False, other_kind="animal")
        w.fact(pet, "vet", old, "animal", other_kind="person")
        t = P(r, [f"{pet} has a new vet now: {new} at {P(r, VETS)}.", f"We switched {pet} to {new}.", f"{pet}'s new vet is {new}.",
                  f"{pet} sees {new} now instead of {old}.", f"Moved {pet} over to {new} at {P(r, VETS)}.", f"New vet for {pet}: {new}.",
                  f"{new} is {pet}'s vet now.", f"We changed vets; {pet} goes to {new} now.", f"{pet} is with {new} now for vet stuff.",
                  f"{pet}'s vet changed to {new}."])
        return w, t, [has(pet, ["vet", "veterinarian", "doctor"], new.split()[-1]), lacks(pet, ["vet", "veterinarian"], old)]
    if k == "work":
        old, new = r.sample(part(ORGS), 2)
        w.fact(w.self, "works-at", old, "person", other_kind="organization")
        t = P(r, [f"I left {old}. I start at {new} on Monday.", f"I switched jobs: I work at {new} now.", f"Last day at {old} today; next week I join {new}.",
                  f"I accepted the offer from {new}, so I'm leaving {old}.", f"New job! I'm at {new} now instead of {old}.", f"I work at {new} now.",
                  f"Started at {new} today.", f"Changed jobs, I'm with {new} now.", f"Goodbye {old}, hello {new}. First day went well.",
                  f"I moved from {old} to {new}."])
        return w, t, [has("me", WORK, new), lacks("me", WORK, old)]
    if k == "insurance":
        old, new = r.sample(part(INSURERS), 2)
        w.fact(w.self, "insurance", old, "person")
        t = P(r, [f"We switched our insurance from {old} to {new}.", f"Our insurance is with {new} now.", f"Dropped {old}; we're with {new} for insurance.",
                  f"New insurer: {new}.", f"We moved our policies to {new}.", f"We changed insurance to {new}.", f"{new} is our insurance now, not {old}.",
                  f"Switched insurers, it's {new} now.", f"We left {old} for {new}.", f"Insurance is {new} from now on."])
        return w, t, [has("me", ["insur"], new.split()[0]), lacks("me", ["insur"], old.split()[0])]
    if k == "budget":
        a, b = r.choice([30, 38, 40, 52]), r.choice([44, 46, 55, 61])
        proj = r.choice(["kitchen remodel", "bathroom remodel", "deck project", "basement finish", "roof repair"])
        w.fact(proj, "budget", f"${a},000", "project")
        t = P(r, [f"{proj.capitalize()} update: we changed the cabinets, so the budget is now ${b},000.", f"The {proj} budget went up to ${b},000.",
                  f"New budget for the {proj}: ${b},000.", f"We're now at ${b},000 for the {proj}.", f"The {proj} will cost ${b},000 now.",
                  f"Budget for the {proj} is ${b},000 now.", f"We raised the {proj} budget to ${b},000.", f"{proj.capitalize()}: budget changed to ${b},000.",
                  f"Our {proj} budget is now ${b}k.", f"Revised {proj} budget: ${b},000."])
        return w, t, [has(proj, ["budget", "cost"], str(b)), lacks(proj, ["budget"], str(a) + ",000")]
    if k == "due":
        proj = r.choice(["Harbor launch", "Atlas migration", "Beacon release", "Q3 audit", "Nimbus rollout"])
        w.fact(proj, "due", "December 1, 2026", "project")
        t = P(r, [f"{proj} slipped. The new date is January 15, 2027.", f"{proj} moved to January 15, 2027.", f"The {proj} date is now January 15, 2027.",
                  f"New date for {proj}: January 15, 2027.", f"{proj} is due January 15, 2027 now.", f"They pushed {proj} to January 15, 2027.",
                  f"{proj} got delayed to January 15, 2027.", f"{proj} won't make December; new target is January 15, 2027.",
                  f"Deadline for {proj} changed to January 15, 2027.", f"{proj} is now scheduled for January 15, 2027."])
        return w, t, [has(proj, ["due", "date", "deadline", "target", "launch"], "January 15"), lacks(proj, ["due"], "December 1")]
    if k == "phone":
        old, new = r.sample(part(PHONES), 2)
        w.facts = [f for f in w.facts if f[2] != "phone-carrier"]
        w.fact(w.self, "phone-carrier", old, "person")
        t = P(r, [f"We switched our phones from {old} to {new}.", f"I'm on {new} now for my phone.", f"Ported my number to {new}.",
                  f"New carrier: {new}.", f"Dropped {old}, we have {new} now.", f"My phone carrier is {new} now.", f"We moved to {new} for cell service.",
                  f"Changed carriers to {new}.", f"I left {old} for {new}.", f"Cell plan is {new} now."])
        return w, t, [has("me", ["phone", "carrier"], new), lacks("me", ["phone", "carrier"], old)]
    if k == "bank":
        old, new = r.sample(part(BANKS), 2)
        w.facts = [f for f in w.facts if f[2] != "bank"]
        w.fact(w.self, "bank", old, "person")
        t = P(r, [f"We moved our accounts from {old} to {new}.", f"Our bank is {new} now.", f"Switched banks to {new}.",
                  f"Closed everything at {old}; we're with {new} now.", f"New bank: {new}.", f"We bank with {new} now.", f"I changed my bank to {new}.",
                  f"We left {old} for {new}.", f"Banking is with {new} now.", f"Our checking is at {new} now instead of {old}."])
        return w, t, [has("me", ["bank"], new.split()[0]), lacks("me", ["bank"], old.split()[0])]
    kid = P(r, KIDS)
    old, new = r.sample(part(SCHOOLS), 2)
    w.fact(w.self, "child", kid, "person", since=False, other_kind="person")
    w.fact(kid, "school", old, "person")
    t = P(r, [f"{kid} switched schools, {kid} goes to {new} now.", f"{kid} is at {new} now.", f"{kid} transferred to {new}.",
              f"New school for {kid}: {new}.", f"{kid} started at {new} this week instead of {old}.", f"We moved {kid} to {new}.",
              f"{kid} goes to {new} now.", f"{kid} changed schools to {new}.", f"{kid}'s new school is {new}.", f"{kid} left {old} for {new}."])
    return w, t, [has(kid, ["school", "attends", "goes-to"], new.split()[0]), lacks(kid, ["school", "attends"], old.split()[0])]


def c_fix(r):
    w = W(r)
    filler(w, r)
    kid = P(r, KIDS)
    w.fact(w.self, "child", kid, "person", since=False, other_kind="person")
    k = r.choice(["spelling", "misspoke", "always", "street"])
    if k == "spelling":
        last = P(r, LAST)
        cid = w.fact(kid, "coach", f"Jen {last}", "person", other_kind="person")
        t = P(r, [f"Correction: {kid}'s soccer coach is Jen {last}s, with an s, not Jen {last}.", f"Oops, it's Jen {last}s, not Jen {last}. {kid}'s coach.",
                  f"I spelled the coach wrong: Jen {last}s.", f"{kid}'s coach is spelled Jen {last}s.", f"Small fix, the coach is Jen {last}s with an s.",
                  f"Not Jen {last}, it's Jen {last}s. My mistake.", f"The coach's name is Jen {last}s; I had it wrong.", f"Fix: Jen {last}s is {kid}'s coach.",
                  f"Correction on the coach, Jen {last}s.", f"{kid}'s coach: Jen {last}s (I typed it wrong before)."])
        return w, t, [has(kid, ["coach"], last + "s"), {"not_kept": cid}]
    if k == "misspoke":
        a, b = r.sample(part(LAST), 2)
        w.facts = [f for f in w.facts if f[2] != "dentist"]
        w.fact(w.self, "dentist", f"Dr. {a}", "person", other_kind="person")
        t = P(r, [f"I misspoke earlier: my dentist is Dr. {b}, not Dr. {a}.", f"Sorry, my dentist is actually Dr. {b}.", f"Wrong name before, the dentist is Dr. {b}.",
                  f"Correction: Dr. {b} is my dentist, not Dr. {a}.", f"My dentist is Dr. {b}. I said Dr. {a} by mistake.", f"It's Dr. {b}, not Dr. {a}, for my teeth.",
                  f"I mixed them up, Dr. {b} is the dentist.", f"Actually my dentist has always been Dr. {b}.", f"Fix that: dentist is Dr. {b}.",
                  f"Dr. {a} was wrong, my dentist is Dr. {b}."])
        return w, t, [has("me", ["dentist"], b), lacks("me", ["dentist"], a)]
    if k == "always":
        w.fact(kid, "birthday", "September 3, 2016", "person")
        t = P(r, [f"{kid}'s birthday was always September 13, 2016; I had it wrong.", f"Correction: {kid} was born September 13, 2016, not the 3rd.",
                  f"{kid}'s birthday is September 13, not September 3.", f"I had {kid}'s birthday wrong, it's September 13, 2016.",
                  f"Fix: {kid}'s birthday is the 13th of September 2016.", f"{kid} was born on September 13, 2016. I said the 3rd before.",
                  f"Wrong date earlier, {kid}'s birthday is September 13.", f"{kid}'s birthday: September 13, 2016 (not the 3rd).",
                  f"Oops, {kid}'s birthday is September 13, 2016.", f"It's September 13 for {kid}'s birthday, my mistake."])
        return w, t, [has(kid, ["birth"], "13"), lacks(kid, ["birth"], "September 3,")]
    a, b = r.sample(part(STREETS), 2)
    city = P(r, CITIES)
    w.fact(w.self, "lives-in", f"{a} in {city}", "person")
    t = P(r, [f"Correction: we live on {b}, not {a}.", f"I said {a} but our street is {b}.", f"Our address is {b} in {city}; {a} was a mistake.",
              f"Wrong street before, it's {b}.", f"We're on {b}, not {a}. Typo.", f"Fix: our street is {b} in {city}.", f"It's {b}, not {a}, where we live.",
              f"My mistake, we live on {b}.", f"The house is on {b}. I wrote {a} by accident.", f"Correction on our address: {b}, {city}."])
    return w, t, [has("me", HOME, b), lacks("me", HOME, a)]


def c_end(r):
    w = W(r)
    filler(w, r)
    k = r.choice(["car", "gym", "report", "club", "pet", "partner-job"])
    if k == "car":
        car = P(r, CARS)
        cid = w.fact(w.self, "car", car, "person")
        model = car.split(" ", 2)[2] if len(car.split(" ")) > 2 else car
        t = P(r, [f"We sold the {model}.", f"We sold our {car.split(' ', 1)[1]} last week.", f"The {model} is gone, we sold it.",
                  f"Someone bought our {car.split(' ', 1)[1]} today.", f"Said goodbye to the {model}; it's sold.", f"No more {model}, we sold it.",
                  f"Sold the car, the {model}.", f"The {model} got sold this weekend.", f"We got rid of the {model}.", f"Traded in the {model} for cash, it's gone."])
        return w, t, [{"ended": cid}]
    if k == "gym":
        g = P(r, GYMS)
        w.facts = [f for f in w.facts if f[2] != "gym"]
        cid = w.fact(w.self, "gym", g, "person")
        t = P(r, [f"I cancelled my membership at {g}.", f"I quit {g}.", f"No more {g} for me.", f"Ended my {g} membership.",
                  f"I'm done with {g}, cancelled it.", f"Dropped my gym, {g}.", f"I left {g} this month.", f"Cancelled {g}.",
                  f"Not a member at {g} anymore.", f"My {g} membership is over."])
        return w, t, [{"ended": cid}]
    if k == "report":
        p = name(r)
        cid = w.fact(p, "manager", w.self, "person")
        f = p.split()[0]
        t = P(r, [f"{p} moved to {P(r, TEAMS)}; {f} doesn't report to me anymore.", f"{p} isn't on my team anymore.",
                  f"{f} left my team for {P(r, TEAMS)}.", f"{p} no longer reports to me.", f"I don't manage {p} anymore, {f} moved teams.",
                  f"{p} transferred out of my team.", f"{f} switched to {P(r, TEAMS)}, so I'm not {f}'s manager now.", f"{p} reports to someone else now.",
                  f"{p} moved off my team today.", f"{f} is not my report anymore."])
        return w, t, [{"ended": cid}, lacks(p, ["manager", "reports"], "me")]
    if k == "club":
        c = P(r, CLUBS)
        cid = w.fact(w.self, "member-of", c, "person")
        t = P(r, [f"I left {c}. No more meetings for me.", f"I quit {c}.", f"I'm not in {c} anymore.", f"Dropped out of {c}.",
                  f"Done with {c}.", f"I stopped going to {c}, I left it.", f"No longer part of {c}.", f"Left {c} last month.",
                  f"I gave up {c}.", f"Officially out of {c}."])
        return w, t, [{"ended": cid}]
    if k == "pet":
        pet, kind = P(r, PETS)
        cid = w.fact(w.self, kind, pet, "person", since=False, other_kind="animal")
        t = P(r, [f"We had to put {pet} down today.", f"{pet} passed away last night.", f"We lost {pet} this morning.", f"{pet} died yesterday. We're sad.",
                  f"Said goodbye to {pet} today.", f"{pet} is gone, old age.", f"Our {kind} {pet} passed.", f"RIP {pet}.", f"{pet} died this week.",
                  f"We rehomed {pet}; {pet} lives with my aunt now."])
        return w, t, [{"ended": cid}]
    p = P(r, FIRST)
    w.fact(w.self, "partner", p, "person", since=False, other_kind="person")
    org = P(r, ORGS)
    cid = w.fact(p, "works-at", org, "person", other_kind="organization")
    t = P(r, [f"{p} quit {org}.", f"{p} left {org} and is taking a break.", f"{p} doesn't work at {org} anymore.", f"{p}'s last day at {org} was Friday.",
              f"{p} resigned from {org}.", f"{p} is done at {org}.", f"{p} got laid off from {org}.", f"{p} retired from {org}.",
              f"{p} stopped working at {org}.", f"{p} is no longer with {org}."])
    return w, t, [{"ended": cid}, lacks("me", WORK, org)]


def c_event(r):
    w = W(r)
    filler(w, r, r.randint(0, 2))
    part_, act = P(r, INJURY)
    city, kid = P(r, TRIPS), P(r, KIDS)
    proj = r.choice(["Atlas migration", "Harbor launch", "kitchen remodel", "garage cleanout", "data center move"])
    race = r.choice(["half marathon", "10K", "triathlon", "century ride", "marathon"])
    t, v = P(r, [
        (f"I hurt my {part_} {act}. No {act} for a while.", part_),
        (f"Back from {city}. What a trip!", city),
        (f"We finished the {proj} today.", proj.split()[0]),
        (f"{kid} lost a first tooth today!", "tooth"),
        (f"I ran my first {race} this morning.", race.split()[0]),
        (f"We got back from a week in {city} last night.", city),
        (f"Just landed from {city}, great trip.", city),
        (f"Sprained my {part_} {act} yesterday.", part_),
        (f"{kid} learned to ride a bike today!", "bike"),
        (f"Finished my first {race} today, so tired.", race.split()[0]),
        (f"We spent the weekend in {city}.", city),
        (f"Wrapped up the {proj} this afternoon.", proj.split()[0]),
        (f"Tweaked my {part_} {act} this morning, ouch.", part_),
        (f"{kid} scored a goal at the game today!", "goal"),
        (f"Home from five days in {city}.", city),
    ])
    return w, t, [{"event": v}]


def c_third_person(r):
    w = W(r)
    filler(w, r)
    old, new = r.sample(part(ORGS), 2)
    mgr = name(r)
    job = w.fact(w.self, "works-at", old, "person", other_kind="organization")
    w.fact(w.self, "manager", mgr, "person", other_kind="person")
    t = P(r, [f"{mgr} left {old} to join {new}.", f"{mgr} has a new job at {new}; they left {old}.", f"Big news at work: {mgr} is leaving {old} for {new}.",
              f"{mgr} starts at {new} next month.", f"{mgr} quit {old} and went to {new}.", f"My manager {mgr} took a job at {new}.",
              f"{mgr} is moving to {new}.", f"Heard {mgr} accepted an offer from {new}.", f"{mgr} works at {new} now.", f"{mgr}'s new employer is {new}."])
    return w, t, [has(mgr, WORK + ["joined", "joining", "moving"], new), {"kept": job}, lacks("me", WORK, new)]


def c_other_fact(r):
    w = W(r)
    filler(w, r)
    car = P(r, CARS)
    mine = w.fact(w.self, "car", car, "person")
    f = P(r, FIRST)
    other = r.choice([c for c in part(CARS) if c != car] or [car])
    boss = name(r)
    t, ex = P(r, [
        (f"My friend {f} just bought a {other}.", [has(f, ["car", "bought", "drives", "owns", "vehicle"], other.split(" ", 1)[1].split()[0]), {"kept": mine}]),
        (f"{f} said their own manager is {boss}.", [has(f, ["manager", "boss"], boss.split()[-1]), lacks("me", ["manager"], boss)]),
        (f"Our neighbor {f} has a border collie too.", [has(f, ["dog", "pet", "has", "owns"], "collie"), lacks("me", ["dog"], "collie")]),
        (f"{f} from book club is a nurse.", [has(f, ["job", "occupation", "profession", "is", "works", "role"], "nurse"), lacks("me", ["job", "occupation"], "nurse")]),
        (f"{f} got a {other}, she loves it.", [has(f, ["car", "got", "drives", "owns", "vehicle", "bought"], other.split(" ", 1)[1].split()[0]), {"kept": mine}]),
        (f"My coworker {f} is allergic to peanuts.", [has(f, ["allerg"], "peanut"), lacks("me", ["allerg"], "peanut")]),
        (f"{f}'s new car is a {other}.", [has(f, ["car", "drives", "owns", "vehicle"], other.split(" ", 1)[1].split()[0]), {"kept": mine}]),
        (f"{f} next door works nights at the hospital.", [lacks("me", ["works", "job"], "hospital")]),
        (f"{f} is vegetarian, remember for dinners.", [has(f, ["diet", "vegetarian", "is", "eats"], "vegetarian"), lacks("me", ["diet"], "vegetarian")]),
        (f"{f}'s boss is {boss}.", [has(f, ["manager", "boss"], boss.split()[-1]), lacks("me", ["manager"], boss)]),
    ])
    return w, t, ex


def c_quiet(r, lst, checks=None):
    w = W(r)
    filler(w, r)
    return w, P(r, lst).format(o=P(r, FIRST), city=P(r, CITIES), team=P(r, TEAMS), kid=P(r, KIDS)), checks or QUIET


SMALL = ["Long day. Too many meetings.", "It snowed about six inches overnight.", "We had tacos for dinner, the kids loved it.",
         "Traffic on I-25 was terrible today.", "We watched a movie as a family tonight.", "{o} made pancakes this morning.",
         "My code review queue is huge this week.", "The kitchen is a mess.", "Feeling tired today.", "Great run this morning!",
         "The coffee machine at work broke again.", "So windy out today.", "{kid} was hilarious at breakfast.", "Ugh, allergies are bad today.",
         "Productive afternoon for once.", "Rainy day, stayed in.", "The game last night was wild.", "Made sourdough, it came out flat.",
         "Ran out of coffee.", "Finally cleaned the garage... partly."]
QUESTIONS = ["What's a good recipe for banana bread?", "How long should I boil an egg?", "Who is my dentist?", "When is book club this month?",
             "What's the weather tomorrow?", "Where do we live?", "What car do I drive?", "How old is {kid}?", "Can you summarize my week?",
             "Who's {o}'s manager again?", "What's my bank?", "Is {o} allergic to anything?", "When did we move?", "What time is it in Tokyo?",
             "Where does {o} work?", "Who's our vet?", "What's the wifi password?", "How many days until Christmas?", "What did I say about the budget?",
             "Do I have anything tomorrow?"]
REQUESTS = ["Remind me to call {o} tomorrow.", "Put the swim meet on {o}'s calendar on Saturday at 8.", "Add a task to buy a bike pump by Friday.",
            "I need to renew my passport.", "Add milk to my shopping list.", "Schedule a dentist appointment for next month.",
            "Set up a monthly budget review with {o}.", "Remind me Friday to pay the water bill.", "Book a table for four on Saturday.",
            "Make a to-do to fix the fence.", "Can you set an alarm for 6?", "Put {kid}'s recital on the family calendar.",
            "Text {o} that I'm running late.", "Add 'call the plumber' to my list.", "Remind me in an hour to move the laundry.",
            "Plan a dinner with {o} next week.", "Cancel my 3pm.", "Move the standup to 10.", "Create a task for the tax documents.",
            "Remind me to water the plants on Sunday."]
PLANS = ["We might get a second dog someday. Not now.", "Thinking about switching dentists, but probably not.",
         "Maybe we'll move to {city} in a few years.", "I'd like to learn to climb one day.", "{o} might move to {team} next year, she said.",
         "If the offer comes through I might leave my job.", "We could repaint the house next summer.", "Considering a trip to Japan at some point.",
         "Maybe I'll take up running again.", "{o} is thinking about grad school.", "We may sell the car next year.",
         "Possibly switching banks, not sure yet.", "Might join a gym in the spring.", "Someday I want a cabin in the mountains.",
         "We're debating getting solar panels.", "{o} might start at a new school next fall, nothing decided.",
         "I may change teams later this year.", "We talked about moving closer to {city}.", "Maybe a new phone carrier, eventually.",
         "Not sure if we'll keep the boat."]


def c_reports(r):
    w = W(r)
    filler(w, r)
    p = name(r)
    f = p.split()[0]
    t = P(r, [f"{p} joined my team today.", f"{p} joined my team today. They report to me.", f"{p} reports to me starting this week.",
              f"Welcome {p}, the new engineer who reports to me.", f"I'm now the manager of {p}; they started today.", f"{p} moved onto my team and reports to me now.",
              f"I have a new direct report, {p}.", f"{p} is my new report.", f"I'm managing {p} from now on.", f"{p} started on my team; I'm {f}'s manager.",
              f"New hire {p} reports to me.", f"{p} transferred onto my team.", f"I'll be {p}'s manager going forward.", f"{p} is joining my team as my report.",
              f"Say hi to {p}, my new direct report."])
    return w, t, [has(p, ["manager", "reports-to", "boss"], "me"), lacks("me", ["manager", "boss"], f)]


def c_birthday(r):
    w = W(r)
    filler(w, r, r.randint(0, 2))
    n = r.randint(25, 60)
    t = P(r, [f"It's my birthday today, I turned {n}.", f"Turned {n} today. Birthday dinner tonight!", f"Happy birthday to me, {n} years old today.",
              f"I'm {n} as of today. Cake later.", f"Birthday today! {n}.", f"Today I turned {n}.", f"{n} today, feeling old.",
              f"Officially {n} today.", f"It's my {n}th birthday.", f"Celebrating turning {n} today."])
    return w, t, [{"has": {"subject": "me", "relation": ["birth", "born"], "other": "", "re": "__BIRTHDAY__"}}, {"n": n}]


def c_multi(r):
    w = W(r)
    a, b = r.sample(part(KIDS), 2)
    p = P(r, FIRST)
    t = P(r, [f"My partner is {p}. Our kids are {a} and {b}.", f"I live with {p} and our two kids, {a} and {b}.", f"Family: my wife {p}, kids {a} and {b}.",
              f"{p} is my husband; we have {a} and {b}.", f"{p} is my partner; our kids are {a} and {b}.", f"Me, my partner {p}, and the kids, {a} and {b}.",
              f"{p} and I have two kids: {a} and {b}.", f"My spouse is {p}; children {a} and {b}.", f"We're a family of four: me, {p}, {a}, and {b}.",
              f"I'm married to {p} and we have {a} and {b}."])
    kid = ["child", "kid", "son", "daughter", "parent-of", "children"]
    # "A family of four: me, P, A, and B" does not say who is the partner.
    rel = [] if "family of four" in t else ["partner", "spouse", "husband", "wife", "married"]
    return w, t, [has("me", rel, p), has("me", kid, a), has("me", kid, b)]


def c_other_update(r):
    w = W(r)
    filler(w, r)
    p = P(r, FIRST)
    w.fact(w.self, "partner", p, "person", since=False, other_kind="person")
    mine = w.fact(w.self, "works-at", P(r, ORGS), "person", other_kind="organization")
    t = P(r, [f"{p}'s work schedule changed: {p} works from home on Mondays and Fridays now.", f"{p} works from home Mondays and Fridays now.",
              f"New schedule for {p}: home on Mondays and Fridays.", f"From now on {p} is remote on Mondays and Fridays.",
              f"{p} is home Mondays and Fridays for work now.", f"{p} gets to work from home on Mondays and Fridays.",
              f"{p}'s remote days are Monday and Friday.", f"{p} switched to WFH on Mondays and Fridays.",
              f"Mondays and Fridays {p} works from home now.", f"{p} is remote on Mon and Fri now."])
    return w, t, [has(p, [], "Mon"), {"kept": mine}, lacks("me", [], "Monday")]


def c_other_near(r):
    w = W(r)
    filler(w, r)
    home = w.fact(w.self, "lives-in", f"{P(r, STREETS)} in {P(r, CITIES)}", "person")
    job = w.fact(w.self, "works-at", P(r, ORGS), "person", other_kind="organization")
    p = name(r)
    place, org = r.choice(["Coors Field", "Wash Park", "Union Station", "Red Rocks", "City Park"]), P(r, ORGS)
    t, ex = P(r, [
        (f"Coffee with {p} this morning; they live near {place} now.", [has(p, ["live", "near"], place.split()[0])]),
        (f"Had lunch with {p} at Sushi Den.", []),
        (f"{p} is moving to {P(r, TEAMS)} next month, they said.", []),
        (f"Ran into {p} at {place}; they work at {org} these days.", [has(p, WORK, org)]),
        (f"{p} got a new puppy named Juniper.", [has(p, [], "Juniper")]),
        (f"Drinks with {p} after work.", []),
        (f"{p} just moved to {P(r, CITIES)}.", []),
        (f"{p} told me they bought a house.", []),
        (f"{p} is starting at {org} next week.", [has(p, WORK + ["start", "join"], org)]),
        (f"Saw {p} at the farmers market.", []),
    ])
    return w, t, ex + [{"kept": home}, {"kept": job}, lacks("me", ["pet", "puppy", "dog"], "")]


# Graph categories: the meaning of a word comes from the notes.

def c_alias(r):
    """A person by another name: the fact of the entity changes, no new person."""
    w = W(r)
    filler(w, r)
    full = r.choice([("Robert", "Bob"), ("William", "Bill"), ("Elizabeth", "Liz"), ("Katherine", "Kate"), ("Alexander", "Sasha"),
                     ("Margaret", "Peggy"), ("Theodore", "Ted"), ("Jennifer", "Jen"), ("Michael", "Mike"), ("Rebecca", "Becca")])
    p = full[0] + " " + P(r, LAST)
    short = full[1]
    w.eid(p, "person", [short])
    old, new = r.sample(part(CITIES), 2)
    k = r.choice(["home", "job"])
    if k == "home":
        w.fact(w.self, "friend", p, "person", since=False)
        w.fact(p, "lives-in", old, "person")
        t = P(r, [f"{short} moved to {new}.", f"{short} lives in {new} now.", f"Heads up, {short} relocated to {new}.", f"{short} just moved out to {new}.",
                  f"{short} is in {new} now, moved last month.", f"{short}'s living in {new} these days.", f"{short} packed up and moved to {new}.",
                  f"{short} bought a place in {new}.", f"{short} left {old} for {new}.", f"{short} finally moved to {new}."])
        return w, t, [has(p, HOME, new), lacks(p, HOME, old), {"no_entity": short}]
    o1, o2 = r.sample(part(ORGS), 2)
    w.fact(w.self, "coworker", p, "person", since=False)
    w.fact(p, "works-at", o1, "person", other_kind="organization")
    t = P(r, [f"{short} left {o1} for {o2}.", f"{short} works at {o2} now.", f"{short} got a job at {o2}.", f"{short} switched to {o2}.",
              f"{short} is at {o2} now.", f"{short} took an offer from {o2}.", f"{short} jumped ship to {o2}.", f"{short} moved over to {o2}.",
              f"{short} quit {o1}; {o2} hired them.", f"{short} starts at {o2} Monday."])
    return w, t, [has(p, WORK + ["start"], o2), lacks(p, WORK, o1), {"no_entity": short}]


KIN = [("sister", "my sister"), ("brother", "my brother"), ("mother", "my mom"), ("father", "my dad"), ("partner", "my partner"),
       ("best-friend", "my best friend"), ("manager", "my boss"), ("neighbor", "our neighbor"), ("cousin", "my cousin"), ("aunt", "my aunt")]


def c_kin(r):
    """'My sister' is the person of the notes with that relation."""
    w = W(r)
    filler(w, r)
    rel, words = P(r, KIN)
    p = name(r) if rel not in ("partner",) else P(r, FIRST)
    w.fact(w.self, rel, p, "person", since=False, other_kind="person")
    W_ = words[0].upper() + words[1:]
    k = r.choice(["home", "job", "pet"])
    if k == "home":
        old, new = r.sample(part(CITIES), 2)
        w.fact(p, "lives-in", old, "person")
        t = P(r, [f"{W_} moved to {new}.", f"{W_} lives in {new} now.", f"{W_} just relocated to {new}.", f"{W_} is living in {new} now.",
                  f"{W_} finally moved to {new}.", f"{W_} bought a house in {new}.", f"{W_} left {old}; {new} now.", f"{W_} moved out to {new} last week.",
                  f"{W_} is in {new} these days.", f"{W_} moved to {new} for good."])
        return w, t, [has(p, HOME, new), lacks(p, HOME, old), lacks("me", HOME, new)]
    if k == "job":
        o1, o2 = r.sample(part(ORGS), 2)
        mine = w.fact(w.self, "works-at", P(r, [x for x in ORGS if x not in (o1, o2)] * 5), "person", other_kind="organization")
        w.fact(p, "works-at", o1, "person", other_kind="organization")
        t = P(r, [f"{W_} got a job at {o2}.", f"{W_} works at {o2} now.", f"{W_} switched to {o2}.", f"{W_} left {o1} for {o2}.",
                  f"{W_} started at {o2} this week.", f"{W_} took a new job at {o2}.", f"{W_} is with {o2} now.", f"{W_} got hired at {o2}!",
                  f"{W_} moved to {o2} from {o1}.", f"{W_}'s new job is at {o2}."])
        return w, t, [has(p, WORK + ["start", "hired"], o2), lacks(p, WORK, o1), {"kept": mine}, lacks("me", WORK, o2)]
    pet = P(r, ["Pepper", "Biscuit", "Miso", "Rocket", "Olive", "Waffles", "Mochi", "Captain", "Pickle", "Juniper"])
    t = P(r, [f"{W_} got a puppy named {pet}.", f"{W_} adopted a cat, {pet}.", f"{W_} has a new dog called {pet}.", f"{W_}'s new kitten is {pet}.",
              f"{W_} rescued a dog named {pet}.", f"{W_} got a dog, {pet}.", f"{W_} has a new pet named {pet}.", f"{W_} adopted {pet} from the shelter.",
              f"{W_} brought home a puppy, {pet}.", f"{W_}'s got a cat now, {pet}."])
    return w, t, [has(p, [], pet), lacks("me", [], pet)]


def c_same_name(r):
    """Two people with one first name: the words say which."""
    w = W(r)
    filler(w, r)
    first = P(r, FIRST)
    a, b = r.sample(part(LAST), 2)
    pa, pb = f"{first} {a}", f"{first} {b}"
    w.fact(w.self, "coworker", pa, "person", since=False, other_kind="person")
    w.fact(w.self, "neighbor", pb, "person", since=False, other_kind="person")
    city = P(r, CITIES)
    pet = P(r, ["Rex", "Luna", "Bear", "Ziggy", "Nala", "Scout", "Hank", "Poppy", "Tank", "Lola"])
    if r.random() < 0.5:
        t = P(r, [f"{first} from work is moving to {city}.", f"My coworker {first} moved to {city}.", f"{first} at the office relocated to {city}.",
                  f"{first} (work {first}) lives in {city} now.", f"Work {first} just moved to {city}.", f"{first} from my team is in {city} now.",
                  f"My colleague {first} moved to {city}.", f"{first} from the office bought a place in {city}.", f"The {first} I work with moved to {city}.",
                  f"{first} from work lives in {city} these days."])
        return w, t, [has(pa, HOME + ["moving", "moved"], city), lacks(pb, [], city), {"no_entity": first}]
    t = P(r, [f"Our neighbor {first} got a new dog named {pet}.", f"{first} next door adopted a dog, {pet}.", f"Neighbor {first} has a puppy now, {pet}.",
              f"{first} across the street got a dog called {pet}.", f"{first} next door has a new dog: {pet}.", f"The neighbor, {first}, got a puppy named {pet}.",
              f"{first} from next door rescued a dog named {pet}.", f"Our neighbor {first}'s new dog is {pet}.", f"{first} (the neighbor) got a dog, {pet}.",
              f"New puppy next door: {first} named it {pet}."])
    return w, t, [has(pb, [], pet), lacks(pa, [], pet), {"no_entity": first}]


def c_pet_ref(r):
    """'The dog' is the pet of the notes."""
    w = W(r)
    filler(w, r)
    pet, kind = P(r, PETS)
    w.fact(w.self, kind, pet, "person", since=False, other_kind="animal")
    old, new = doctor(r), doctor(r)
    w.fact(pet, "vet", old, "animal", other_kind="person")
    k = r.choice(["vet", "food", "age"])
    if k == "vet":
        t = P(r, [f"The {kind}'s new vet is {new}.", f"We switched the {kind} to {new}.", f"The {kind} sees {new} now.", f"New vet for the {kind}: {new}.",
                  f"The {kind} goes to {new} from now on.", f"Moved the {kind} over to {new}.", f"The {kind} has a new vet, {new}.",
                  f"{new} is the {kind}'s vet now.", f"Our {kind}'s vet is {new} now.", f"Changed the {kind}'s vet to {new}."])
        return w, t, [has(pet, ["vet", "veterinarian", "doctor"], new.split()[-1]), lacks(pet, ["vet"], old), {"no_entity": f"the {kind}"}, {"no_entity": kind}]
    if k == "food":
        food = r.choice(["salmon kibble", "grain-free food", "a kidney diet", "senior food", "wet food only"])
        t = P(r, [f"The {kind} is on {food} now.", f"Vet put the {kind} on {food}.", f"The {kind} eats {food} from now on.", f"Switched the {kind} to {food}.",
                  f"The {kind}'s food is {food} now.", f"Our {kind} only eats {food} now.", f"The {kind} has to eat {food}.", f"New food for the {kind}: {food}.",
                  f"The {kind} started {food}.", f"We changed the {kind}'s diet to {food}."])
        return w, t, [has(pet, [], food.split()[-2] if len(food.split()) > 1 else food), {"no_entity": f"the {kind}"}, {"no_entity": kind}, lacks("me", ["food", "diet", "eats"], "")]
    n = r.randint(3, 14)
    t = P(r, [f"The {kind} turned {n} today.", f"It's the {kind}'s birthday, {n} years old.", f"Our {kind} is {n} now.", f"The {kind} is {n} as of today.",
              f"Happy {n}th birthday to the {kind}.", f"The {kind} hit {n} this week.", f"Can't believe the {kind} is {n}.", f"The {kind} just turned {n}!",
              f"{n} years old today, the {kind}.", f"The {kind} celebrated {n} today."])
    return w, t, [has(pet, ["age", "birth", "born", "old"], ""), {"no_entity": f"the {kind}"}, lacks("me", ["birth", "age"], "")]


def c_restate(r):
    """The person says a fact that the notes already have: nothing changes."""
    w = W(r)
    filler(w, r)
    org, st, city = P(r, ORGS), P(r, STREETS), P(r, CITIES)
    w.fact(w.self, "works-at", org, "person", other_kind="organization")
    w.fact(w.self, "lives-in", f"{st} in {city}", "person")
    t = P(r, [f"Still at {org}, by the way.", f"We still live on {st}.", f"I'm still working at {org}.", f"Yep, still on {st} in {city}.",
              f"Same job, {org}.", f"Nothing new, still with {org}.", f"We're still on {st}, no plans to move.", f"Still {org} for work.",
              f"As you know, I work at {org}.", f"Still living on {st} in {city}."])
    return w, t, [{"quiet": True}]


def c_hypo(r):
    """A guess, a wish, or a fact of the past: nothing changes."""
    w = W(r)
    filler(w, r)
    org, other = r.sample(part(ORGS), 2)
    st = P(r, STREETS)
    w.fact(w.self, "works-at", org, "person", other_kind="organization")
    w.fact(w.self, "lives-in", f"{st} in {P(r, CITIES)}", "person")
    t = P(r, [f"If I left {org} I'd probably go to {other}.", f"I almost took a job at {other} once.", f"Imagine if we lived on the coast.",
              f"{other} reached out but I said no.", f"I wonder what working at {other} would be like.", f"Years ago I interviewed at {other}.",
              f"Should I apply to {other}? Just thinking.", f"A recruiter from {other} called; not interested.", f"We looked at a house in another town but passed.",
              f"In another life I'd work at {other}."])
    return w, t, [{"quiet": True}]


MAKERS = {
    "new-fact": c_new_fact, "change": c_change, "fix": c_fix, "end": c_end, "event": c_event,
    "third-person": c_third_person, "other-fact": c_other_fact,
    "small-talk": lambda r: c_quiet(r, SMALL, [{"no_change": True}]), "question": lambda r: c_quiet(r, QUESTIONS),
    "request": lambda r: c_quiet(r, REQUESTS), "plan": lambda r: c_quiet(r, PLANS),
    "reports-to-me": c_reports, "birthday": c_birthday, "multi-fact": c_multi, "other-update": c_other_update,
    "other-near-self": c_other_near, "alias": c_alias, "kin": c_kin, "same-name": c_same_name, "pet-ref": c_pet_ref,
    "restate": c_restate, "hypothetical": c_hypo,
}


def make(r, cat, i):
    w, text, checks = MAKERS[cat](r)
    at = when(r)
    styles = part(STYLES)
    if r.random() < 0.55:
        text = r.choice(styles)(text, r)
    out = []
    n = None
    for c in checks:
        if "n" in c:
            n = c["n"]
            continue
        out.append(c)
    for c in out:
        h = c.get("has")
        if h and h.get("re") == "__BIRTHDAY__":
            # The day and the month in either order, and no year or the right
            # one (Go regexp: no lookahead, so two expressions).
            m, d = at.strftime("%B"), at.day
            h["re"] = [rf"(?i)\b{m[:3]}[a-z]*\.?\s+{d}(st|nd|rd|th)?\b|\b{d}(st|nd|rd|th)?\s+(of\s+)?{m[:3]}|-{at.month:02d}-{d:02d}\b",
                       rf"^(?:.*\b{at.year - n}\b.*|(?:[^0-9]|\b\d{{1,2}}\b)*)$"]
    return {"id": f"{cat}-{PART}-{i:05d}", "category": cat, "when": at.isoformat(), "self": w.self, "notes": w.notes(),
            "text": text, "expect": [], "forbid": [], "allow_extra": True, "graph": out}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("out")
    ap.add_argument("--part", required=True, choices=["train", "dev", "test"])
    ap.add_argument("--n", type=int, default=45, help="cases for each category")
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--split", default="")
    ap.add_argument("--per-file", type=int, default=500)
    ap.add_argument("--only", default="")
    a = ap.parse_args()
    global PART
    PART = a.part
    r = random.Random(a.seed)
    cats = [c for c in MAKERS if not a.only or c in a.only.split(",")]
    cases = [make(r, cat, i) for cat in cats for i in range(a.n)]
    r.shuffle(cases)
    out = Path(a.out)
    out.mkdir(parents=True, exist_ok=True)
    split = a.split or a.part
    for k in range(0, len(cases), a.per_file):
        with open(out / f"{split}-{k // a.per_file:03d}.jsonl", "w") as f:
            for c in cases[k:k + a.per_file]:
                f.write(json.dumps(c, ensure_ascii=False) + "\n")
    print(f"{len(cases)} cases, {len(cats)} categories")


if __name__ == "__main__":
    main()
