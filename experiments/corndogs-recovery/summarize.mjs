import { readFileSync } from 'node:fs';

// Print leaf test outcomes. Parent failures must not inflate the case count.
for (const file of process.argv.slice(2)) {
  const events = readFileSync(file, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse);
  const paths = new Set(events.filter(e => e.Test).map(e => e.Test));
  const counts = new Map();
  const totals = { pass: 0, fail: 0, skip: 0 };
  const suites = {};
  for (const e of events) {
    if (!e.Test || !['pass', 'fail', 'skip'].includes(e.Action)) continue;
    if ([...paths].some(p => p.startsWith(e.Test + '/'))) continue;
    const count = counts.get(e.Test) ?? { pass: 0, fail: 0, skip: 0 };
    count[e.Action]++;
    counts.set(e.Test, count);
    totals[e.Action]++;
    const suite = e.Test.split('/')[0];
    suites[suite] ??= { pass: 0, fail: 0, skip: 0 };
    suites[suite][e.Action]++;
  }
  console.log(JSON.stringify({ file, totals, suites, outcomes: Object.fromEntries(counts) }, null, 2));
}
