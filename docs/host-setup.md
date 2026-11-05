# Host setup tool

Status (2026-10-31): the model host role is built (`magi/cmd/magi-host-setup`,
package `magi/hostsetup`). The speech host role is only in the live check;
to make a speech host, use `primitives/speech/engine/install.sh --provision`
(deterministic, no setup model).

Magi does not ship images for models or speech engines. Magi gives a tool
that makes a host into a model host or a speech host. The tool downloads
the models (for example from Hugging Face) to the host.

## Requirements

1. **The tool is AI driven.** A local model helps with the setup: it reads
   the host, finds problems, and does the steps. The model runs on CPU. It
   must fit in the RAM of a laptop. It can be slow.
2. **Basic checks are not AI.** Deterministic code does the checks that
   have one correct answer first. For example: the host is Linux (not BSD,
   macOS, or Windows), the CPU architecture, free RAM and disk, the GPU and
   its driver, and the commands that the tool needs.
3. **Few restrictions on the model.** The model must have flexible
   understanding and tools for the setup work. The tool must not limit it
   to a small fixed set of actions.

## How the tool works

1. **Linux only.** The program stops on BSD, macOS, and Windows with a
   clear message. It also stops on a CPU architecture that has no pinned
   engine build (x86_64 and aarch64 have).
2. **Deterministic checks.** The program reads the facts from probe
   commands: `uname`, `/etc/os-release`, `/proc/meminfo`, `df` of the
   download directory, `lspci`, `nvidia-smi`, `rocm-smi`, the sysfs VRAM
   size, and `vulkaninfo`. A GPU that `lspci` shows, but whose driver does
   not answer, is a problem that the model must tell the person about.
3. **Filter in code.** The program removes each engine build and model
   that does not fit, before the model sees the lists:
   - A GPU engine needs a GPU of its vendor with a driver that answers.
   - On a GPU engine, the model file plus 2 GiB must fit in the GPU memory.
   - On the CPU engine, the model file plus 2 GiB must fit in the
     available RAM, and the file must be 8 GB or smaller (a larger model
     is too slow on a CPU).
   - The model file plus 1 GiB must fit on the free disk.
   The setup loop also refuses a download or start of a removed item.
4. **Setup model.** The program downloads the pinned llama.cpp CPU build
   and the first helper model that fits the RAM (Qwen3.5 9B, else Qwen3.5
   4B), and starts it on `127.0.0.1`. With `-model-url`, it uses an
   OpenAI-compatible endpoint instead.
5. **Tools of the model.** `run_command` (a shell as the person, no sudo),
   `download` (a pinned item, with a SHA-256 check), `start_engine`,
   `ask_person`, and `finish`. The person sees each command and its output.
   With `-confirm`, the program asks before each command. Commands with
   `sudo`, `su`, `doas`, or `pkexec` are refused; the model must ask the
   person and give the exact command.
6. **Engine start.** `start_engine` first runs `llama-server --version`
   (this finds a missing library, for example `libcuda.so.1`). Then it
   starts the engine and waits for `GET /health`. When the setup model
   uses the RAM that a CPU engine needs, only the first part runs, and the
   full check runs after the setup model stops.
7. **Result.** After `finish` with status `ok`, the program stops the setup
   model, does a full start check, and writes
   `DIR/inference-engine.json`. This is the `engine` part of the
   inference host configuration (`primitives/inference`). The program does
   not install services.

Exit status: 0 ok, 1 failed, 2 the host is not supported, 3 the person
must do a step.

## Usage

```sh
cd magi && go build -o magi-host-setup ./cmd/magi-host-setup
./magi-host-setup -check            # the checks and the plan only
./magi-host-setup                   # set up; downloads to ~/.local/share/magi/host-setup
./magi-host-setup -confirm          # ask before each command
./magi-host-setup -model-url http://inference.example.local:8095/v1 -model qwen3.5-9b -model-key-file /path/to/token
```

## Pins

