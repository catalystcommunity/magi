// The Markdown of persona pages (page.tsx). It makes DOM nodes, never HTML
// text, so a page cannot run a script. It has no imports, so tests can load
// it alone.
export function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls)
        e.className = cls;
    if (text !== undefined)
        e.textContent = text;
    return e;
}
const safeURL = (u) => /^(https?:|mailto:|#)/i.test(u.trim());
// inline: **bold**, *italic*, `code`, [text](url), and bare https URLs.
function inline(parent, text) {
    const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*\s][^*]*\*)|(\[[^\]]+\]\([^)\s]+\))|(https?:\/\/[^\s)]+)/g;
    let at = 0;
    for (const m of text.matchAll(re)) {
        if (m.index > at)
            parent.append(text.slice(at, m.index));
        const t = m[0];
        if (m[1])
            parent.append(el("code", "", t.slice(1, -1)));
        else if (m[2]) {
            const b = el("strong");
            inline(b, t.slice(2, -2));
            parent.append(b);
        }
        else if (m[3]) {
            const i = el("em");
            inline(i, t.slice(1, -1));
            parent.append(i);
        }
        else {
            const [label, url] = m[4] ? [t.slice(1, t.indexOf("](")), t.slice(t.indexOf("](") + 2, -1)] : [t, t];
            if (safeURL(url)) {
                const a = el("a", "", label);
                a.href = url;
                if (!url.startsWith("#")) {
                    a.target = "_blank";
                    a.rel = "noopener noreferrer";
                }
                parent.append(a);
            }
            else
                parent.append(label);
        }
        at = m.index + t.length;
    }
    if (at < text.length)
        parent.append(text.slice(at));
}
// markdown renders the blocks of text into root and returns the diagrams.
export function markdown(root, text) {
    const lines = text.replace(/\r\n/g, "\n").split("\n");
    const diagrams = [];
    let i = 0;
    const isTableRow = (l) => /^\s*\|.*\|\s*$/.test(l);
    const cells = (l) => l.trim().replace(/^\||\|$/g, "").split("|").map((c) => c.trim());
    while (i < lines.length) {
        const l = lines[i];
        const fence = l.match(/^\s*```\s*([\w-]*)\s*$/);
        if (fence) {
            const body = [];
            i++;
            while (i < lines.length && !/^\s*```\s*$/.test(lines[i]))
                body.push(lines[i++]);
            i++;
            if (fence[1] === "mermaid") {
                const box = el("figure", "diagram", "Drawing the diagram…");
                root.append(box);
                diagrams.push({ box, code: body.join("\n") });
            }
            else {
                const pre = el("pre");
                pre.append(el("code", "", body.join("\n")));
                root.append(pre);
            }
            continue;
        }
        const h = l.match(/^(#{1,6})\s+(.*)$/);
        if (h) {
            const n = Math.min(6, h[1].length + 1);
            const e = el(("h" + n));
            inline(e, h[2]);
            root.append(e);
            i++;
            continue;
        }
        if (isTableRow(l) && i + 1 < lines.length && /^\s*\|?\s*:?-{2,}/.test(lines[i + 1])) {
            const table = el("table");
            const head = el("tr");
            for (const c of cells(l)) {
                const th = el("th");
                inline(th, c);
                head.append(th);
            }
            table.append(head);
            i += 2;
            while (i < lines.length && isTableRow(lines[i])) {
                const tr = el("tr");
                for (const c of cells(lines[i])) {
                    const td = el("td");
                    inline(td, c);
                    tr.append(td);
                }
                table.append(tr);
                i++;
            }
            const wrap = el("div", "table-wrap");
            wrap.append(table);
            root.append(wrap);
            continue;
        }
        const item = l.match(/^\s*([-*+]|\d+[.)])\s+(.*)$/);
        if (item) {
            const list = el(/\d/.test(item[1]) ? "ol" : "ul");
            while (i < lines.length) {
                const m = lines[i].match(/^\s*([-*+]|\d+[.)])\s+(.*)$/);
                if (!m)
                    break;
                const li = el("li");
                inline(li, m[2]);
                list.append(li);
                i++;
            }
            root.append(list);
            continue;
        }
        if (/^\s*>/.test(l)) {
            const q = el("blockquote");
            const body = [];
            while (i < lines.length && /^\s*>/.test(lines[i]))
                body.push(lines[i++].replace(/^\s*>\s?/, ""));
            inline(q, body.join(" "));
            root.append(q);
            continue;
        }
        if (!l.trim()) {
            i++;
            continue;
        }
        const para = [];
        while (i < lines.length && lines[i].trim() && !/^\s*(```|#{1,6}\s|[-*+]\s|\d+[.)]\s|>|\|)/.test(lines[i]))
            para.push(lines[i++]);
        if (!para.length)
            para.push(lines[i++]);
        const p = el("p");
        inline(p, para.join(" "));
        root.append(p);
    }
    return diagrams;
}
