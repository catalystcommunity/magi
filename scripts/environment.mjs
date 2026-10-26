// New names take precedence, including an explicitly empty value.
export function environment(name, values = process.env) {
  if (!/^MAGI_[A-Z0-9_]+$/.test(name)) throw Error('Expected a MAGI_ environment setting');
  return values[name] ?? values[name.replace(/^MAGI_/, 'OPAL_')];
}
