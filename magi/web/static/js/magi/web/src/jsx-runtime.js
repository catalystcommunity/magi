// The JSX runtime of the web client (the import map gives it the name
// "solid-js/h/jsx-runtime"). It is Solid's h runtime with one change: an
// element makes its child elements when it is made itself, as Solid's
// compiler does.
//
// h returns each element as a function that makes it later. When an element
// has a list of children with a dynamic one in it, h makes the later
// children inside the render effect of that list. When the effect runs
// again (for example a sibling <Show> changes), Solid disposes what the
// effect owns: the DOM of the later children stays, but their reactive code
// (a <For>) is dead and the view stops updating. Elements stay lazy, so
// <Show> still makes its children only when it shows them.
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
export function jsx(type, props) {
    if (typeof type !== "string" || !props || !("children" in props))
        return h(type, props);
    let made;
    const el = () => {
        if (made === undefined) {
            props.children = make(props.children);
            made = h(type, props)();
        }
        return made;
    };
    return Object.assign(el, { [mark]: true });
}
export { jsx as jsxs, jsx as jsxDEV };
export function Fragment(props) { return props.children; }
