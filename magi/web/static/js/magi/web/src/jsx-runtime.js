// The JSX runtime of the web client (the import map gives it the name
// "solid-js/h/jsx-runtime"). It is Solid's h runtime with two changes, so
// that it acts as Solid's compiler:
//
// 1. An element makes its child elements when it is made itself. h makes
//    the later children inside the render effect of the list of children.
//    When the effect ran again (for example a sibling <Show> changed),
//    Solid disposed the reactive code of those children: the DOM stayed,
//    but a <For> in it never updated again.
// 2. Each call of a lazy element makes a new one. h keeps the first result
//    of each element. A <Show> that hid its children and then showed them
//    again got the old, disposed nodes: a list stayed on its first state.
//
// Elements stay lazy, so <Show> makes its children only when it shows them.
import h from "solid-js/h";
// The mark of h's lazy elements (a private symbol of h).
const mark = Object.getOwnPropertySymbols(h("template")).find((s) => s.description === "hyper-element");
const lazy = (v) => typeof v === "function" && mark in v;
function make(c) {
    if (Array.isArray(c))
        return c.map(make);
    while (lazy(c))
        c = c();
    return c;
}
// h changes the props object and its lists, so each call gets copies.
const copy = (props) => {
    const p = { ...props };
    if (Array.isArray(p.children))
        p.children = [...p.children];
    return p;
};
export function jsx(type, props) {
    props ??= {};
    const el = typeof type === "string"
        ? () => {
            const p = copy(props);
            if ("children" in p)
                p.children = make(p.children);
            return h(type, p)();
        }
        : () => h(type, copy(props))();
    return Object.assign(el, { [mark]: true });
}
export { jsx as jsxs, jsx as jsxDEV };
export function Fragment(props) { return props.children; }
