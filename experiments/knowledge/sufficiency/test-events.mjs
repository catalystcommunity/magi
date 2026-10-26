// Go can split a test log line across events and interleave different tests.
export function collectLines(onLine) {
  const pending = new Map();
  return event => {
    if (!event.Output) return;
    const key = `${event.Package || ''}/${event.Test || ''}`;
    let text = (pending.get(key) || '') + event.Output;
    if (text.length > 1_048_576) throw Error('Oversized test output line');
    let end;
    while ((end = text.indexOf('\n')) >= 0) {
      onLine(text.slice(0, end));
      text = text.slice(end + 1);
    }
    pending.set(key, text);
  };
}
