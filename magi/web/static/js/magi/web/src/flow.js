// Solid's control flow with types for the h JSX runtime. Without a compiler,
// a reactive prop is a function: h makes each function prop with no
// parameters a getter, so `each={messages}` follows the signal. The Solid
// types expect the plain value (what the compiler gives), so these aliases
// accept both.
import { For as SFor, Show as SShow, Switch as SSwitch, Match as SMatch } from "solid-js";
export const For = SFor;
export const Show = SShow;
export const Switch = SSwitch;
export const Match = SMatch;
// aria returns a reactive ARIA attribute to spread on an element, for
// example {...aria("pressed", () => String(on()))}.
export const aria = (name, value) => ({ ["aria-" + name]: value });
