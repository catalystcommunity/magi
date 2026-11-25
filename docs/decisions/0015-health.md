# 0015 Magi keeps itself healthy

Status: accepted

## Decision

Magi finds problems of its own installation and repairs the simple ones.
A problem that Magi finds is an incident: a durable record with a number,
a kind, a subject (for example `database/magi-db-1` or
`service/conversation`), and one sentence. All incidents are in one list.

There are three levels of repair. Each level acts only when the level
before it did not repair the problem.

1. The platform. Kubernetes starts a container again. The operator of the
   database promotes the standby. Magi does nothing, and it records what
   it sees (`failover`).
2. Code rules. The watch of the release controller applies a fixed list of
   rules. Each rule has an action that loses no data.
   - A database instance is not ready for 90 seconds, or a service is not
     ready for 3 minutes: the watch deletes the pods that are not ready,
     one time (`restarted`). The platform makes them again with the same
     data.
   - The watch does not act on a service while the database has no
     primary that is ready, or while a release runs: the cause is there.
   - The part is still not ready 10 minutes after the restart: the watch
     records `unhealthy` and acts no more on that part until it was ready
     again.
3. A model. For an `unhealthy` incident, Magi reads the facts: the state
   of the pods, the last lines of the log, the events of the cluster, and
   the last releases. A model names the cause and selects one action:
   `restart` (one more time), `rollback` (the images before the last
   release), or `person`. Magi does the action. Code permits it:
   - A rollback is possible only for the newest release, when it
     succeeded, changed this part, and ended in the 2 hours before the
     incident.
   - Magi acts on itself no more than 3 times in one hour.
   - Magi looks at one incident one time.
   - The facts are data in a fence. The answer of the model is one of
     three words, so text in a log cannot make another action.

Level 3 is work of the Magi system. It is not a turn of a persona: a
persona is for a person.

The watch is in the release controller. That controller has the access to
the cluster, and it keeps its records in a file. Thus levels 1 and 2 work
while the database, the queue, or Magi itself does not.

Magi sends each incident to the admins as a notice, with the cause and
the action of level 3. A notice has a key, so an incident gives one
notice.

A person decides everything else: data that can be lost (a volume, a
restore from a backup), a change of the size of the installation, and
each problem that the model cannot name.

The owner decided that the model acts from the start, before an eval of
health cases exists (record 0009 asks for an eval before a model
decides). The reason: the two actions lose no data, and code limits them.
The eval is still necessary to know how often the model is correct.

## Reason

On 25 November 2026 the volume of the one database instance got an
input/output error. The pod started 25 times in 110 minutes, and all of
Magi was down until a person deleted the pod. The repair was one command
with no risk. A system that needs a person for that command is not
available when the person sleeps.

A model is not the first tool for this. A rule that is always correct is
faster and works while the model does not. A model is for a problem that
needs reading: a log with an error that no rule names.

A test in the development installation showed one more fact. A primary
with processes that do not answer keeps the default time to stop of 30
minutes, and the operator does not promote the standby before the old
primary is gone. Thus the watch deletes a pod with 30 seconds to stop.

## Done when

- [x] The database has two instances, and the watch records a failover.
- [x] The watch restarts a database instance or a service that stays not
      ready.
- [x] Magi sends each incident to the admins.
- [x] For an `unhealthy` incident, a model names the cause and Magi does
      the action that the code rules permit.
- [ ] An eval of health cases measures the action that the model selects.
- [ ] Magi records an incident when one of its own calls to a service
      fails for some minutes (`/healthz/deps`), and when a queue or a
      worker stops.
- [ ] A Deployment with no pod is an incident (the watch sees pods only).
