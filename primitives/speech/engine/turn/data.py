"""Training data for the turn classifier (docs/speech-interaction.md, 2.3.1).

The classifier gets two texts:
  context: the last words that the persona spoke before the overlap
  text:    the words of the person during the overlap (speech-to-text, so
           lower case and with little punctuation; often only the first
           words)
and gives three classes:
  ack      a backchannel, an acknowledgment, or a reaction: the persona
           continues ("mm-hm", "wow okay", "that makes sense")
  turn     the person takes the turn: the persona stops ("wait", a
           question, a correction, a new topic, an answer to a question
           that the persona asked)
  unclear  too few words to know ("so", "um", "and the")

The data is made from parts, in many combinations, with noise like that of
speech-to-text. It teaches the idea, not a list: heldout.py has a separate
test set with other words and other topics.

  python data.py OUT.jsonl [--n 40000] [--seed 1]
"""

import json
import random
import re
import sys

ACK, TURN, UNCLEAR = "ack", "turn", "unclear"

# Persona speech that is not a question at its end. Topics on purpose
# differ from heldout.py.
STATEMENTS = [
    "first rinse the rice in cold water until the water runs clear then add it to the pot",
    "the recipe needs two cups of flour one teaspoon of baking soda and a pinch of salt",
    "after that you let the dough rest for about thirty minutes on the counter",
    "the forecast for tomorrow shows light rain in the morning and clear skies after noon",
    "high pressure usually means calm weather while low pressure often brings storms",
    "your meeting with the design team moved from two o'clock to three thirty",
    "the build failed because a test in the conversation package timed out",
    "i started a task that adds the retry logic to the upload client",
    "the apprentice changed four files and all the checks passed on the first try",
    "the branch has three commits and the diff touches the speech service and its tests",
    "the release went out to all three runners and they reported healthy",
    "to reset the router hold the button on the back for ten seconds",
    "the dishwasher shows error code four which usually means a blocked drain filter",
    "your package left the warehouse this morning and should arrive on thursday",
    "the library closes at eight tonight and opens at nine tomorrow",
    "photosynthesis turns light water and carbon dioxide into sugar and oxygen",
    "the treaty was signed in seventeen eighty three and ended the war",
    "a barometer measures the pressure of the air around it",
    "the kids have soccer practice on tuesday and a dentist appointment on friday",
    "you spent about forty dollars on groceries this week which is less than last week",
    "the garden needs water twice a week while it is this hot",
    "the oven should be at three fifty before you put the tray in",
    "the error comes from a null pointer in the token refresh code",
    "the proxy blocked two requests to an unknown host during the last task",
    "next you open the settings page and choose the network tab",
    "the train leaves at six forty and the trip takes about two hours",
    "the vet said the dog should eat smaller meals for a week",
    "your flight lands at eleven fifteen at gate twenty two",
    "there are three ways to fix this and the simplest is to restart the service",
    "the database migration adds two columns and keeps the old data",
    "in the second step the script copies the files to the new host",
    "the paint needs about four hours to dry before the second coat",
    "the moon is about three hundred eighty thousand kilometers away",
    "the main reason is that the cache keeps old entries for too long",
    "so the plan is to finish the parser today and the tests tomorrow",
    "the store has the blue one in stock but the red one is sold out",
    "the thermostat is set to sixty eight and the house is at sixty five",
    "most people need seven to nine hours of sleep each night",
    "the contract renews in march unless you cancel it before then",
    "the first part of the report covers the costs and the second part covers risks",
]

# Persona speech that ends with a question to the person.
QUESTIONS = [
    "would you like me to set a timer for that",
    "do you want me to start that task now",
    "should i add milk to the shopping list",
    "do you want the short version or the long one",
    "shall i send this to your phone",
    "do you want me to approve the change",
    "should i remind you about it tomorrow morning",
    "would you like me to record this in your knowledge graph",
    "can i cancel the old task",
    "do you want me to read the next step",
    "is that the one you meant",
    "do you want me to merge it",
    "should i try again",
    "do you want me to book it",
    "would you like the recipe for that",
    "shall i turn the lights off",
    "do you want me to call her",
    "is the meeting still on",
    "should i use the same settings as last time",
    "which one do you want the first or the second",
]

