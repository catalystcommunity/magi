# Setup

This guide tells you how to set up a new Magi installation, from zero. It
gives the order of the steps and refers to the documents that have the
details. It contains no private values. Keep your values in a values file
and in credential files outside the repository.

Keep this guide current. A change that adds a host, a value, a Secret, a
credential, or a setup step must also change this guide.

## Parts

| Part | Where | Needed | Details |
| --- | --- | --- | --- |
| Primitives, `magi`, apprentice worker | Kubernetes namespace (for example `magi`) | yes | `deploy/README.md` |
| Execution environments | second namespace (for example `magi-envs`) | yes | `deploy/README.md` |
| Model provider | any OpenAI-compatible Chat Completions endpoint | yes | `deploy/README.md`, "Values file" |
| Sign-in | a LinkKeys relying party | yes | `primitives/identity/README.md` |
| Runners | Linux or macOS machines that connect out to Magi | for code changes | `magi/README.md`, "Runners" |
| Local model host | a Linux machine with a GPU (`primitives/inference`) | no | `primitives/inference/README.md`, `docs/host-setup.md` |
| Speech engine host | a Linux machine (a GPU helps TTS) | for voice | `primitives/speech/README.md` |
| Egress proxy | the Magi namespace | no | `docs/egress-proxy.md`, `deploy/README.md` |
| Clients | web (served by `magi`), command line, Android | | `magi/README.md`, `android/README.md` |

Magi does not include third-party engines or models: llama.cpp, Parakeet,
Kokoro, Silero VAD, and the injection detector come from their projects.
Magi includes the programs that install, check, start, and use them, and
the pins (versions and SHA-256 hashes) of each file. The turn classifier is
Magi's own model; the speech host trains it from the code in
`primitives/speech/engine/turn/`.

## Requirements

- A Kubernetes cluster with:
  - the CloudNativePG operator (PostgreSQL);
  - a Gateway API implementation and a Gateway with an HTTP listener (for
    example Contour), for the public route;
  - a storage class for ReadWriteOnce volumes (the default class, or
    `STORAGE_CLASS`);
  - kube-proxy in iptables mode with localhost NodePorts (the image
    registry, `deploy/README.md`).
- A public host name with HTTPS in front of the Gateway (`PUBLIC_HOST`).
  Machines of the people connect out to it. Magi does not connect to them.
- A build machine with Go, Docker (or BuildKit), Python 3, and `kubectl`.
  `source "$HOME/.local/catalyst-tools/env.sh"` gives the other toolchains
  (Gradle and the JDK for Android).
- An API key of the model provider.

## Steps

Do the steps in this order. Each step refers to the section with the
details.

1. **Credentials.** Make the issuer key and the service tokens with the
   `identity` command (`deploy/README.md`, "Credentials"). Make the LinkKeys
   relying-party runtime file (`primitives/identity/README.md`,
   "Configuration"). Create the Secrets of the table in "Credentials".
2. **Values file.** Copy the values from `deploy/README.md`, "Values
   file", into a private file and set them. `ADMINS` is the subject of the
   first operator. You can set it after the first sign in (step 6).
3. **Image registry, database, images.** Make the registry credentials
   (`deploy/README.md`, "Image registry"). Then:

   ```sh
   python3 deploy/render.py --base --values VALUES.env | kubectl apply -f -
   tools/build-web.sh
   tools/build-images.sh
   kubectl apply -f primitives/execution/deploy/rbac.yaml
   python3 deploy/render.py --images .build/images.env --values VALUES.env > out.json
   kubectl apply -f out.json
   ```

   Each service answers `GET /healthz` (`README.md`, "Health endpoints").
4. **Source repository.** Import the repository that code changes use:
   `tools/seed-repo.py --source URL --token-file TOKEN` (`deploy/README.md`,
   "Source repository").
5. **Command-line client.** `go build -o magi ./magi/cmd/magi`, then
   `magi --server https://PUBLIC_HOST login you@your.domain`.
6. **First operator.** `magi whoami` shows your subject. Put it in
   `ADMINS` (and `LEXICON_EDITORS` if needed), render, and apply again.
7. **Speech engine host** (for voice). From the checkout:

   ```sh
   primitives/speech/engine/install.sh --provision user@speech.example.local
   ```

   This makes the Python environments, gets the models, trains the turn
   classifier, and starts the engines (`primitives/speech/README.md`,
   "Deploy the engines"). Put the engine token in the Secret
   `magi-speech-engine`, and set `SPEECH_STT_URL`, `SPEECH_TTS_URL`,
   `SPEECH_TURN_URL`, and `DETECTOR_URL` (ports 8092, 8093, 8095, 8094).
8. **Local model host** (optional). Make the host with `magi-host-setup`
   (`docs/host-setup.md`) or write the inference configuration by hand
   (`primitives/inference/README.md`). Run it as a user service. Mint a
   token with the scope `inference:use` into the Secret
   `magi-inference-token`, and set `PERSONA_LOCAL_URL` and
   `PERSONA_LOCAL_MODEL`.
9. **Runners** (for code changes). On each runner machine: install the
   toolchains, mint a runner token, and install the runner as a service
   (`magi/README.md`, "Install a runner"). For task VMs on Linux, build and
   install the kernel with `tools/vm/build-kernel.sh --install USER@HOST`
   (`magi/README.md`, "Task VMs"). Set `RUNNER`, and `PUBLISHER` for the
   runner that pushes to the upstream.
10. **Egress proxy** (optional). Make the proxy CA, the environment key,
    the policy, and the rules, then set `EGRESS=true`
    (`deploy/README.md`, "Egress proxy").
11. **Android** (optional). Build the app, publish the build, and install
    the first build over USB (`android/README.md`, "Updates"). Later builds
    install by themselves.
12. **Check.** Open `https://PUBLIC_HOST` and sign in. Say or type
    "Computer, hello". `magi runners` lists the runners. Run
    `tools/check.sh` in the checkout before you change the code.

## Update an installation

- Magi releases its own `magi` Deployment after a merged code change. For a
  change to a primitive, build the images and apply as in step 3.
- Run `install.sh --provision` again after a change to the engine lock
  files or to `model-pins.json`. It changes only what is different.
- Runners update by themselves (`magi/README.md`, "Runner updates").
