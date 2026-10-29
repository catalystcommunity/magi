# agent

The Magi agent harness. Magi owns this code. It does not wrap another agent
product.

## Purpose

The agent does one coding task in a Magi execution environment:

1. It gets a task from the workflow queue (worker mode) or from the command
   line (one-shot mode).
2. It sends the task to a model through the OpenAI-compatible Chat Completions
   API with tool calling.
3. It runs the tools that the model selects. All effects go through the
   ExecutionService (`exec`, `read-file`, `write-file`).
4. It records each model request, model response, tool call, and tool result
   as JSONL. At the end, it uploads this transcript to the ArtifactService.
5. It completes the workflow step with a summary.

The agent does not commit or push. Separate workflow steps do git operations,
unless the task instructions tell the agent to do them.

## Tools

| Tool | Function |
| --- | --- |
| `bash` | Runs `bash -lc <command>` in the work directory. Has a timeout. Returns the exit code, stdout, and stderr. |
| `read_file` | Reads a file with line numbers. `offset` and `limit` select lines. |
| `write_file` | Creates or replaces a file. Creates parent directories. |
| `edit_file` | Replaces an exact string. The string must be unique, unless `replace_all` is true. |
| `list_files` | Lists files below a directory (skips `.git`). An optional glob filters them. |
| `search` | Searches file contents with ripgrep (or `grep -rE` if ripgrep is not installed). |
| `finish` | Stops the task with a summary. |

When a check command is set, the harness runs it when the model calls
`finish`. If the check fails, the harness rejects `finish` and shows the
output to the model. After `MAGI_AGENT_CHECK_RETRIES` failures, the task
finishes with `agent.check_passed` = `false`.

## Context management

- Each tool result in the context is cut to `MAGI_AGENT_TOOL_OUTPUT_CHARS`.
  The head and the tail stay; the middle is removed.
- The harness estimates the prompt size. It calibrates the estimate with the
  token counts that the server returns. When the estimate is more than
  `MAGI_AGENT_CONTEXT_TOKENS`, the harness replaces old tool results and old
  large tool arguments with short notes. If this is not sufficient, it
  removes the oldest turns. The system prompt and the task always stay.
- Before each model call, the harness calls a steering hook. The hook returns
  operator notes. The default hook returns no notes, because the workflow
  service cannot send messages to a worker yet.

## Worker mode

```sh
python -m magi_agent worker [--queue agent] [--max-tasks N]
```

The worker claims a step from the queue, sends heartbeats on a background
thread, and sends progress notes: at the start, after each
`MAGI_AGENT_PROGRESS_EVERY` tool calls, and at the end.

Assignment vars:

| Var | Use |
| --- | --- |
| `environment` | Execution environment ID. Required. |
| `instructions` | Task text. Required. |
| `workdir` | Work directory. Default `/work/repo`. |
| `title` | Short task title. Optional. |
| `check_command` | Check command. Default `MAGI_AGENT_CHECK_COMMAND`. |
| `check_output` | Output of a failed check from an earlier attempt. |
| `review.comment` | Changes that a reviewer asked for. |
| `model` | A model name, or JSON with `name`, `temperature`, `max_tokens`, `max_steps`. The base URL and the key cannot change through vars. |

Outputs: `agent.summary`, `agent.transcript` (artifact digest), `agent.steps`,
`agent.status`, `agent.tool_calls`, and `agent.check_passed` (when a check
ran).

Outcomes:

- `done`: the agent finished, or it stopped at a step or time limit
  (`agent.status` tells which). The workflow checks are the independent
  judgment.
- `fail`: an error that the agent cannot recover from, for example a model
  that does not answer. `error` has the cause.
- `retry`: the worker got SIGTERM during the task.
- No completion: the claim was lost (for example, the run was cancelled).

## One-shot mode

```sh
# In an execution environment:
python -m magi_agent run --environment env_123 --instructions "Fix the failing test"
# On this host, in a directory (no isolation; for tests only):
python -m magi_agent run --local /tmp/work --instructions @task.txt \
  --check-command "python3 -m unittest" --transcript /tmp/t.jsonl
```

