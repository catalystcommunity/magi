// Devices and notices (docs/attention.md). The page says hello as a device
// of the person and waits for notices. A notice that is for this device
// (or for all devices) is:
//   - said, after a chime, when the page can play audio and the person
//     uses the page (it is visible, or hands-free talk is on): "high" at
//     once, also over a persona that speaks; "normal" at the next pause
//     (no persona speaks and the person does not speak);
//   - a system notification when the page is hidden (if the person
//     allowed notifications);
//   - always in the tray, until the person dismisses it on any device.
import { createSignal, createEffect, createRoot } from "solid-js";
import { magi, store } from "./api.js";
import { speak, speaking, interrupt, unlocked, chime } from "./speech.js";
import { handsFree, hearing } from "./listen.js";
export const [tray, setTray] = createSignal([]);
const deviceId = (() => {
    let id = store.get("magi.device");
    if (!id) {
        const b = new Uint8Array(12);
        crypto.getRandomValues(b);
        id = "web-" + Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
        store.set("magi.device", id);
    }
    return id;
})();
function deviceName() {
    const ua = navigator.userAgent;
    const browser = /Firefox\//.test(ua) ? "Firefox" : /Edg\//.test(ua) ? "Edge" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "Browser";
    const os = /Android/.test(ua) ? "Android" : /iPhone|iPad/.test(ua) ? "iOS" : /Mac OS X/.test(ua) ? "Mac" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "";
    return os ? `${browser} on ${os}` : browser;
}
const active = () => document.visibilityState === "visible" || handsFree();
let lastHello = "";
async function hello(force = false) {
    const can = ["speak", "show"];
    if ("Notification" in window && Notification.permission === "granted")
        can.push("notify");
    const req = { id: deviceId, kind: "browser", name: deviceName(), can, active: active(), handsFree: handsFree() };
    const key = JSON.stringify(req);
    if (!force && key === lastHello)
        return;
    lastHello = key;
    try {
        await magi.putDevice(req);
    }
    catch {
        lastHello = "";
    }
}
// The queue of notices to say, in order.
const toSay = [];
let saying = false;
function show(n) {
    setTray([...tray().filter((x) => x.id !== n.id), n]);
}
export function dismiss(id) {
    setTray(tray().filter((x) => x.id !== id));
    magi.ackNotice({ id, device: deviceId }).catch(() => { });
}
function deliver(n, how) {
    magi.deliverNotice({ id: n.id, device: deviceId, how }).catch(() => { });
}
function systemNotify(n) {
    if (!("Notification" in window) || Notification.permission !== "granted")
        return false;
    try {
        const sys = new Notification(n.title, { body: n.text.slice(0, 300), tag: n.id });
        sys.onclick = () => { window.focus(); sys.close(); };
        return true;
    }
    catch {
        return false;
    }
}
function arrive(n) {
    show(n);
    const say = n.urgency !== "low" && unlocked() && active();
    if (say) {
        toSay.push(n);
        pump();
    }
    else if (document.visibilityState === "hidden" && systemNotify(n)) {
        deliver(n, "notified");
    }
    else {
        deliver(n, "shown");
    }
}
// pump says the next notice when the turn is free: at once for "high",
// else when nobody speaks.
async function pump() {
    if (saying || !toSay.length)
        return;
    const n = toSay[0];
    if (n.urgency !== "high" && (speaking() !== null || hearing())) {
        setTimeout(pump, 400);
        return;
    }
    toSay.shift();
    if (!tray().some((x) => x.id === n.id))
        return pump(); // dismissed meanwhile
    saying = true;
    if (speaking() !== null)
        interrupt("notice");
    await chime(n.urgency === "high");
    deliver(n, "spoken");
    speak({ id: n.message, text: n.text }, Date.now(), (reason) => {
        saying = false;
        // Heard to the end on a page that the person uses: done.
        if (reason === "done" && active())
            dismiss(n.id);
        pump();
    });
}
let started = false;
export async function startAttention() {
    if (started)
        return;
    started = true;
    await hello(true);
    setInterval(() => hello(true), 60_000);
    document.addEventListener("visibilitychange", () => hello());
    createRoot(() => createEffect(() => { handsFree(); hello(); }));
    // Open notices from before this page loaded.
    try {
        const open = await magi.listNotices({ open: true, limit: 20 });
        setTray(open.notices.filter((n) => !n.device || n.device === deviceId).reverse());
    }
    catch { /* the wait gets new ones */ }
    let closedAfter = 0;
    for (;;) {
        try {
            hello();
            const r = await magi.waitNotices({ device: deviceId, waitMs: 25_000, closedAfter });
            closedAfter = r.nowMs;
            if (r.closed.length)
                setTray(tray().filter((x) => !r.closed.includes(x.id)));
            for (const n of r.notices)
                arrive(n);
        }
        catch {
            await new Promise((ok) => setTimeout(ok, 5000));
            await hello(true);
        }
    }
}
// askForNotifications asks the browser for system notifications, after a
// click of the person.
export async function askForNotifications() {
    if (!("Notification" in window))
        return;
    if (Notification.permission === "default")
        await Notification.requestPermission();
    hello(true);
}
