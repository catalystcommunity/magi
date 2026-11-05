# Prompt injection: research and defense plan

Research date: 2026-10-31. This document is for people who design or review
Magi. It tells what the industry and research use against prompt injection
today, what each method stops, what it does not stop, and how Magi will
combine them.

## 1. The problem

A model cannot reliably separate instructions from data. When Magi reads
text that it did not get from the person, the text can contain instructions.
Examples of such text:

- a web page, a document, or an email;
- a message from another member of the household;
- a file, an issue, or test output in a repository;
- a record in memory or in the knowledge graph that came from one of these;
- speech from a television, a guest, or a recording.

The model can obey these instructions. Then it can do an action that the
person did not ask for, or send private data to the attacker.

Two design rules state when this becomes dangerous:

- **The lethal trifecta** (Simon Willison, 2025): private data, untrusted
  content, and a way to send data out. When a component has all three, an
  attacker can steal data.
- **The Agents Rule of Two** (Meta, 2025): (A) the component reads
  untrusted input, (B) it can get private data or sensitive systems, (C) it
  changes state or communicates out. Give a component a maximum of two of
  these three in one session. When a task needs all three, divide the task
  or put a person in the loop.

## 2. The most important result

In October 2025, Nasr, Carlini, and others attacked 12 published defenses
with attacks that adapt to the defense ("The Attacker Moves Second", USENIX
Security 2026). Most of these defenses reported an attack success rate near
0%. The adaptive attacks got more than 90% against most of them. Human red
teamers broke all 40 challenges.

Defenses of these types failed:

- prompt methods (delimiters, spotlighting, reminders after the data);
- detector models (for example, Prompt Guard and ProtectAI);
- models trained to ignore injected instructions (for example, Meta
  SecAlign: 2% before, 96% after the adaptive attack);
- methods that test the model with a secret or a second run (DataSentinel,
  MELON).

The defenses that stayed strong are **system designs**. In these designs,
untrusted text cannot change which action runs, or where data goes. A
deterministic check, not a model, makes that decision. The adaptive
benchmark AutoDojo (2026) found that these designs "held up best".

**Rule:** A defense is strong only when no token from the attacker can
change the security decision. When a model reads attacker text and the
model output decides the security result, an attacker can find an input
that wins.

## 3. Methods

### 3.1 System designs (strong)

| Method | What it does | Measured result | Cost |
| --- | --- | --- | --- |
| Dual LLM (Willison, 2023) | A privileged model sees only trusted input and calls tools. A quarantined model reads untrusted text and returns only values or opaque handles. | Design only | Engineering. The person can still be deceived by text from the quarantined model. |
| CaMeL (Google DeepMind, 2025) | The privileged model writes a program from the person's request only. An interpreter runs it. Each value has a label for its source and its readers. A policy is checked at each tool call. | AgentDojo: 77% of tasks done with the defense, 84% with no defense. | Fails on open tasks (AgentDyn: 0% useful). Needs a strong planner. |
| FIDES (Microsoft, 2025) | Information-flow control. Labels for confidentiality and integrity. Deterministic policy checks. | Stopped all AgentDojo attacks with policy checks on. | Strong planner. Up to 29.6 times the tokens for computer use. |
| Progent (2025) | A policy language that tells which tool, with which arguments, is permitted. | AgentDojo, adaptive, Qwen2.5-7B: 25.8% to 4.2%. | Policies to write. Less useful as the tool set grows. |
| ROPE (August 2026) | A value can go into a sensitive tool argument only when it comes from the person, from a source that the person named, or from the person's own records. | 1.6–2.6% attack success, 82–100% of normal usefulness. Optimized attacks did not help much. Not replicated yet. | Each value must carry its source. |
| Sandbox, least privilege | Limits what an attack can do. It does not stop the attack. | Incidents in 2026 show the need. | Infrastructure. |

Limits of system designs:

- They do not stop **data changes**. A web page can make a summary false
  without changing any action (CaMeL calls this a "text-to-text" attack).
- **Branch steering:** the attacker can make a permitted branch of an
  approved plan go to a bad target.
- The plan is only as safe as its inputs. Memory that contains attacker
  text makes the planner unsafe.

### 3.2 Trained models (lower the rate; not a boundary)

- **Instruction hierarchy** (OpenAI): the model is trained to rank system,
  developer, user, and tool text. The data is not public.
- **StruQ, SecAlign, Meta SecAlign:** a separate role for untrusted data,
  and training to ignore instructions in it. These are good against fixed
  attacks and weak against adaptive attacks. Meta SecAlign uses the Llama
  licence.