# A question earlier, then more statement: an assent is then a backchannel.
QUESTION_THEN = [
    "do you want the details here they are first you open the panel",
    "would you like to know more the short answer is that it depends on the soil",
    "should i explain well the key idea is that each step waits for the last one",
]

ACK_CORE = [
    "mm hmm", "mhm", "uh huh", "mm", "yeah", "yep", "yes", "right", "okay", "ok", "sure",
    "i see", "got it", "gotcha", "makes sense", "that makes sense", "uh huh yeah", "okay yeah",
    "right right", "yeah yeah", "oh okay", "ah okay", "oh i see", "ah i see", "interesting",
    "that's interesting", "that's very interesting", "wow", "oh wow", "cool", "nice", "oh nice",
    "great", "perfect", "good", "very good", "sounds good", "fair enough", "true", "exactly",
    "totally", "of course", "understood", "okay got it", "alright", "all right", "yeah okay",
    "huh interesting", "oh cool", "neat", "wow okay", "oh good", "good to know", "ha", "ha ha",
    "oh really", "really", "no way wow", "oh that's good", "that's great", "that's cool", "i didn't know that",
    "oh i didn't know that", "huh", "ah", "oh", "okay cool", "yeah that's right", "right okay",
    "mm okay", "thanks", "okay thanks", "nice okay", "wow that's a lot", "that's amazing",
    "oh that's smart", "okay that helps", "that helps", "yeah makes sense", "uh huh okay",
]

# An optional second part that keeps it a backchannel.
ACK_TAIL = ["", "", "", "", " okay", " yeah", " go on", " keep going", " thanks", " nice", " cool", " i see", " mm hmm"]

TURN_STOP = [
    "stop", "wait", "hold on", "hang on", "wait wait", "stop stop", "okay stop", "pause", "one second",
    "one sec", "hold up", "wait a second", "wait a minute", "hang on a second", "be quiet", "quiet",
    "enough", "that's enough", "never mind", "cancel that", "forget it", "stop talking", "shush",
    "okay that's enough", "no stop", "please stop", "yeah okay stop", "alright stop", "sorry wait",
    "excuse me", "sorry to interrupt", "can i stop you", "let me interrupt", "skip that", "skip",
    "next", "go back", "repeat that", "say that again", "slow down",
]

TURN_OPENERS = [
    "wait", "actually", "no", "hold on", "sorry", "but", "okay but", "yeah but", "hmm but", "oh wait",
    "um actually", "well actually", "no no", "hang on", "hey", "excuse me", "quick question", "sorry but",
    "", "", "", "",
]

TURN_BODIES = [
    "what about the {thing}", "how long does that take", "how much does it cost", "why is that",
    "what do you mean", "can you say that again", "can you repeat the last part", "is that right",
    "are you sure", "that's wrong", "that's not right", "that's not what i asked", "i meant the {thing}",
    "it's the other {thing}", "not that one", "i said the {thing}", "i already did that",
    "can you check the {thing}", "what time is it", "what's the weather tomorrow", "remind me to call mom",
    "add eggs to the list", "turn on the lights", "send that to my phone", "start a new task for the {thing}",
    "cancel the task", "what's the status of the {thing}", "who is on the call", "where did i put the {thing}",
    "how do i fix the {thing}", "do i need the {thing}", "should i use the {thing}", "can we talk about the {thing}",
    "let's do the {thing} instead", "i don't want that", "i don't think so", "that doesn't make sense",
    "you got the date wrong", "the {thing} is broken", "tell me more about the {thing}", "go back to the {thing}",
    "which {thing}", "when is the {thing}", "is the {thing} ready", "what happened to the {thing}",
    "i have a question", "can i ask something", "let me ask you something", "change of plans",
    "different question", "read me the {thing} instead", "skip to the {thing}",
]

