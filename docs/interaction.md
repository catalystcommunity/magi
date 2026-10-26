# Human interaction

Updated 2026-10-16. Status: accepted interaction requirements with proposed
client and device mechanisms. Android is the first mobile client. It offers
regular application and optional default-assistant modes. See the
[integrated release design](integrated-release.md) for platform restrictions,
delivery defaults, and connected acceptance tests. Adaptive learning details
remain deferred.

The [demo prototype design](demo-prototype.md) adds Parakeet, replaceable TTS
engines with eight voices, speaker attribution, and sensitive-operation
authentication. Its TTS comparison and detailed contracts remain open.

## Conversation graph

The conversation graph is the primary interaction model. Its records include
source interactions, presentations, speaker estimates, interpretations, and
actual session inputs. Typed relationships express replies, references,
corrections, derivations, and delivery. There is no required conversation mode.

Segments can select overlapping nodes or source ranges. Optional analysis can
describe a segment as planning, discussion, or another mode. Such labels can
overlap and change with later evidence. They do not partition the conversation,
select permissions, or replace the original records.

Model context is a permitted projection of selected graph records and external
evidence. Knowledge derives from this graph and from searches, documents, and
other sources. Authorship and scoped acceptance still apply. Graph relationships
do not grant access to hidden source content or metadata.

## Sessions and identities

Authenticate the person separately from the client device and application.
An authenticated device can carry observations without proving which person
spoke. A household session can include more than one identity. Select context
and response audience explicitly; do not silently combine private memories.

Speaker estimation supports attribution and cross-talk handling. Keep it separate
from authentication evidence. Simple requests need no new MFA challenge when
resource policy permits them. Sensitive operations can emit an authentication
or exact-approval request to the user. LinkKeys provides authentication at the
required supported level; the resource service still decides authorization.
Record request binding, freshness, expiry, refusal, and timeout. Do not lower
the required level when an authentication method is unavailable.

A persona supplies voice, personality, and focus across interfaces. It has a
stable LinkKeys identity and can serve several users. Use its privacy defaults
for each represented user; a shared persona is not a shared private context.

A conversation can contain several task sessions. A new task gets a separate
session. Existing sessions can remain active while attention moves between them.
Child work remains within its task session's privacy boundary.
Do not use one visible chat transcript as automatic context for every task.
An external harness that combines tasks retains the resulting combined scope.
See [Personas sessions and trust](personas-sessions-and-trust.md).

Clients attach to conversations and executions through the API. The API owns
permissions, cancellation, and result state. A UI can display an approval, but
the receiving service validates the authenticated approval and exact operation.

Environment attachment is live. Pause, resume, and command cancellation are
separate requested operations on every client. To inform a worker, the user
can message the initiating persona. It selects useful additional context.
Do not substitute automatic file-watcher messages for that interaction.

## Concurrent channels and reply routing

Magi coordinates interaction points, conversations, sessions, and evaluations
in real time. A persona does not have one global active conversation. A channel
does not own the task context. Waiting for one answer must not stop unrelated work.

Voice is a shared half-duplex interaction channel for a participating user.
Text can carry concurrent exchanges. A voice prompt can receive a typed answer.
A typed reply can instead advance a different conversation. Neither input
modality nor the last spoken question is sufficient to select the destination.

Use explicit context links where available and interpret the reply with pending
questions and current context. One reply can address several prompts. Decide
whether to split it, share its meaning, or clarify an unresolved reference.
Keep the source interaction, interpretation, and actual session inputs linked.
Do not copy unrelated private material into each destination.

For a shower interaction, three task sessions can run while Magi presents one
question and its answer at a time. A reply can answer an earlier question after
the next topic was introduced. Route the reply to its intended context and
present the new topic cue again when needed. Preserve the original sequence.

At a desk, Janaya can ask for an address aloud while the user handles another
text thread. A pasted address can satisfy the spoken prompt. An unrelated text
answer need not satisfy it or take over voice output. User settings and current
context determine whether the next result uses text, queued speech, or later
delivery. Do not fix one universal channel-switch rule.

