# 0002 Decisions are records in this directory

Status: done

## Decision

A decision that limits later work is a record in `docs/decisions/`. The
index and the table of who decides are in `README.md` of this directory.
A change that goes against a record changes the record in the same
commit.

## Reason

A persona that changes Magi reads the repository, not the conversations
in which the decisions were made. A decision that is not in the
repository is not known to it.

## Done when

The directory has the index, the table, and one record for each decision
that is in force.
