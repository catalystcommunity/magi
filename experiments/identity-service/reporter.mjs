export default async function* report(source) {
  for await (const event of source) {
    if (['test:pass', 'test:fail'].includes(event.type)) {
      const d = event.data;
      yield JSON.stringify({event: event.type, name: d.name, nesting: d.nesting, duration_ms: d.details?.duration_ms, skip: d.skip ?? false, error: d.details?.error?.message}) + '\n';
    }
  }
}