THINGS = [
    "oven", "meeting", "build", "second step", "router", "red one", "flight", "report", "dishwasher",
    "garden", "invoice", "deploy", "kids", "car", "parser", "release", "dentist", "budget", "timer",
    "recipe", "tests", "proxy", "calendar", "dog", "package",
]

# Answers to a question that the persona asked: a turn after QUESTIONS,
# a backchannel elsewhere for the assent words.
ASSENT = ["yes", "yeah", "yep", "sure", "okay", "ok", "yes please", "yeah please", "sure go ahead", "yes do it",
          "go ahead", "please do", "absolutely", "definitely", "of course", "sounds good", "yeah sure", "uh huh",
          "mm hmm", "right", "alright", "perfect", "great"]
ANSWER_ONLY = ["no", "no thanks", "nope", "not now", "no don't", "maybe later", "the first one", "the second one",
               "the short one", "the long version", "yes but tomorrow", "no just the {thing}", "not yet",
               "yeah do it now", "yes and also the {thing}", "no cancel it", "do the {thing} first"]

UNCLEAR_WORDS = ["um", "uh", "so", "and", "but", "well", "i", "the", "uh so", "um so", "so um", "and um",
                 "but i", "i was", "well i", "so the", "and the", "i think", "if", "can", "so if", "um i",
                 "uh the", "and then", "so i", "i mean", "you", "do", "is", "what if", "but the", "it's", "hmm"]

FILLERS = ["um ", "uh ", "", "", "", "", "", ""]

# Parts for many backchannels and turns made from smaller pieces, so that
# the model learns what they do, not the exact phrases.
INTERJ = ["oh", "ah", "wow", "huh", "hmm", "ha", "aha", "oh man", "oh wow", "man", "gosh", "oh my", "geez", "aw"]
ADJ = ["interesting", "cool", "neat", "nice", "great", "smart", "clever", "crazy", "funny", "helpful", "useful",
       "good", "amazing", "awesome", "lovely", "weird", "surprising", "impressive", "fantastic", "wonderful",
       "brilliant", "handy", "fun", "beautiful", "strange", "excellent", "important", "clear", "simple", "easy",
       "tricky", "scary", "sad", "exciting"]
EVAL = ["that's {a}", "{a}", "how {a}", "so {a}", "that's really {a}", "that's pretty {a}", "very {a}",
        "really {a}", "that sounds {a}", "sounds {a}", "it's {a}", "super {a}", "that is {a}", "that's so {a}"]
UNDERSTAND = ["i see", "i get it", "i understand", "makes sense", "that makes sense", "fair", "fair enough",
              "got it", "gotcha", "understood", "okay", "alright", "right", "sure", "yeah", "yep", "uh huh", "mhm",
              "mm hmm", "true", "exactly", "totally", "absolutely", "definitely", "of course", "no doubt",
              "good to know", "i didn't know that", "i never knew that", "learn something new", "i knew it",
              "i thought so", "that's what i thought", "same here", "me too", "agreed", "yes exactly",
              "that's true", "so true", "right on time", "okay okay okay", "yeah i remember", "oh yeah i remember",
              "that rings a bell", "i hear you", "point taken", "go on", "keep going", "carry on", "continue",
              "i'm listening", "mm", "uh huh go on"]
SYMPATHY = ["oh no", "ouch", "aw man", "bummer", "that's too bad", "oh dear", "that's a shame", "poor thing",
            "ugh", "that sucks", "oh that's annoying", "that's rough"]
THANKS = ["thanks", "thank you", "thanks a lot", "cheers", "much appreciated", "nice thanks", "great thank you"]
LAUGH = ["ha ha", "haha", "ha", "hehe", "lol", "that's hilarious", "ha nice"]


