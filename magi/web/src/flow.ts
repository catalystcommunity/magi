// Solid's control flow with types for the h JSX runtime. Without a compiler,
// a reactive prop is a function: h makes each function prop with no
// parameters a getter, so `each={messages}` follows the signal. The Solid
// types expect the plain value (what the compiler gives), so these aliases
// accept both.
import { For as SFor, Show as SShow, Switch as SSwitch, Match as SMatch, type JSX } from "solid-js";

type Maybe<T> = T | undefined | null | false;
type Value<T> = Maybe<T> | (() => Maybe<T>);

export const For = SFor as unknown as <T>(props: {
  each: Value<readonly T[]>;
  fallback?: JSX.Element;
  children: (item: T, index: () => number) => JSX.Element;
}) => JSX.Element;

export const Show = SShow as unknown as <T>(props: {
  when: Value<T>;
  fallback?: JSX.Element;
  children: JSX.Element | ((item: () => NonNullable<T>) => JSX.Element);
}) => JSX.Element;

export const Switch = SSwitch as unknown as (props: { fallback?: JSX.Element; children: JSX.Element }) => JSX.Element;

export const Match = SMatch as unknown as <T>(props: {
  when: Value<T>;
  children: JSX.Element | ((item: () => NonNullable<T>) => JSX.Element);
}) => JSX.Element;

// aria returns a reactive ARIA attribute to spread on an element, for
// example {...aria("pressed", () => String(on()))}.
export const aria = (name: string, value: () => string | undefined) => ({ ["aria-" + name]: value }) as {};
