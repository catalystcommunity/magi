# 0016 Magi places the models on the hosts

Status: accepted

## Decision

A model is not a property of a host. Magi decides which host runs which
model, and it changes that when a host leaves or when a different layout
is better.

- A model in the registry has no host in its name. Its record has the
  model file (an artifact with a hash), the arguments of the engine, the
  GPU memory that it needs, its adapters, and its measured scores.
- A host reports what it has: its GPUs with the total and the free memory,
  the model files on its disk, the models that are loaded, and if its
  owner uses the machine.
- A placement is a decision of Magi: this model runs on this host now.
  Magi tells the host to load or to unload a model. A host that does not
  have the file gets it from the artifacts of the installation and checks
  the hash.
- A request names a role or a model, not a host. Magi sends it to a host
  that has the model loaded. When no host has it, Magi loads it on a host
  with room, or it uses the next model of the role.
- The rules of the owner of a machine come first. A host that is busy with
  the work of its owner refuses a load and can drop a model. Magi then
  places the model on a different host.
- A host that leaves is an incident (record 0015). The repair is a new
  placement, and code does it.
- Code decides a placement: the roles in the order of their importance,
  the memory that each model needs, the free memory of each host, and the
  measured speed. A model does not decide it.

A decision must be fast. A model for decisions runs on a GPU. Magi does
not serve a decision from a CPU when that takes seconds.

## Reason

Each inference host has one engine with one model that a file on that
machine names, and the registry has one entry for each model on each host
(`qwen3.6-35b-a3b-vr1`, `gemma-4-12b-studiodt`). Magi can select between
these entries. It cannot load a model, and it does not know the free
memory of a host.

The result: a better decision model (the scoring head on Qwen3.5-4B,
`evals/egress/README.md`) had no place, because the one host of the
decision models has no free GPU memory. Other hosts have GPUs. An
orchestrator that cannot use them is only a list of endpoints, and a
person can make such a list with no Magi.

## Done when

- [x] A host reports its GPUs with the total and the used memory, the
      memory of its engine, and the model names that it serves (`status`
      with `detail`).
- [ ] A host reports the model files on its disk. An admin can read the
      reports of all hosts.
- [ ] A host loads and unloads a model when Magi tells it to, and it can
      hold more than one engine when the memory permits. It gets a file
      that it does not have from the artifacts.
- [ ] The registry has models with no host in the name, and the routes of
      the roles use them.
- [ ] Magi makes the placement from the roles and the reports of the
      hosts, and makes a new one when a host leaves.
- [ ] The scoring head for the host question runs on a GPU that Magi
      selected, with the adapter as the fallback.
