"""Held-out test set for the turn classifier: other topics and other words
than data.py. It measures whether the model learned the idea of a
backchannel and a turn, not the phrases of the training data. Each item is
(context kind, text, label); contexts come from S (statements) or Q (the
persona asked a question).

  python heldout.py OUT.jsonl
"""

import json
import sys

S = [
    "to change the oil you first warm up the engine for a few minutes and then park on level ground",
    "the chorus comes back after the bridge and the key moves up by a whole step",
    "for the visa you need a passport that is valid for six more months and two photos",
    "in this opening white gives up a pawn to get fast development of the pieces",
    "you cast on forty stitches and knit every row until the scarf is long enough",
    "the deduction applies only when your income is below the limit for that year",
    "the pod restarts because the liveness probe fails before the app is ready",
    "for the party we have balloons and the cake comes from the bakery at four",
    "the leak is probably at the joint under the sink where the washer is old",
    "the trail climbs for three miles and then follows the ridge to the lake",
    "the merge request adds a new endpoint for the export and updates the docs",
    "the battery in your laptop has lost about a fifth of its original capacity",
]
Q = [
    "do you want me to order a new washer",
    "should i put the party on the shared calendar",
    "would you like me to open a task to fix the probe",
    "shall i read you the full list of documents",
    "do you want the easy route or the hard one",
    "should i ask the apprentice to revise it",
    "want me to text your sister about the cake",
    "should i check the price of a new battery",
]