def gen_ack(rng):
    r = rng.random()
    if r < 0.3:
        parts = [rng.choice(EVAL).format(a=rng.choice(ADJ))]
    elif r < 0.6:
        parts = [rng.choice(UNDERSTAND)]
    elif r < 0.7:
        parts = [rng.choice(SYMPATHY)]
    elif r < 0.8:
        parts = [rng.choice(THANKS)]
    elif r < 0.85:
        parts = [rng.choice(LAUGH)]
    elif r < 0.93:
        parts = [rng.choice(NEG_ACK)]
    else:
        parts = [rng.choice(ACK_CORE)]
    if rng.random() < 0.35:
        parts.insert(0, rng.choice(INTERJ))
    if rng.random() < 0.35:
        parts.append(rng.choice(UNDERSTAND + [rng.choice(EVAL).format(a=rng.choice(ADJ))]))
    return " ".join(parts)


VERBS = ["set", "add", "remove", "call", "text", "email", "send", "open", "close", "start", "cancel", "approve",
         "merge", "check", "read", "play", "turn on", "turn off", "book", "order", "find", "look up", "schedule",
         "move", "delete", "save", "show me", "tell me", "explain", "write", "fix", "restart", "pause", "stop",
         "reject", "revise", "deploy", "buy", "record", "note"]
OBJ = ["the timer", "an alarm for six", "milk", "the lights", "my wife", "the plumber", "the report", "that",
       "the task", "the build", "the second task", "the oven", "the list", "the music", "a table for two",
       "the meeting", "the calendar", "the tests", "the diff", "the change", "it", "this", "the email", "my phone",
       "the doctor", "the last one", "the first step", "the recipe", "the logs", "the server", "the deploy",
       "the bill", "the tickets", "the apprentice", "the review", "the weather", "the news", "the summary"]
QWORDS = ["what", "how", "why", "when", "where", "who", "which"]
QFRAMES = ["{q} is {o}", "{q} about {o}", "{q} do i {v} {o}", "{q} does {o} work", "{q} did you {v} {o}",
           "{q} happens with {o}", "{q} should i {v}", "{q} time is {o}", "{q} long is {o}", "{q} much is {o}",
           "can you {v} {o}", "could you {v} {o}", "would you {v} {o}", "will you {v} {o}", "please {v} {o}",
           "i want you to {v} {o}", "i need {o}", "i'd like to {v} {o}", "let's {v} {o}", "go ahead and {v} {o}",
           "is {o} done", "is {o} ready", "did {o} pass", "does {o} need to", "are we done with {o}",
           "{v} {o}", "{v} {o} please", "{v} {o} now", "do you know {q} {o} is", "tell me about {o}",
           "remind me to {v} {o}", "don't {v} {o}", "never {v} {o}", "can we {v} {o} later", "should we {v} {o}",
           "what's going on with {o}", "how's {o} going", "any news on {o}", "status of {o}"]
CORRECT = ["no it's {o}", "that's not {a}", "you're wrong", "that's incorrect", "not quite", "i didn't say that",
           "that's the wrong one", "nope wrong {t}", "no not {o}", "it was {o} not that", "you misheard me",
           "that is not what i meant", "i meant {o}", "wrong", "incorrect", "that's backwards", "no that's old",
           "that's outdated", "you skipped {o}", "you forgot {o}", "that isn't it", "no no no", "not true",
           "i disagree", "i don't agree", "that's not how it works", "that can't be right"]
STOPS = ["stop {s}", "wait {s}", "hold on {s}", "hang on {s}", "pause {s}", "enough {s}", "okay stop {s}",
         "please be quiet", "stop it", "stop now", "can you stop", "could you stop", "you can stop",
         "no more", "that's all i needed", "i've heard enough", "cut it", "end it", "be silent", "shh",
         "quiet please", "stop speaking", "stop there", "let me talk", "let me finish", "listen", "listen to me",
         "hey listen", "magi stop", "computer stop", "computer", "magi", "hey magi", "hey computer", "sorry hold on",
         "sorry one moment", "just a sec", "one moment", "not now", "later", "shut it", "mute", "skip this",
         "skip ahead", "next one", "go to the next", "back", "previous", "start over", "from the top", "again"]
