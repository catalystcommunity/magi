# Placement: record 0016, and a host reports its GPUs

Magi must place the models on the inference hosts. Now each inference host has one engine with one model that a file on that machine names, and the registry has one entry for each model on each host. Magi can select between these entries. It cannot load a model, and it does not know the free GPU memory of a host. Do the first step of the change: write the decision record, and make an inference host report its GPUs.

Files: docs/decisions/, contracts/inference.csil, primitives/inference/host/ (host.go, load.go, proc_unix.go, proc_windows.go, the tests), primitives/inference/README.md. Read docs/conventions.md before you change the primitive.

1. Decision record. Add docs/decisions/0016-placement.md with the same parts as the other records (ASD-STE100). The title is "0016 Magi places the models on the hosts". The status is accepted.
   The decision:
   - A model is not a property of a host. Magi decides which host runs which model, and it changes that when a host leaves or when a different layout is better.
   - A model in the registry has no host in its name. Its record has the model file (an artifact with a hash), the arguments of the engine, the GPU memory that it needs, its adapters, and its measured scores.
   - A host reports what it has: its GPUs with the total and the free memory, the model files on its disk, the models that are loaded, and if its owner uses the machine.
   - A placement is a decision of Magi: this model runs on this host now. Magi tells the host to load or to unload a model. A host that does not have the file gets it from the artifacts of the installation and checks the hash.
   - A request names a role or a model, not a host. Magi sends it to a host that has the model loaded. When no host has it, Magi loads it on a host with room, or it uses the next model of the role.
   - The rules of the owner of a machine come first. A host that is busy with the work of its owner refuses a load and can drop a model. Magi then places the model on a different host.
   - A host that leaves is an incident (record 0015). The repair is a new placement, and code does it.
   - Code decides a placement, from the roles in the order of their importance, the memory that each model needs, the free memory of each host, and the measured speed. A model does not decide it.
   - A decision must be fast. A model for decisions runs on a GPU. Magi does not serve a decision from a CPU when that takes seconds.

   The reason: the state that the first paragraph of this task tells. The result of that state: a better decision model (the scoring head on Qwen3.5-4B, evals/egress/README.md) had no place, because the one host of the decision models has no free GPU memory, and other hosts have GPUs. An orchestrator that cannot use them is only a list of endpoints.

   "Done when" has these six items. Mark only the first item as complete ("- [x]"); each other item is "- [ ]".
   - A host reports its GPUs with the total and the used memory, the memory of its engine, and the model names that it serves (`status` with `detail`).
   - A host reports the model files on its disk. An admin can read the reports of all hosts.
   - A host loads and unloads a model when Magi tells it to, and it can hold more than one engine when the memory permits. It gets a file that it does not have from the artifacts.
   - The registry has models with no host in the name, and the routes of the roles use them.
   - Magi makes the placement from the roles and the reports of the hosts, and makes a new one when a host leaves.
   - The scoring head for the host question runs on a GPU that Magi selected, with the adapter as the fallback.

2. docs/decisions/README.md: add the record to the table of records (decision "Magi places the models on the hosts", status accepted). Add this row to the table "Who decides", directly after the row "Which model serves a role?":
   | Which host runs a model? | Code, from the roles, the memory that a model needs, and the reports of the hosts. The rules of the owner of a machine come first | record 0016 |

3. Contract. In contracts/inference.csil, add the type Gpu and three optional fields to Status: gpus, engine_mib, models.

   Gpu = { name: text, ? total_mib: uint, ? used_mib: uint, ? util: uint }
   Status: ? gpus: [* Gpu], ? engine_mib: uint, ? models: [* text]

   Write a `;;;` comment that tells what the type and the fields are. Then run tools/gen.sh: it makes the Go code of primitives/inference/api and the Python and TypeScript clients. Do not edit generated files.

4. The host (primitives/inference/host). When the request of `status` has `detail` true, the status also has:
   - `gpus`: each GPU of the machine with its name, `total_mib`, `used_mib` (the memory that all processes use, also the engine), and `util` (percent). A value that the host cannot read is absent.
   - `engine_mib`: the GPU memory that the engine process uses. `engine_mib` is absent when the engine does not run.
   - `models`: the model names that the engine serves: the model of the engine first, then the names of `engine.models` in alphabetical order.

   Without `detail`, the status has none of the three fields.

5. NVIDIA GPUs come from nvidia-smi. The sampler reads the first GPU only now; read all GPUs, with their names. nvidia-smi prints `[N/A]` for a value that it cannot read. The busy rules do not change: they use the first GPU of nvidia-smi only.

6. On Linux, AMD and Intel GPUs come from /sys/class/drm. A card is a directory `card<N>`. A directory such as `card0-DP-1` is an output of a card, not a card. The files of a card are in `card<N>/device/`: `vendor` (`0x1002` is AMD, `0x8086` is Intel), `device` (the device id, for example `0x7480`), `mem_info_vram_total` and `mem_info_vram_used`, and `gpu_busy_percent`. The name of such a GPU is the maker and the device id without `0x`, for example "AMD 7480". The memory files have bytes; the status has MiB. A card with no file for a value has no value (most Intel cards have no memory files). Do not report cards of NVIDIA (vendor `0x10de`) from this directory, because nvidia-smi reports them, and do not report cards of other makers. On Windows, only nvidia-smi reports GPUs.

7. Tests in primitives/inference/host: the status with detail and without detail, through the real HTTP transport with the generated client; the cards of a /sys/class/drm tree that the test makes in a temporary directory.

8. primitives/inference/README.md (ASD-STE100): a section "Report for a placement" after the states: what `status` with `detail` adds, and where the GPU values come from (nvidia-smi, /sys/class/drm).

Verify: cd primitives/inference && GOWORK=off go test -count=1 ./... Then ./tools/check.sh.