"Tuesday works for both" can advance two sessions. Each needs enough permitted
context to understand the shared answer. The two delivered inputs retain one
common source reference and separate destination records. A later correction
adds evidence; it does not erase the earlier routing or undo completed effects.

Record available presentation state: pending prompt, queued output, presentation,
interruption, reply association, and completion or uncertainty. Client playback
reports do not prove attention. Keep interpretation and observed events distinct.
The detailed state machine remains open.

Settings and learned modes can depend on identity, interaction point, situation,
or a combination. Shower, phone, desk, and car use can supply different context
when known and permitted. Record supporting and contrary evidence for a mode.
An inferred mode is not an explicit setting or permission to expand access.
Learning and review follow the user's chosen authority rules. Defer the detailed
adaptation algorithm; support additional assessment metadata later.

See [Knowledge context and reinterpretation](knowledge-and-context.md) for
source-specific storage, assessment layers, and query examples.

## Historical recall and failure

Find an earlier discussion from approximate topic and time, including a segment
inside an interleaved exchange. Recover its relevant context under current
access policy. Do not require the user to supply an agent or session ID.

Target a useful response within a few seconds. If retrieval takes longer,
acknowledge promptly and aim to recover the context in about twenty seconds.
These targets need workload definitions and measurement. A known outage needs
an explanation, not a false progress message.

Distinguish no match, incomplete search, and unavailable service where disclosure
is permitted. An active request at 03:00 can expose a blocking failure even if
unsolicited alerts are deferred. Explain the alert's actual state from evidence;
do not assume quiet hours caused the silence. Keep diagnosis possible without
requiring the failed conversation store. Repairs need separate authority.

## Personal inbox and notifications

A personal inbox belongs to an identity at an authority selected by that
identity. Each item refers to an admitted source event. Endpoint records bind
an identity, device installation, application, platform, and provider address.
Provider addresses are confidential routing data, not identities.

Store notification rules as typed records with optional bounded expressions.

| Rule input | Example |
| --- | --- |
| Verified sender or group | Family group, trusted monitoring service |
| Event kind and resource | Direct message, project completion |
| Urgency permission | Which sender may request immediate interruption |
| Local schedule | Business days, local 08:00, IANA timezone, holiday calendar |
| Presence | Active desktop, phone availability, occupied room |
| Content sensitivity | Private message unsuitable for a room speaker |
| Expiry and replacement | Replace an obsolete reminder with current state |
| Receipt requirement | Seen, explicit acknowledgement, or no acknowledgement |

Resolve rules when the event arrives and again when delivery becomes due.
Record the decision, policy revision, and next evaluation time. A claimed
emergency does not bypass policy unless the sender has that permission.

Example: an ordinary work message received Friday evening is scheduled for
08:00 on the next permitted business day. At that time, recheck the calendar,
rule, and access. Send to the active desktop and phone if the rule selects both.
Reading it on the desktop can cancel an unplayed voice announcement under the
recipient's rule. This does not rewrite the original event.

Distinguish provider acceptance, device receipt, display, seen, and explicit
acknowledgement. Track these per endpoint and per inbox item. Do not report a
person as informed solely because a push provider accepted a request.

## Delivery providers

Proposed first adapters: direct APNs, FCM, Web Push, and an existing live-client
stream. Email, SMS, and telephony are separate channel adapters. Evaluate a
managed push provider if operating the direct adapters is not suitable.