STOP_TAIL = ["", "", "please", "a second", "a sec", "a minute", "for a moment", "there", "right there", "now"]
# Words that call for attention, and endings that close the reply ("that's
# enough, thanks"): a turn, also with a polite word after them.
ATTENTION = ["hey", "hey hey", "yo", "hello", "hello hello", "um hello", "hey there", "excuse me excuse me",
             "sorry sorry", "uh hey", "psst", "okay hey", "magi", "computer", "hey magi", "magi magi"]
CLOSING = ["that's enough", "that's all", "that's it", "i'm good", "i'm all set", "that'll do", "good enough",
           "i've got it", "i got it", "no need", "no more please", "that's fine you can stop", "okay i'm done",
           "i'm done", "we're done", "all done", "that covers it", "i think i got it", "enough for now"]
POLITE = ["", "thanks", "thank you", "please", "cheers", "thanks a lot", "thanks though"]
# A complaint about the reply is a turn: the person wants it shorter or
# different.
COMPLAINT = ["too long", "this is too long", "too much", "that's a lot of words", "get to the point",
             "you're rambling", "this is boring", "i don't need all that", "less detail please",
             "you talk too much", "way too long", "shorter please", "keep it short", "i asked a simple question",
             "that's not helpful", "this isn't helping", "you lost me", "i'm confused", "this is confusing",
             "too many details", "can you summarize", "just the answer please", "bottom line please",
             "that's more than i need", "tl dr"]
# A backchannel can hold a negative word: "not bad", "yeah no I agree".
NEG_ACK = ["not bad", "not too bad", "not bad at all", "no kidding", "no way", "no way really", "yeah no i agree",
           "yeah no totally", "no that's great", "no i get it", "not surprised", "can't argue with that",
           "i can't complain", "not too shabby", "no doubt about it", "never knew that", "nothing wrong with that",
           "no that makes sense", "not a bad idea", "isn't that something", "who knew", "you don't say",
           "oh is it", "oh do they", "oh really now"]
STRONG_ASSENT = ["absolutely", "definitely", "yes absolutely", "yes definitely", "for sure yes", "yes of course",
                 "certainly", "yes yes", "yeah definitely", "oh yes", "yes that would be great", "please yes",
                 "yeah go", "do that", "make it so", "go for it", "affirmative", "yes go", "mm hmm do it",
                 "uh huh yes", "yeah go ahead and do it", "okay go ahead", "sure do that", "yep go on then",
                 "mhm please", "ok sure go", "alright do it"]


def gen_turn(rng):
    r = rng.random()
    o, v, q, a = rng.choice(OBJ), rng.choice(VERBS), rng.choice(QWORDS), rng.choice(ADJ)
    if r < 0.45:
        t = rng.choice(QFRAMES).format(q=q, o=o, v=v)
    elif r < 0.65:
        t = rng.choice(CORRECT).format(o=o, a=a, t=rng.choice(THINGS))
    elif r < 0.78:
        t = rng.choice(STOPS).format(s=rng.choice(STOP_TAIL))
    elif r < 0.85:
        t = rng.choice(COMPLAINT)
    elif r < 0.93:
        t = rng.choice(CLOSING) + " " + rng.choice(POLITE)
    else:
        t = rng.choice(ATTENTION)
    if rng.random() < 0.3:
        t = rng.choice(TURN_OPENERS) + " " + t
    if rng.random() < 0.15:  # a backchannel first, then the turn
        t = rng.choice(UNDERSTAND[:20]) + " " + rng.choice(["but", "and", "so", "", "wait"]) + " " + t
    return " ".join(t.split())


def noise(rng, s):
    """Speech-to-text noise: casing, punctuation, repeats, fillers."""
    s = s.replace("{thing}", rng.choice(THINGS))
    if rng.random() < 0.15:
        s = rng.choice(FILLERS) + s
    if rng.random() < 0.1:
        w = s.split()
        if w:
            i = rng.randrange(len(w))
            w.insert(i, w[i])  # a stutter
            s = " ".join(w)
    if rng.random() < 0.3:
        s = s.capitalize() + rng.choice([".", "?", "!", ","])
    if rng.random() < 0.05:
        s = s.replace("'", "")
    return s


