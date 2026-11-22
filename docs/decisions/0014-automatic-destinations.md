# 0014 Magi permits known public destinations with no person

Status: accepted

## Decision

A task environment has no network access to a host that the policy of the
egress proxy does not list. An admin decided each new host. Now Magi
decides a new host itself when the answer is not in doubt:

1. Code permits a host in the list of public software sources, and a host
   in the list of the installation.
2. The review model permits a host only when it answers that it knows the
   host as a public source of software, documentation, or reference data,
   with high confidence.
3. Code refuses the automatic decision for an address, a private or
   reserved name, a punycode name, and a port that is not 80 or 443. The
   model cannot change this.
4. All other hosts go to an admin, as before.

The decision permits the destination only. Each request to it goes through
the same inspection as before: secret removal, the plugins, and the
request verdict. Each automatic decision has a record with the reason, an
admin sees it with the decided reviews, and an admin can replace it.

The setting is `auto` in the egress rules file. It is off when the file
does not have it.

## Reason

In the development installation, each open review was a destination, and
most were package indexes and toolchain hosts. A person adds no safety
when the person permits `pypi.org` each time. The reviews that need a
person were hidden in the list.

The risk of a wrong automatic decision is a read from a bad host, or a
small amount of data in the address of a read. A request that sends a
body still needs the request verdict. The model gets only the host name
and the task, so text from the environment cannot instruct it.

## Done when

A measurement on a list of hosts that includes hosts made to deceive
(names that look like known names, paste sites, request collectors,
tunnels) shows that the model permits none of them, and the development
installation runs code changes with no open review for a software source.