Register provider tokens through an authenticated device session. Handle token
rotation, invalid-token responses, logout, device loss, and reassignment. Keep
test and production provider applications separate. FCM documents token
refresh and stale-token removal; use those rules in the adapter.
[FCM token management](https://firebase.google.com/docs/cloud-messaging/manage-tokens)

The provider receives only the payload permitted for that channel. An opaque
inbox reference can prompt an authenticated fetch. A minimal preview is an
explicit alternative when offline display matters. Provider TTL does not
control retention of the authoritative inbox item.

ntfy is excluded from the baseline. Twilio and Telnyx remain communications
provider candidates, not selected general push implementations. Do not depend
on a deprecated product or assume a call-push API accepts arbitrary messages.

## Voice pipeline

```text
microphone -> shared local audio buffer -> wake-word detector
                                      -> speech boundary detector
                                      -> selected STT backend
transcript -> interaction input -> one or more task sessions
session outputs -> presentation queue -> selected TTS -> output device
```

Share capture through the platform audio service where supported. Keep a
bounded local pre-roll buffer. Define microphone permission, background capture,
and suspension behavior separately for each operating system. Do not claim that
a browser experiment establishes always-on mobile support.

Wake-word detection, speech boundaries, speaker matching, and transcription
are separate capabilities. They may share a process and audio features. A
personal wake-word model can be enrolled with explicit consent and replaced
without changing the STT service.

Parakeet is the selected first STT family. Compare replaceable TTS engines with
at least eight distinct voice profiles. CUDA can be a backend requirement without
coupling other Magi services to it. STT and TTS can run as separate services on
shared or different hosts. Personal TTS cloning is excluded.
Preserve language, voice revision, sample format, latency, and cancellation.
Person-and-device speaker adaptation is a design goal, not a prototype guarantee.

The existing lab-speech-engine provides experimental capture, STT/TTS interfaces,
streamed transcripts, and speaker filtering. Its streaming path depends on
native Whisper. Its API-key-based speaker profile is not a LinkKeys human
authentication path. Model results need explicit partial and final semantics;
an offline STT model must not be presented as native streaming.
See [Evidence](evidence.md) for source locations and the prototype review for
the new boundaries.

Use one admitted engine for live STT. Later, process retained audio with two or
three local engines. Keep their transcripts, alignment, disagreements, and the
preferred interpretation. Consensus is not ground truth or speaker authentication.
Expire raw audio under a bounded policy after derivative storage and retention
checks. Preserve the transcript and context that actually caused an operation.

If a speech backend fails, try a compatible admitted alternative. Otherwise send
a permitted text notice with the observed failure and known cause. Investigation
can involve a human. Do not invent a root cause or require the failed speech
service to announce its own failure. Output voice changes require a configured
fallback mapping. Repeated requests must not replay already presented speech.

## Multiple devices

Each microphone reports a bounded observation with device identity, local
sequence, time estimate, room when known, and confidence. The conversation
coordinator correlates observations and assigns one conversation lease.
Use timing and audio evidence; do not combine every wake event in one interval.
Two people can start different conversations at the same time. The lease
prevents competing presentation of the same interaction; it does not limit a
person to one active task session or prevent concurrent text interaction.

Select output independently of the input microphone. In the kitchen example,
request playback from the phone and require readiness. If it cannot play,
apply the user's fallback rule and request an Ichoi interruption.

An interruption record identifies the conversation, player, prior playback
revision, and owner. Resume only if the interruption still owns the pause and
the player has not been changed by a person. Support speech interruption and
echo cancellation so Magi does not answer its own output.

After a coordinator restart, expire or reconcile conversation leases. Under a
network partition, devices must follow a declared local fallback or remain
silent. Global single-response behavior cannot be promised without coordination.

Home Assistant is excluded. Wyoming remains an optional interoperability
adapter when someone requests it.

## Calendars

Calendar extraction from Longhouse is a separate work package. First define:

- Availability rules and privacy-preserving availability queries.
- Proposals with several candidate times and participants.
- Temporary reservations with expiry and conflict checks.
- Confirmed commitments, resources, and attendance.
- Recurrence, local time, timezone changes, and per-occurrence exceptions.
- Delegated actions, audit, invitations, updates, and cancellation.

Use CSIL as the native contract. Existing calendar standards are adapters.
Preserve unsupported fields when safe, or report explicit conversion loss.
Do not select Radicale as the native authority without the calendar review.

Querying availability does not reserve a time. A reservation or booking must
check conflicts at the authority when committed. Scope shared availability by
date range, detail level, purpose, and duration under local policy.

## Next Actions

Build the personal inbox and authenticated endpoint registration first. Use
text clients and simulated voice events to test cross-channel routing and
multi-session responses. Test recall and unavailable services before claiming
latency targets. Add real voice devices without changing the context boundaries.