ITEMS = [
    # Backchannels and reactions, words not in the training parts.
    ("S", "oh neat", "ack"), ("S", "huh cool", "ack"), ("S", "that tracks", "ack"),
    ("S", "ah gotcha gotcha", "ack"), ("S", "nice one", "ack"), ("S", "oh that's clever", "ack"),
    ("S", "mhm mhm", "ack"), ("S", "for sure", "ack"), ("S", "yeah yeah yeah", "ack"),
    ("S", "oh wow i had no idea", "ack"), ("S", "that's wild", "ack"), ("S", "sweet", "ack"),
    ("S", "awesome", "ack"), ("S", "lovely", "ack"), ("S", "yup", "ack"), ("S", "indeed", "ack"),
    ("S", "uh huh uh huh", "ack"), ("S", "okay okay", "ack"), ("S", "right on", "ack"),
    ("S", "oh that's helpful", "ack"), ("S", "good stuff", "ack"), ("S", "ooh", "ack"),
    ("S", "whoa", "ack"), ("S", "oh boy", "ack"), ("S", "makes total sense", "ack"),
    ("S", "i follow", "ack"), ("S", "i'm with you", "ack"), ("S", "noted", "ack"),
    ("S", "okay noted", "ack"), ("S", "copy that", "ack"), ("S", "roger", "ack"),
    ("S", "fascinating", "ack"), ("S", "that's so cool", "ack"), ("S", "wow that's really interesting okay", "ack"),
    ("S", "hmm okay", "ack"), ("S", "ah right", "ack"), ("S", "oh right", "ack"), ("S", "sure sure", "ack"),
    ("S", "yeah i figured", "ack"), ("S", "figures", "ack"), ("S", "ha that's funny", "ack"),
    ("S", "oh no", "ack"), ("S", "yikes", "ack"), ("S", "oof", "ack"), ("S", "nice nice", "ack"),
    ("S", "cool cool", "ack"), ("S", "perfect thanks", "ack"), ("S", "oh good to hear", "ack"),
    ("S", "yes", "ack"), ("S", "yeah", "ack"), ("S", "okay", "ack"), ("S", "sure", "ack"),
    ("S", "mm hmm", "ack"), ("S", "right", "ack"),
    # Turns: stops in other words.
    ("S", "whoa whoa back up", "turn"), ("S", "scratch that", "turn"), ("S", "let me stop you there", "turn"),
    ("S", "hold it", "turn"), ("S", "time out", "turn"), ("S", "okay okay enough", "turn"),
    ("S", "can you stop for a second", "turn"), ("S", "wait up", "turn"), ("S", "hush", "turn"),
    ("S", "stop reading", "turn"), ("S", "that's plenty thanks", "turn"), ("S", "nope stop", "turn"),
    ("S", "hey hey hey", "turn"), ("S", "just a moment", "turn"), ("S", "give me a second", "turn"),
    ("S", "i got it you can stop", "turn"), ("S", "okay i don't need the rest", "turn"),
    # Turns: questions, corrections, new topics.
    ("S", "how many miles was that", "turn"), ("S", "which joint", "turn"), ("S", "is the bakery open on sunday", "turn"),
    ("S", "hmm no i think you're mistaken", "turn"), ("S", "that isn't correct", "turn"), ("S", "you mixed that up", "turn"),
    ("S", "quick thing before you go on", "turn"), ("S", "hey can we switch to the taxes", "turn"),
    ("S", "what's a liveness probe", "turn"), ("S", "why forty stitches", "turn"), ("S", "but what if it rains", "turn"),
    ("S", "uh how long is the drive", "turn"), ("S", "sorry what was the first thing", "turn"),
    ("S", "no the other laptop", "turn"), ("S", "it's actually a key change down", "turn"),
    ("S", "can you text that to me", "turn"), ("S", "put that on my phone", "turn"),
    ("S", "remind me about the oil change on saturday", "turn"), ("S", "what's on my calendar today", "turn"),
    ("S", "play some music", "turn"), ("S", "is it going to snow", "turn"), ("S", "okay but which trail", "turn"),
    ("S", "yeah but the passport expired", "turn"), ("S", "right but i don't have photos", "turn"),
    ("S", "mhm and what about the cake", "turn"), ("S", "cool can you also order candles", "turn"),
    ("S", "okay and then what", "turn"), ("S", "wait how", "turn"), ("S", "huh why", "turn"),
    ("S", "what", "turn"), ("S", "pardon", "turn"), ("S", "come again", "turn"),
    ("S", "i didn't catch that", "turn"), ("S", "louder please", "turn"), ("S", "can you go slower", "turn"),
    ("S", "no", "turn"), ("S", "nah that's wrong", "turn"),
    # Answers to a question of the persona.
    ("Q", "yes", "turn"), ("Q", "yeah", "turn"), ("Q", "yup go for it", "turn"), ("Q", "please", "turn"),
    ("Q", "do it", "turn"), ("Q", "sure thing", "turn"), ("Q", "nah", "turn"), ("Q", "no not yet", "turn"),
    ("Q", "the easy one", "turn"), ("Q", "the hard route", "turn"), ("Q", "hmm maybe tomorrow", "turn"),
    ("Q", "yes and tell her four o'clock", "turn"), ("Q", "absolutely yes", "turn"), ("Q", "okay", "turn"),
    ("Q", "mm hmm", "turn"), ("Q", "sounds great", "turn"), ("Q", "no thank you", "turn"), ("Q", "don't bother", "turn"),
    ("Q", "go ahead and do that", "turn"), ("Q", "yeah why not", "turn"),
    # Too early to tell.
    ("S", "uh", "unclear"), ("S", "um", "unclear"), ("S", "so", "unclear"), ("S", "and uh", "unclear"),
    ("S", "but um", "unclear"), ("S", "well so", "unclear"), ("S", "so like", "unclear"), ("S", "i uh", "unclear"),
    ("S", "like", "unclear"), ("S", "the uh", "unclear"), ("S", "er", "unclear"), ("S", "and so", "unclear"),
    ("Q", "um", "unclear"), ("Q", "uh", "unclear"), ("Q", "well", "unclear"), ("Q", "so", "unclear"),
]


def make():
    out = []
    for i, (kind, text, label) in enumerate(ITEMS):
        ctx = (S if kind == "S" else Q)[i % len(S if kind == "S" else Q)]
        out.append({"context": ctx, "text": text, "label": label})
    return out


if __name__ == "__main__":
    with open(sys.argv[1], "w") as f:
        for r in make():
            f.write(json.dumps(r) + "\n")