`magi/hostsetup/pins.json` (built into the program) has the source, size,
and SHA-256 of each item. A download that does not match stops, and the
file is removed. The pins now are:

- llama.cpp `b12017` (MIT): CPU (x86_64, aarch64), CUDA 12.8 with its
  runtime (x86_64), ROCm 10.0 (x86_64), Vulkan (x86_64, aarch64). The
  hashes are the GitHub release digests. The CPU and Vulkan hashes were
  also checked on a download.
- Qwen3.5 4B, 9B, and 27B Q4_K_M from `unsloth/Qwen3.5-*-GGUF`, pinned to
  a commit (Apache-2.0). The 9B hash was also checked on a local copy.

The ROCm build is not tested on a host. The engines must listen only on
`127.0.0.1`; the inference host is the part that other machines call.

## Model choice (measured)

The live check `TestLiveHostSetup` (`magi/hostsetup`) gives the model 7
scripted hosts with a fake shell. Each host has one correct result. The
check ran each model 3 times on each host (21 runs), on CPU only
(`-ngl 0 -t 8`, `-c 16384`, llama.cpp b12017), on 2026-10-31.

| Model (Q4_K_M) | File | Server RSS | Correct | Time for each run | Generation |
| --- | --- | --- | --- | --- | --- |
| Qwen3.5 9B | 5.7 GB | 11.1 GB | 20/21 (95%) | 151 s | 5.6 tok/s |
| Qwen3.6 35B-A3B | 22.1 GB | 34.8 GB | 18/21 (86%) | 56 s | 10.2 tok/s |
| Gemma 4 E4B | 5.0 GB | 8.6 GB | 14/21 (67%) | 161 s | 9.2 tok/s |
| Qwen3.5 4B | 2.7 GB | 11.8 GB | 13/21 (62%) | 122 s | 9.5 tok/s |

Errors:

- **Qwen3.5 9B:** on the host with 4 GB of free disk, one run downloaded
  the 9B model, which does not fit.
- **Qwen3.6 35B-A3B:** the same error on the 4 GB disk host, in all 3
  runs. It also does not fit in the RAM of a 16 GB laptop.
- **Gemma 4 E4B:** it selected a 27B model for an 8 GB GPU (3 runs). It
  did not ask the person to install ffmpeg (3 runs).
- **Qwen3.5 4B:** it did not finish within 24 model calls (4 runs). It
  selected the wrong engine or model (4 runs).

Decision: **Qwen3.5 9B Q4_K_M.** It fits in 16 GB of RAM, and it made the
fewest errors. A setup takes about 2 to 3 minutes on CPU.

These results are from the version of the check before the filter in
code. With the filter (2 runs for each host, RTX 4080, 2026-10-31):

| Model (Q4_K_M) | Correct | Time for each run |
| --- | --- | --- |
| Qwen3.5 9B | 14/14 (100%) | 6 s |
| Qwen3.5 4B | 12/14 (86%) | 4 s |
| Gemma 4 E4B | 12/14 (86%) | 9 s |

The errors of the small models are all on the speech host: they did not
ask the person to install ffmpeg with apt.

Notes:

- A run with Qwen3.5 9B on the GPU (an earlier version of the check) got
  11/14 correct, at about 7 s for each run.
- On the host with no NVIDIA driver, one passed run started a 27B model
  on CPU. The 8 GB limit for the CPU engine now prevents this.
- The RSS includes the KV cache for 16384 tokens of context.

## Known limits

- The speech host role is only in the live check. Its engine install is
  still `primitives/speech/engine/install.sh`.
- The program does not install a service. The inference host starts the
  engine when a request comes.
- One full run was done on a real host (2026-11-01): Arch Linux, NVIDIA
  RTX 4080 (17.2 GB). The setup model selected llama.cpp-cuda with Qwen3.5
  9B. The CUDA archive and the cudart archive extract into one directory,
  and the engine was healthy in 2 s. The ROCm and Vulkan layouts were not
  tested on a real host.
