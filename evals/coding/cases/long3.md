# Inference host: more than one engine, and a load from Magi

A GPU that holds one large model can serve a second model, but not at the same time. Now an inference host has one engine, and a person changes the model in the configuration file of that machine. Make a host hold more than one engine, run one of them at a time, and change the engine when Magi tells it to (decision record 0016: "A host loads and unloads a model when Magi tells it to").

Files: contracts/inference.csil, primitives/inference/host/ (host.go, variants.go, decide.go, the tests), primitives/inference/cmd/magi-inference/main.go, primitives/inference/README.md. Read docs/conventions.md before you change the primitive.

1. Configuration. The configuration file gets `engines`: a list of more engines, after `engine`. Each entry has the same fields as `engine` (`command`, `port`, `model`, `description`, `decide`, `models`). `engine` is the first engine of the host. A configuration with no `engines` works as before.
   With more than one engine, each engine needs `command`, `port`, and `model`, and each model name (the `model` of an engine and each name of its `models`) must be in one engine only. The host program does not start with a configuration that breaks this rule: it ends with an error that tells the rule.

2. One engine at a time. The host runs its first engine until a load (item 4). It never runs two engines at the same time: an engine starts only after the process of the engine before it ended, because that process holds the GPU memory until then. The rules for a busy machine, `keep_warm`, `idle_stop`, and `start_wait` apply to the engine that the host runs.

3. Chat requests (`/v1/`). A request finds its engine by the `model` field of its JSON body: the `model` of an engine, or a name of its `models`.
   - A request for a model of the engine that the host runs goes to that engine, as now.
   - A request for a model of a different engine is refused: status 503, the JSON error type `not_loaded`, and a `Retry-After` header. A chat request never changes the engine, and a refused request does not start an engine.
   - On a host with more than one engine, a model name that no engine has gets the same refusal (503, `not_loaded`).
   - A host with one engine sends each model name to that engine, as now.
   - `GET /v1/models` lists the model names of the engine that the host runs.

4. Contract. Add to contracts/inference.csil, then run tools/gen.sh (do not edit generated files):

   LoadRequest = { model: text, ? hold_seconds: uint }
   service InferenceService: load: LoadRequest -> Status
   Status: ? loadable: [* text], ? hold_until_ms: uint

   Write `;;;` comments that tell what the operation and the fields do.

5. `InferenceService/load` (scope `inference:use`, as the other operations).
   - `load` makes the engine that serves `model` the engine of the host. `model` is the `model` of an engine or a name of its `models`. A model that no engine has is "not found" (status 404).
   - Requests in flight get their answers from the old engine. The old engine stops when no request uses it. Then the new engine starts.
   - From the load on, a new request for a model of the old engine is refused (`not_loaded`).
   - `load` does not wait for the new engine. Its answer is the status with detail. The state of the status is `starting` from the load until the new engine is ready, also while the old engine still answers a request. `model` of the status is the `model` of the engine that the host runs or starts next.
   - A load on a busy machine starts no engine: the state stays `busy`.

6. The hold. The host keeps an engine that is not its first engine for `hold_seconds` after its last request (the start and the end of a request each count). The default is 900 seconds, and the maximum is 14400: a larger value counts as 14400. When the hold ended and no request is in flight, the host goes back to its first engine: with `keep_warm` the first engine starts at once, else it starts with the next request. A load of a model of the first engine ends the hold at once. Thus a caller that stops does not leave the host on the wrong model.

7. Status with `detail`. `models` has the model names of the engine that the host runs or starts next. On a host with more than one engine, `loadable` has the `model` of each engine, in the sequence of the configuration; on a host with one engine it is absent. `hold_until_ms` is the time when the hold ends, in milliseconds since 1970; it is absent while the host runs its first engine.

8. `InferenceService/decide` answers with the engine that the host runs. A `model` of a different engine is refused in the same way as a chat request for it (the error has `not_loaded`).

9. Tests in primitives/inference/host, with two fake engines: a load changes the engine (through the real HTTP transport with the generated client), the refusals before and after a load, a load while a request is in flight, the end of a hold with and without `keep_warm`, a model that no engine has, and the configuration rule.

10. primitives/inference/README.md (ASD-STE100): the operation in the API table, the error type `not_loaded` in the table of refusals, `loadable` and `hold_until_ms` in "Report for a placement", and a section "More than one engine" with a configuration example and the rules of items 2 to 6.

Verify: cd primitives/inference && GOWORK=off go test -count=1 -race ./... Then ./tools/check.sh.