The command prints a JSON result. The exit code is 0 when the task finished
and no check failed.

## Configuration

| Variable | Default | Use |
| --- | --- | --- |
| `MAGI_MODEL_BASE_URL` | none (required) | Chat Completions base URL |
| `MAGI_MODEL_NAME` | `magi-coder` | Model name |
| `MAGI_MODEL_API_KEY_FILE` | none | File with the model API key |
| `MAGI_MODEL_API_KEY` | none | Key value, if no key file is set |
| `MAGI_MODEL_TIMEOUT` | `300` | Request timeout in seconds |
| `MAGI_MODEL_MAX_RETRIES` | `5` | Retries on 429, 5xx, and connection errors |
| `MAGI_MODEL_MAX_TOKENS` | `4096` | Output tokens for each reply |
| `MAGI_MODEL_TEMPERATURE` | `0.2` | Sampling temperature |
| `MAGI_WORKFLOW_URL` | none | WorkflowService base URL (worker mode) |
| `MAGI_EXECUTION_URL` | none | ExecutionService base URL |
| `MAGI_ARTIFACTS_URL` | none | ArtifactService base URL. If not set, no upload |
| `MAGI_TOKEN_FILE` | none | Bearer token file. The agent reads it again for each call |
| `MAGI_AGENT_QUEUE` | `agent` | Workflow queue |
| `MAGI_AGENT_WORKER_ID` | `agent-<host>-<pid>` | Worker name for claims |
| `MAGI_AGENT_CHECK_COMMAND` | `./tools/check.sh` | Default check command (worker mode). Empty disables it |
| `MAGI_AGENT_MAX_STEPS` | `60` | Model calls for each task |
| `MAGI_AGENT_MAX_SECONDS` | `1800` | Wall-clock limit for each task |
| `MAGI_AGENT_CONTEXT_TOKENS` | `24000` | Compaction threshold (estimate) |
| `MAGI_AGENT_TOOL_OUTPUT_CHARS` | `8000` | Tool result size in the context |
| `MAGI_AGENT_BASH_TIMEOUT` | `300` | Default bash timeout in seconds (maximum 1200) |
| `MAGI_AGENT_PROGRESS_EVERY` | `5` | Tool calls between progress notes |
| `MAGI_AGENT_CHECK_RETRIES` | `3` | Failed checks before `finish` is accepted |

For llama.cpp, start `llama-server` with `--jinja`. For a hosted provider,
change only the base URL, the model name, and the key file.

## Secrets

The transcript and the model context do not contain these values:

- The model API key and the Magi bearer token.
- The values of environment variables with `TOKEN`, `KEY`, `SECRET`,
  `PASSWORD`, `PASSWD`, or `CREDENTIAL` in the name.

The harness replaces each of these values with `[REDACTED]`. Values shorter
than 6 characters are not replaced.

## Image

```sh
docker build -f primitives/agent/Dockerfile -t magi-agent .
```

Build from the repository root. The image copies the generated clients from
`clients/python`. It runs as user 10001. The default command is `worker`.

## Tests

```sh
cd primitives/agent
python3 -m unittest discover -s tests          # or: .venv/bin/python -m pytest
```

The unit tests use a scripted model, an in-memory execution client, and a
fake workflow and artifact service behind the generated clients. One test
uses the real HTTP transport.

Live test with a real model. The tools run as local subprocesses in a
temporary directory:

```sh
MAGI_TEST_MODEL=1 MAGI_MODEL_BASE_URL=http://model.example.local/v1 \
MAGI_MODEL_API_KEY_FILE=/path/to/key python3 -m unittest discover -s tests -p test_live.py
```

## Known limits

- No streaming. A long reply waits for the full response.
- The environment cuts long output from the end. If a command writes more
  than 1 MiB, the end is lost; the tool result tells the model this.
- The token estimate is approximate before the first server response.
- The steering hook has no source in the workflow service yet.
- The agent does one task at a time. Run more workers for more parallel
  tasks.
- `--local` mode has no isolation. Use it only for tests.
