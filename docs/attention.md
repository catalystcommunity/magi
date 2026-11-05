# Attention: devices and notices

This document tells how Magi gets the attention of a person: on which
device, how, and when. The model is the computer of a starship. You talk
to it from where you are. It tells you about events when it is correct to
interrupt you, and it sends information to where you want to read it ("send
the report to my phone").

## 1. Devices

A device is a client of one person: a phone, a browser, a desktop, or a
speaker. Each client:

1. Makes a random device id one time and keeps it.
2. Says hello with `put-device` when it starts, about once a minute, and
   when its state changes. The hello gives:
   - `kind`: `phone`, `browser`, `desktop`, or `speaker`.
   - `name`: for example "Pixel 9" or "Firefox on Linux".
   - `can`: `speak` (it can play speech), `notify` (it can show system
     notifications when the client is not open), `show` (it has a screen).
   - `active`: the person uses the client now (it is in the foreground, or
     hands-free talk is on).
   - `hands_free`: the microphone stays on.
3. Waits for notices with `wait-notices` (a long poll of up to 25 s). The
   client connects out. Magi never connects to a device.

The conversation service keeps the devices. Only the person (or a service
with act-as) can see the devices of a person.

## 2. Notices

A notice tells the person about a persona message. The message has the
modality `notice` and is in the stream of the persona, so the conversation
keeps it. The notice has:

- `title`: a short title.
- `urgency`:
  - `low`: show it. Do not say it.
  - `normal`: say it at the next pause: when no persona speaks and the
    person does not speak.
  - `high`: say it at once. A persona that speaks stops (reason `notice`).
- `device`: the device that the notice is for. Without it, all devices of
  the person get the notice.

A notice that is said (`normal` or `high`) gets its audio in parts, as a
spoken reply. Thus a client can start to say it in less than one second.

### 2.1 What a client does with a notice

| Condition | Action | `how` |
| --- | --- | --- |
| The client can play audio, the person uses it, and urgency is not `low` | Chime, then say the notice | `spoken` |
| The client is not in use and it can show system notifications | System notification | `notified` |
| All other conditions | Show it in the notice tray | `shown` |

The client records the delivery with `deliver-notice`. A notice goes to a
device one time only. The notice stays open until the person acknowledges
it (`ack-notice`) on one device. A notice that a client said to the end
while the person used it is acknowledged. When a notice closes, the other
devices get its id in `closed` of `wait-notices` and remove it.

Notices that nobody acknowledges go to devices for 24 hours.

## 3. Who sends notices

- A persona, with the tool `send_notice`, when the person asks for it ("send
  it to my phone", "tell me when it is done") or when a long answer is
  better to read than to hear. The persona names the device with a word
  ("phone", "laptop", a device name, or "all"). The tool `list_devices`
  shows the devices and which are in use.
- Tasks. Each event of a code-change task is a notice from the persona that
  started the task:

  | Event | Urgency |
  | --- | --- |
  | Ready for review | normal |
  | Checks failed and the task stopped, merge failed, push failed, release failed | normal |
  | Merged, rejected, released, done, stopped by the person | low |

## 4. The engineering manager

The persona `persona:engineering` ("Engineering manager") runs the coding
tasks of the person. It starts tasks with complete instructions, follows
many tasks at the same time, gives the status in a few sentences, reads
the change of a task that waits for review, and gives a short summary.

Decisions:

- `revise` sends the task back to the apprentice with a comment. The
  persona can do this when the person asks for changes.
- `approve` (merge and release) and `reject` need the agreement of the
  person. Magi checks three conditions before it does the decision:
  1. The call has `confirm` true.
  2. An earlier turn of the persona (in the last 30 minutes, in one of the
     last four messages of the person) asked to confirm the same outcome
     for the same task. Magi reads this from the record of that turn, which
     only the Magi service writes. Thus the person had a message in which
     to say yes or no.
  3. The current turn did not read task output (`task_status`,
     `task_diff`) before the decision. Task output can contain
     instructions from an attacker (`docs/prompt-injection.md`).

## 5. Call a persona by name

A message that starts with the name of a persona and a comma, colon, or
exclamation mark goes to that persona, from any conversation: "Computer,
what time is it?" goes to Magi, "Engineering, what is the status?" goes to
the engineering manager. Magi and "computer" call `persona:magi`;
"engineering" and "engineering manager" call `persona:engineering`; the
name of any persona calls that persona. The reply of `say` has the
persona, and the clients then show that conversation. "computer science is
hard" calls nobody, because no comma follows the name.

## 6. Status

Done: devices and notices in the conversation service and the Magi API,
the tools `send_notice` and `list_devices`, task notices, the engineering
manager, and the web client (hello, notice loop, chime, tray, system
notifications), the Android client (system notifications from a
foreground service, notices said after a tone while the app is open), and
calling a persona by name. Not done: hands-free talk on Android while the
screen is off (Android gives silence to a microphone in the background; it
needs a foreground service of type microphone), and a wake word.
