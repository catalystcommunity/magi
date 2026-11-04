// Package hostsetup makes a host into a Magi model host (docs/host-setup.md).
// Deterministic checks (Gather) read the facts; NewPlan removes the pinned
// engines and models that do not fit; Run is the loop of the setup model,
// with tools that act on an Env: Local (the real host) or a scripted host
// in the live check (TestLiveHostSetup).
package hostsetup