def context(rng, pool):
    """The last 6 to 22 words of a persona text."""
    words = rng.choice(pool).split()
    n = rng.randint(6, 22)
    if pool is QUESTIONS:
        # A question ends the context; a statement can come before it.
        words = (rng.choice(STATEMENTS).split() + words)[-n:] if len(words) < n else words
    else:
        # The overlap can be mid-sentence; after QUESTION_THEN, only after
        # the statement started.
        cut = rng.randint(min(len(words), 9 if pool is QUESTION_THEN else 6), len(words))
        words = words[max(0, cut - n):cut]
    s = " ".join(words)
    if pool is QUESTIONS and rng.random() < 0.6:
        s += "?"
    return s


def norm(s):
    """The same normalization as the engine."""
    s = s.lower().replace("’", "'")
    s = re.sub(r"[^a-z0-9' ]+", " ", s)
    return " ".join(s.split())


def sample(rng):
    r = rng.random()
    if r < 0.30:  # backchannel
        pool = rng.choice([STATEMENTS] * 6 + [QUESTION_THEN])
        a = rng.choice(ACK_CORE) + rng.choice(ACK_TAIL)
        if rng.random() < 0.15:
            a = rng.choice(ACK_CORE) + " " + rng.choice(ACK_CORE)
        if rng.random() < 0.6:
            a = gen_ack(rng)
        return context(rng, pool), noise(rng, a), ACK
    if r < 0.40:  # assent: depends on the context
        a = rng.choice(ASSENT)
        if rng.random() < 0.5:
            if rng.random() < 0.4:
                a = rng.choice(STRONG_ASSENT)
            return context(rng, QUESTIONS), noise(rng, a), TURN
        return context(rng, rng.choice([STATEMENTS] * 4 + [QUESTION_THEN])), noise(rng, a), ACK
    if r < 0.46:  # answers that are a turn anywhere
        return context(rng, rng.choice([QUESTIONS, STATEMENTS])), noise(rng, rng.choice(ANSWER_ONLY)), TURN
    if r < 0.58:  # stop
        return context(rng, rng.choice([STATEMENTS, STATEMENTS, QUESTIONS])), noise(rng, rng.choice(TURN_STOP)), TURN
    if r < 0.66:  # a backchannel, then a turn ("okay but wait", "yeah stop")
        a = rng.choice(ACK_CORE) + " " + rng.choice([rng.choice(TURN_STOP), rng.choice(TURN_OPENERS[:12]) + " " + rng.choice(TURN_BODIES)])
        return context(rng, STATEMENTS), noise(rng, a), TURN
    if r < 0.86:  # a question, correction, or new topic
        t = (rng.choice(TURN_OPENERS) + " " + rng.choice(TURN_BODIES)).strip()
        if rng.random() < 0.6:
            t = gen_turn(rng)
        w = t.replace("{thing}", rng.choice(THINGS)).split()
        if len(w) > 2 and rng.random() < 0.3:  # only the first words were heard
            t = " ".join(w[:rng.randint(2, len(w))])
        return context(rng, rng.choice([STATEMENTS, STATEMENTS, QUESTIONS])), noise(rng, t), TURN
    # unclear
    u = rng.choice(UNCLEAR_WORDS)
    return context(rng, rng.choice([STATEMENTS, STATEMENTS, QUESTIONS])), noise(rng, u), UNCLEAR


def make(n, seed, exclude=()):
    """n samples. Texts in exclude (the held-out texts) are left out."""
    rng = random.Random(seed)
    exclude = set(exclude)
    out = []
    while len(out) < n:
        c, t, y = sample(rng)
        t = norm(t)
        if t and t not in exclude:
            out.append({"context": norm(c), "text": t, "label": y})
    return out


if __name__ == "__main__":
    args = sys.argv[1:]
    path = args[0]
    n = int(args[args.index("--n") + 1]) if "--n" in args else 40000
    seed = int(args[args.index("--seed") + 1]) if "--seed" in args else 1
    with open(path, "w") as f:
        for r in make(n, seed):
            f.write(json.dumps(r) + "\n")