- **SecOPD** (August 2026): the training includes adaptive attacks. The
  released model is Qwen3.6-27B-SecOPD (Apache-2.0). It reports 9% attack
  success under an adaptive attack, compared with 94% for Meta SecAlign. The
  authors measured it with their own attack. It is too large for the 16 GB
  card together with the chat model.
- **Frontier vendors** report 1–2% under their own adaptive attacks for
  browser use (Anthropic, November 2025). These numbers are from the vendor.
- **Qwen:** we found no published training against injection for Qwen3,
  Qwen3.5, or Qwen3.6. User reports show Qwen models obey injected text.
  **Magi must think that Qwen3.5 9B obeys injected text.**

### 3.3 Prompt methods (almost free; weak)

- **Delimiters, spotlighting, datamarking, encoding:** mark the untrusted
  text. These cut naive attacks by a large amount. Adaptive attacks get more
  than 95%. In one human competition, spotlighting made the attack rate
  higher.
- **Reminder after the data:** the task is repeated after the untrusted
  text. Small help.
- **Destyling:** models decide the role of text from its style, not from its
  tag. Removing role-like headers, fake reasoning blocks, and chat format
  from untrusted text cut attack success from 61% to 10% in one study (ICML
  2026).

Use these methods. Do not count on them as security.

### 3.4 Detectors (triage; not a gate)

| Detector | Size | Licence | Note |
| --- | --- | --- | --- |
| PIGuard | about 184M | MIT | Best of the filters under adaptive attack (71%). Useful text is often lost when it fires. |
| ProtectAI deberta v2 | 184M | Apache-2.0 (archived) | English only. 8.8% false positives on one independent test. |
| Prompt Guard 2 (86M, 22M) | small | Llama 4 licence (restrictive) | 25.5% false positives at 95% detection on an independent test. |
| BrowseSafe | 30B (3B active) | MIT | Too large for the card with the chat model. |
| Qwen3Guard, Granite Guardian | 0.6–8B | Apache-2.0 | These find harmful content, not injection. Do not use them for injection. |

A detector costs tens of milliseconds for each 512 tokens. Use it to send
suspicious content to a stricter path. Do not use its score to give
permission.

### 3.5 Output and egress controls (strong for data theft)

- **Markdown images and links** are the most frequent real way to steal
  data. Examples: EchoLeak in Microsoft 365 Copilot (2025), and many 2026
  incidents.
