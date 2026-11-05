"""A second, larger held-out set for the turn classifier. It was written
after the model was chosen, and it is not used for training or for the
choice of the model: it only measures the error rate. Other topics, and
mostly other words, than data.py and heldout.py.

  python heldout2.py OUT.jsonl
"""

import json
import sys

S = [
    "the sourdough starter needs flour and water every day for about a week before it is strong",
    "the tide is lowest at about two in the afternoon so the rock pools are easy to reach then",
    "your insurance covers the windshield but the deductible is one hundred and fifty dollars",
    "the spreadsheet adds the monthly totals in column f and the yearly total at the bottom",
    "the bus to the airport stops running at midnight so after that you need a taxi",
    "the bike chain makes that noise when it is dry so a little oil should fix it",
    "the new policy gives everyone two extra days off in december",
    "the service mesh adds a sidecar to each pod and the sidecar handles the retries",
    "the tomato plants want full sun and deep water about every three days",
    "the museum has a free day on the first sunday of each month",
    "the compiler warns because the variable is never read after the loop",
    "the printer is out of cyan so the photos come out a bit green",
    "your sister's birthday is on the twelfth and she mentioned she wants a new kettle",
    "the queue has eleven jobs and the oldest one waits for a free runner",
    "the hike is about nine kilometers with one steep part near the end",
]
Q = [
    "do you want me to order the kettle",
    "shall i put the museum trip on the calendar",
    "would you like me to restart the runner",
    "do you want me to read the whole policy",
    "should i book the taxi for midnight",
    "do you want the steep route or the long flat one",
    "shall i ask the apprentice to fix the warning",
    "is that the right insurance plan",
]

ACK = """yeah no that makes sense
okay that's good to hear
huh i never thought of that
oh nice i like that
ah that explains it
that's handy
mm right
okay right
oh is that so
wow okay i see
neat neat
gotcha okay
right yeah
uh huh sure
oh fun
oh that's lovely
good good
okay perfect
sure thing
i hear ya
yeah totally
mhm okay
oh yeah
yes yes i know
hmm interesting
oh huh
cool beans
heh nice
oh wow that's cheap
that's not bad
not bad at all
oh good
super
okay great thanks
oh that's useful
yeah that's fair
fair point
right right right
ok ok
okay i'm following
that's what i figured
sure makes sense
oh wow nine kilometers
aha
oh that's sad
aw that's nice
cool thanks
yep yep
oh i like that idea
yeah sounds about right""".splitlines()

TURN = """wait which column
hold on what deductible
stop for a sec
okay pause there
can you go back to the first part
no that's not my sister
actually make that the thirteenth
sorry what time does the bus stop
hang on where is the museum
how much oil should i use
is it free for kids too
can you add that to my notes
send me the route on my phone
put the policy on my phone so i can read it later
remind me tomorrow about the kettle
no no the other printer
that's the wrong plan
i think you mean column g
you said nine but the sign says twelve
let me interrupt you for a moment
okay i get it you can stop
that's enough for now thank you
stop i need to leave
wait wait wait
what does sidecar mean
why does it need a sidecar
can we talk about something else
change the subject please
what's the weather like at the coast
turn the music down
call the insurance company
how many jobs did you say
which runner is free
can you restart it now
cancel that job
is the oldest job mine
let me think about that
okay but what if it rains
yeah but i don't have a bike
right but the kettle is out of stock
mhm and how long does it take
cool and where do i park
excuse me can i ask a question
hey quick one
hey listen
sorry to cut you off
not that one
i didn't ask about that
skip the details
give me the short version
just tell me the price
go faster
slower please
louder
repeat the last bit
what was that
come on stop
enough already
that's way too much detail
okay never mind
forget the museum
hmm no let's not do that
i'd rather take the train
can you check my calendar first
open the spreadsheet
email this to my boss
is the printer fixed
what about magenta
do they deliver on sunday
who told you that
when did she say that
where did you hear that
is that true
are you sure about that
that doesn't sound right
i'm not sure that's correct""".splitlines()

ANSWER = """yes please do
yeah go ahead
sure book it
no don't bother
not yet maybe later
the flat one
the steep one sounds fun
yes read it
no just the summary
yeah restart it
please yes
nah i'll do it myself
okay do it
yep that's the one
no that's the old plan
uh huh go ahead
sounds good do it
mm hmm yes
sure why not
no thanks""".splitlines()

UNCLEAR = """uh
um
er
so
and
but
well
like
so um
and uh
i
the
i um
okay so
so the""".splitlines()


def make():
    out = []
    for i, t in enumerate(ACK):
        out.append({"context": S[i % len(S)], "text": t, "label": "ack"})
    for i, t in enumerate(TURN):
        out.append({"context": (S + Q)[i % (len(S) + len(Q))], "text": t, "label": "turn"})
    for i, t in enumerate(ANSWER):
        out.append({"context": Q[i % len(Q)], "text": t, "label": "turn"})
    for i, t in enumerate(UNCLEAR):
        out.append({"context": (S + Q)[i % (len(S) + len(Q))], "text": t, "label": "unclear"})
    return out


if __name__ == "__main__":
    with open(sys.argv[1], "w") as f:
        for r in make():
            f.write(json.dumps(r) + "\n")
