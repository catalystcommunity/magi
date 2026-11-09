# Web client

The browser client of the Magi API. It is one replaceable client: it uses the
generated TypeScript clients and has no access that other clients do not
have. Later, the client moves to the Catalyst `zest` components.

## Build

```sh
tools/build-web.sh     # compile src/ into static/js (tsgo, no Node)
tools/vendor-web.sh    # get Solid again (only to change its version)
```

Commit `static/js` with the source. `magi-server` embeds `static/`, so a Go
build does not need TypeScript.

## How it works without Node

- The views use SolidJS 1.9 (MIT) with JSX. There is no Babel and no
  bundler. tsgo compiles TSX with `jsxImportSource: "solid-js/h"`.
- `tools/vendor-web.sh` gets `solid-js` from the npm registry, checks the
  sha512 hash, and copies the runtime files to `static/vendor/solid-js/` and
  the types to `types/solid-js/`. `types/csstype` is a small stub.
- The import map in `static/index.html` gives the browser the bare names
  (`solid-js`, `solid-js/web`, `solid-js/h`). It maps
  `solid-js/h/jsx-runtime` to `src/jsx-runtime.ts`, not to Solid's file.
- `web.go` changes each `/s/@/` in `index.html` to `/s/<content hash>/`. A
  new build has new file URLs, so caches never mix two builds.

## Rules for the JSX

Without the Solid compiler, a reactive value must be a function:

| Do this | Not this |
| --- | --- |
| `<For each={messages}>` (the signal) | `<For each={messages()}>` (one value, never updates) |
| `<p>{() => name()}</p>` | `<p>{name()}</p>` |
| `class={() => (on() ? "on" : "")}` | `class={on() ? "on" : ""}` |
| `{...aria("pressed", () => String(on()))}` | `aria-pressed={...}` with a function (the types refuse it) |

- Import `For`, `Show`, `Switch`, and `Match` from `src/flow.ts`. They are
  Solid's, with types that accept a signal.
- h makes each function prop with no parameters on a component a getter. A
  callback prop of a component must have a parameter, for example
  `close={(_: unknown) => ...}`, or h calls it when the component reads it.
- `src/jsx-runtime.ts` replaces Solid's h runtime. Keep it. It fixes two
  bugs of h that the client had:
  1. h makes the children of an element later, inside the render effect of
     the parent. When that effect ran again (a sibling `<Show>` changed),
     Solid disposed their reactive code: the conversation stopped showing
     new messages. The runtime makes the children with the element.
  2. h keeps the first result of each element. A `<Show>` that hid its
     children and showed them again got the old, disposed nodes: the
     decided network reviews never showed. The runtime makes new nodes on
     each call.
- `browser_test.go` runs these cases in headless Chromium or Chrome
  (`MAGI_TEST_BROWSER`, or one on the PATH; skipped when there is none).
  It also tests the voice activity detector (`src/vad.ts`) and the whole
  hands-free microphone path (`src/listen.ts`, `src/capture-worklet.ts`)
  with the fake microphone of Chromium. The page sends its result to the
  test server.

## Design

Light sky blues with the warm light of a sunrise. The persona is warm
(apricot and peach); the person and the actions are blue. The orb is the
presence of the persona: it breathes while the persona speaks and turns blue
while it listens. The colors are tokens on `:root` in `static/style.css`,
with dark values for `prefers-color-scheme: dark` and `data-theme`. There
are no font files: headings use the rounded system face where there is one.

| Token | Light | Use |
| --- | --- | --- |
| `--sky` | `#eaf2fa` | page |
| `--cloud` | `#fafcfe` | surfaces |
| `--harbor` | `#1f3a5f` | text |
| `--blue` | `#2f6fca` | actions, the person's messages |
| `--apricot` | `#f3a65f` | the persona, counts that need you |
| `--peach` | `#fff0e2` | the persona's messages |

## Views

| Hash | View | File |
| --- | --- | --- |
| `#/talk` | The stream with the selected persona, search, context | `src/conversation.tsx` |
| `#/questions` | Questions of the librarian | `src/review.tsx` |
| `#/network` | Network reviews of the egress proxy (admins only) | `src/review.tsx` |
| `#/tasks`, `#/task/<id>` | Tasks and their decisions | `src/tasks.tsx` |
| `#/terminal/<id>` | A shell in the sandbox of a running task (xterm.js, loaded only here) | `src/terminal.tsx` |
| `#/knowledge`, `#/words` | Knowledge and the household lexicon | `src/knowledge.tsx` |