- **An allowlist of domains is not sufficient.** Attacks used allowed
  domains that accept writes from anybody or that redirect (Google Forms, an
  API that accepts the attacker's own key, an open redirect).
- Controls: show no remote images, permit exact destinations only, and do
  not let the model make a URL that contains data.

### 3.6 Confirmation by a person (weak at volume)

A person can stop a rare, clear, high-impact action. A person fails when
there are many questions. In a 2026 study of 1,053 developers, only 13.6%
refused a dangerous command in a permission prompt. Also, untrusted text can
write the request for approval (a pull request description, a summary).

Ask only for actions that cross a boundary. Show the exact bytes or diff and
their source. Do not show a justification that a model wrote.

## 4. Layers for Magi

Each layer stops a class of attack that other layers miss.

| Layer | Stops | Does not stop |
| --- | --- | --- |
| L0. Rule of Two for each component | The structural route to theft or damage | Attacks inside the two permitted properties |
| L1. Identity scope (row-level security, read as the person) | Reading data that the person cannot read | Misuse of data that the person can read |
| L2. Plan from trusted input only | New tool calls from data | Branch steering, changed arguments |
| L3. Source checks on sensitive arguments | Recipients, URLs, visibility, and code targets that come from untrusted text | Wrong values that come from real data |
| L4. Quarantined reader with typed output | Instructions in summaries that go to the planner | A wrong value from the permitted set |
| L5. Hardened model | Most casual injections | Adaptive attacks |
| L6. Detector and destyling at input | Bulk, known styles | New wording |
| L7. Egress control | Theft with no click | Theft through permitted actions |
| L8. Sandbox with no credentials | Damage from code that runs | Changes that pass review |
| L9. Person reviews rare, clear actions | Final high-impact actions | A tired or deceived reviewer |
| L10. Audit log and undo | Long-term harm | Harm before anybody sees it |

Risks that remain after all layers:

1. False data with no change of action (a false answer, a false link).
2. Attacker text in the planner's own inputs (memory, examples).
3. Branch steering in an approved plan.
4. A deceived reviewer.
5. Dormant injections that start on a trigger later. In one 2026 study,
   these got 43–83% success on nine production agents.
6. Bad files that normal commands run (supply chain).

For risks 1 and 2, show sources and provenance. Keep the untrusted label on
each record that came from untrusted text.

## 5. Magi components

### 5.1 Librarian

The librarian reads each utterance and proposes links and a visibility. Its
token has `conversation:act-as`. It reads as the person of the stream,
through the persona of the stream (`magi/compose/librarian.go`). Thus
row-level security limits what it reads. Done on 2026-11-02 (plan step 1):

- Only text of the person, and of the persona in the same stream, goes
  into the context. Search hits from other members stay out.
- The conversation service, not the model, decides when to ask. A
  classifier can make a message more private with no question. It cannot
  make a message shared in a private persona: the person always gets a
  question, whatever the confidence. In a public persona, shared is the
  default, and the threshold applies. **A confidence threshold is not a
  security control.** The live test showed 7 wrong decisions with a
  confidence of 0.9 or more in 29.
- A JSON Schema limits the answer (a grammar in llama.cpp): link targets
  only from the candidate ids, the known link kinds, and the visibility
  values of the persona.
- The examples come only from the answers of the person.

This gap remains:

- A false link can show an "about" line in a shared view.

### 5.2 Persona turn

The tools are recall, remember, search_conversation, start_code_change,
list_tasks, and cancel_task. Search results and memory can contain
untrusted text now. `remember` can make an injection permanent.
`start_code_change` can turn attacker text into a task. A web fetch tool
will add a way to send data out: a URL that the model makes can carry data
in the request.

### 5.3 Apprentice

README files, issues, comments, test output, and dependency documents are
untrusted. A 2026 test of 500 README files found that injection worked about
84% of the time against the major coding assistants. The finish gate runs
the check command of the repository. A changed check script is attacker
code. The summary that the reviewer reads can be attacker text.

### 5.4 Memory and knowledge graph

One bad memory write can affect all later turns. The usual defenses do not
cover it (MPBench, 2026). The knowledge graph has evidence links to
utterances. Use them as the provenance of each record.

### 5.5 Speech

Magi uses a cascade: speech to text, then a text model. Attacks on models
that take audio directly do not apply. But speech in the room becomes text
from the person. A television or a guest can speak instructions. Reading an
untrusted page aloud lets its author speak with the voice of Magi.

## 6. Plan, in order

1. **Librarian.** Apply visibility changes that make a message more private
   with no question. Always ask the person before a message becomes shared.
   Do not put text from other people in the context that decides a
   visibility. Limit the output with a grammar: the visibility values, and
   link targets from the candidate list only. Take examples only from the
   person's own answers. *Why:* it is the only part that has all three Rule
   of Two properties now, and it uses the weakest model.
2. **Source labels and deterministic checks.** Give each value a source:
   person, named source, own record, or untrusted. Let only person values go
   into sensitive arguments: visibility, recipient, URL, repository and
   branch of a code change, and memory. *Why:* this is the class of defense
   that stayed strong against adaptive attacks.
3. **Quarantined reader.** A local model reads web pages, documents, other
   people's messages, and repository text. It returns typed values or
   handles. The planner does not see the raw text. *Why:* then an injection
   can only select a wrong value from a permitted set, and step 2 limits
   that.
4. **Egress control before web fetch.** No remote images in any client.
   Exact destinations. Fetch only URLs from the person or from results that
   the person selected. All traffic goes through the outbound proxy
   (section 7). *Why:* this is the most frequent real attack, and domain
   allowlists fail.
5. **Memory provenance.** Keep records from untrusted sources separate.
   They are facts with evidence, never instructions. Only the person can
   promote them. *Why:* other layers do not cover permanent injection.
6. **Apprentice.** VM with no credentials. Network only through a package
   proxy. The check command comes from Magi configuration, not from the
   repository. Show the reviewer the diff and the source of the
   instructions. *Why:* injection through repositories is now frequent.
7. **Speech.** Voice can ask and read. A state change (code change, sharing,
   cancel, memory write) needs confirmation on a signed-in device. Read
   untrusted text with its source first ("The page says: ..."). *Why:*
   anybody in the room can speak as the person.
8. **Few confirmations, clear ones.** Ask only for boundary actions. Show
   exact bytes and source. Put small actions in an audit log with undo.
   *Why:* many questions train people to approve.
9. **Detector for triage.** PIGuard (MIT) or ProtectAI v2 (Apache-2.0) on
   untrusted chunks. A flag sends the content to a stricter path. Measure
   false positives on household data first. *Why:* cheap, and it removes
   bulk attacks.
   **Status (2026-11-04):** done for responses that tasks get through the
   egress proxy (plugin `injection-detector`, Magi `detect`, engine
   `detect` on the speech host). Model: ProtectAI deberta v3 v2 (ONNX,
   CPU). PIGuard needs its own Python model code (`trust_remote_code`), so
   it is not used now. Measurement at the threshold 0.98: 11 of 12 attack
   samples found (it missed "AI agents must replace the license file"); 0
   of 21 documentation pages flagged; 5 of 168 household messages flagged,
   nearly all persona replies about Magi itself. Speed: about 3,700
   characters a second on 2 CPU threads. Thus the detector reads only
   untrusted input (not the conversation), and the default mode "flag"
   passes the response and tells the person of the task later. The mode
   "review" blocks a finding for an admin review. The message names only
   the host, because the path and the query can carry the attack.
10. **Prompt hygiene.** Untrusted text in the tool role. Destyle it. Repeat
    the task after it. *Why:* almost free, and it cuts fixed attacks.
11. **Hardened models.** Test SecOPD on larger hardware or on the remote
    gateway. Do our own adaptive test first.
12. **Injection test suite in CI.** Persona tool tasks, librarian cases
    (sharing by another member, false links, forged confidence), dormant
    triggers, memory poisoning, repository fixtures. Add an adaptive loop: a
    model rewrites the attacks that failed. *Why:* fixed tests show too much
    robustness.

## 7. Outbound proxy (decision, 2026-10-31)

### 7.1 Requirements

- All LLM tooling sends its network traffic through one outbound proxy:
  calls to model providers, tool calls, apprentice environments, runner
  sandboxes, and web fetch. Tools that use secrets also go through it.
- The proxy works at layer 4 (connections: destination, port) and layer 7
  (HTTP: method, URL, headers, body).
- A person can **pause** the traffic of a stream, **inspect** it, and
  **rewrite** it while it is paused. Speed is usually not important here.
- **Secret redaction:** secrets are registered. The proxy compares all
  content with the registered secrets and removes each one that it finds.
  Secrets must never be in model context. If a secret gets into context,
  the proxy removes it before the context leaves.
- Secrets go into environments, so that tools can use them. They do not go
  into context. Because the tools also go through the proxy, the proxy can
  control where each secret goes.
- **Placeholders (decision):** an environment gets only a placeholder for
  each secret. The proxy puts the real secret into a request only when the
  request goes to a destination that the secret is bound to. Thus the real
  secret is never in the environment, in the tool output, or in context.
- **Encodings (decision):** the proxy decodes content that is encoded
  (base64, hex, URL encoding, and others) and checks the decoded content
  again. Content that is still encoded after the decode (for example base64
  of base64), or that the proxy cannot decode to a known form, stops the
  request. A normal tool has no reason to encode data many times.
- **Easy routing (decision):** it must be very easy to make the proxy the
  only route out of an environment that runs a task. The proxy is a setting
  of the environment, not work for each tool.

### 7.2 Design notes

These notes are recommendations. They are not decisions yet.

1. **Both directions.** Remove secrets from requests that go out (a prompt
   to a model provider, a tool request), and from responses that come back
   into context (tool output, a fetched page). A tool that prints its
   environment must not put a secret into context.
2. **Encoded forms.** Compare each secret as plain text and in the usual
   encodings: base64 (standard and URL), hex, URL encoding, and JSON
   escapes. Decode encoded parts and check them again, to a fixed depth.
   Stop the request when a part is still encoded at that depth. Compressed
   bodies (gzip, zstd) are decoded first. Look across chunk boundaries: the
   proxy can keep each stream in a buffer, because speed is not important.
   Use a multi-pattern search (Aho-Corasick). Do not register very short
   secrets; they match by chance. Encryption with a key that the attacker
   chose cannot be found this way; that is why placeholders and bound
   destinations (note 3) are the main control, and the scan is the second.
3. **Secrets bound to destinations.** Each registered secret has a
   placeholder and the destinations where it can go (for example a GitHub
   token to `api.github.com`, in the `Authorization` header). The proxy
   changes the placeholder to the secret only in a request to a bound
   destination, in the bound place. A placeholder in a request to another
   destination stops the request. The proxy also looks for the real secret
   in all traffic (a secret that came in another way) and removes it.
   Placeholders must be random and unique, so that the proxy does not
   change text that only looks like one.
4. **TLS.** Layer 7 inspection of HTTPS needs the proxy to end TLS with its
   own certificate authority, and the environments must trust it. Without
   that, the proxy sees only layer 4 and the TLS server name.
5. **No other route out.** The proxy works only when it is the only way
   out. Otherwise a tool can go around it. To make this easy, each
   environment kind gets it from one setting:
   - Cluster environments: a NetworkPolicy that permits egress only to the
     proxy, and the proxy address and CA in the environment.
   - Runner sandboxes (bubblewrap): a network namespace with no route but
     the proxy.
   - Runner task VMs: no network card; the guest sends each TCP connection
     to the proxy (see `docs/egress-proxy.md` section 4).
   - mitmproxy modes that need no change to the tools: WireGuard (the
     environment connects to the proxy as a VPN) and transparent or TUN
     mode on Linux.
6. **Hold queue.** Policy can hold a request for a person: a new
   destination, a flag from the detector (section 3.4), or a request that
   carries data of the person. The pause, inspect, and rewrite view is the
   same view.
7. **Log.** The proxy records each request and response (with secrets
   removed) as an audit record of the stream (layer L10).
8. **Engine.** mitmproxy (MIT licence) is an open intercepting proxy. It
   ends TLS with its own CA. Its console (`mitmproxy`) and web view
   (`mitmweb`) can hold a request or response, show it, and let a person
   change it before it goes on. `mitmdump` runs it with no view. Python
   addons see each flow, so policy, placeholders, and redaction can be an
   addon. Modes: regular and SOCKS5 proxy, transparent, reverse (also raw
   TCP and UDP), WireGuard, local capture by program, and TUN.
   **Decision:** Magi makes its own proxy in Rust, with WASM plugins that
   can call Magi. mitmproxy is a reference for features. See
   `docs/egress-proxy.md`.

## 8. Sources

- Nasr, Carlini et al., "The Attacker Moves Second", arXiv 2510.09023,
  2025-10-10. https://arxiv.org/abs/2510.09023
- Debenedetti et al., CaMeL, arXiv 2503.18813, 2025.
  https://arxiv.org/abs/2503.18813
- Costa et al., FIDES, arXiv 2505.23643, 2025.
  https://arxiv.org/abs/2505.23643
- Beurer-Kellner et al., "Design Patterns for Securing LLM Agents against
  Prompt Injections", arXiv 2506.08837, 2025.
  https://arxiv.org/abs/2506.08837
- Shi et al., Progent, arXiv 2504.11703, 2025.
  https://arxiv.org/abs/2504.11703
- Ma et al., ROPE, arXiv 2608.27496, 2026-08-27.
  https://arxiv.org/abs/2608.27496
- Ma et al., AutoDojo, arXiv 2606.15057, 2026.
  https://arxiv.org/abs/2606.15057
- Li et al., AgentDyn, arXiv 2602.03117, 2026.
  https://arxiv.org/abs/2602.03117
- Peng et al., SecOPD, arXiv 2608.21500, 2026.
  https://arxiv.org/abs/2608.21500
- Chen et al., Meta SecAlign, arXiv 2507.02735, 2025.
  https://arxiv.org/abs/2507.02735
- Ye et al., "Prompt Injection as Role Confusion", arXiv 2603.12277, 2026.
  https://arxiv.org/abs/2603.12277
- Hines et al., Spotlighting, arXiv 2403.14720, 2024.
  https://arxiv.org/abs/2403.14720
- Meta, "Agents Rule of Two", 2025-10-31.
  https://ai.meta.com/blog/practical-ai-agent-security/
- Willison, "The lethal trifecta for AI agents", 2025-06-16.
  https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/
- Willison, "The Dual LLM pattern", 2023-04-25.
  https://simonwillison.net/2023/Apr/25/dual-llm-pattern/
- Willison, prompt-injection tag (2026 incidents).
  https://simonwillison.net/tags/prompt-injection/
- Anthropic, "Mitigating the risk of prompt injections in browser use",
  2025-11-24. https://www.anthropic.com/research/prompt-injection-defenses
- Bastion, pi-detector-bench, 2026.
  https://github.com/bastion-soft/pi-detector-bench
- Li et al., PIGuard, arXiv 2410.22770. https://huggingface.co/leolee99/PIGuard
- Szczepaniak et al., "Defusing Explosive Prompts", arXiv 2609.22510, 2026.
  https://arxiv.org/abs/2609.22510
- Dash et al., MPBench, arXiv 2606.04329, 2026.
  https://arxiv.org/abs/2606.04329
- Cloud Security Alliance, "README Injection", 2026-03-17.
  https://labs.cloudsecurityalliance.org/

Many 2026 papers were read at abstract level only. OpenAI pages were not
readable (HTTP 403); their content comes from secondary reports.
