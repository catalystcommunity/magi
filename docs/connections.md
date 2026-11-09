# Connections

This document tells how Magi acts for a person in other applications: the
calendar (catalystcal) and the shared projects and tasks (Longhouse). The
code is `magi/compose/connect.go` and `magi/compose/work.go`. The identity
primitive keeps the grants and the credentials.

## Calendar: act-as grant

catalystcal accepts LinkKeys act-as grants. Magi is the grantee and
catalystcal is the audience.

1. The person says `magi connect calendar`, or a persona calls
   `connect_account`.
2. Magi gets a signed scope set from catalystcal (`calendars.read`,
   `events.write`, `events.respond`). The identity primitive checks it
   and signs a grant request with Magi's application key.
3. The person opens the link, signs in at their home domain, and approves.
4. The home domain sends the browser to `/login/grant/callback`. The
   identity primitive gets the grant through the RP and stores it.
5. For a calendar call, Magi asks identity for a presentation
   (`present-grant`). Identity checks the grant at the home domain first.
   Magi exchanges the presentation for a short catalystcal bearer and
   keeps the bearer until one minute before it expires.

A revoked grant stops at the next bearer. catalystcal also checks
revocations itself. The person revokes the grant on their LinkKeys account
page. `magi disconnect calendar` deletes only Magi's copy.

Grant terms: Magi asks for a lifetime of 24 hours and a renewal window of
30 days (the LinkKeys maximums). Without these terms, a grant lives one hour
and cannot renew. The identity primitive renews each active grant every 15
minutes when less than half of its lifetime is left. The renewal window
starts at the first expiry, so a grant ends about 31 days after the person
approves it. Three days before the end, Magi sends the owner a notice
("Connect your calendar again") once a day (`expiring-grants`). A newer
grant for the same application stops the notices.

An event that Magi puts on the calendar of another person needs a share
with `write` (or `contribute`) access to the person whom Magi acts for.

## Longhouse: CLI session

Longhouse has no act-as grants. The person approves a Longhouse CLI
session for the client "Magi":

1. `magi connect longhouse` gives a link and a code.
2. The person signs in to Longhouse and approves the code.
3. Magi gets the session and stores it in identity, encrypted.

Anyone who has the code can approve it. Magi asks Longhouse for the account
of the new session. When it is not the account of the person who started
the connection, Magi revokes the session and does not store it.
`magi disconnect longhouse` ends the session at Longhouse and deletes it.

Longhouse revokes a session when a used refresh token comes back. Thus
Magi stores the bearer next to the refresh token. Only one Magi replica
refreshes: it takes a 30-second lease with a revision check
(`expected_revision`). The other replicas wait and read the new bearer.

## Persona tools

| Tool | Does |
| --- | --- |
| `connect_account` | Gives the person a link to connect a system |
| `calendar_events` | The occurrences in a range, on the person's calendars and the shared ones. Optional words and calendar |
| `find_events` | Search all events by words |
| `create_event` | One event or a repeating event (`repeat`: words or an RRULE; `until`). Uses `create-event-once`: the same message makes one event |
| `change_event`, `cancel_event` | One occurrence, this and later ones, or all |
| `free_busy` | Busy times of the person and of the owners of shared calendars |
| `longhouse_projects`, `longhouse_tasks` | Projects and tasks of a house, with status, due date, assignees, and projects |
| `add_longhouse_task`, `update_longhouse_task`, `add_longhouse_project` | Changes, and comments on tasks |

Times are local times of the household (`MAGI_TIME_ZONE`). A date alone as
the end of a range includes that day.

Beatrice (`persona:assistant`) and Magi have all the tools. Archimedes
(`persona:engineering`) reads the calendar and uses Longhouse for the state
of software work.

## Network

With the egress proxy, all calls go through it as `svc_magi`. The policy
must have the two hosts as destinations for the environment `svc_magi`.
