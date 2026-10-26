import { createHash } from 'node:crypto';
import { readFileSync, existsSync, mkdirSync, renameSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(fileURLToPath(import.meta.url));
const pin = JSON.parse(readFileSync(join(root, 'model.json')));
const dir = resolve(root, '../.cache/minilm');
mkdirSync(dir, {recursive: true});
function verify(data, kind, hash, size) {
  if (data.length !== size) throw Error(`Wrong ${kind} size`);
  const h = createHash(kind === 'tokenizer' ? 'sha1' : 'sha256');
  if (kind === 'tokenizer') h.update(`blob ${data.length}\0`);
  if (h.update(data).digest('hex') !== hash) throw Error(`Wrong ${kind} hash`);
}
for (const [kind, file, target, hash, size] of [
  ['model', pin.file, 'model.onnx', pin.sha256, pin.size],
  ['tokenizer', 'tokenizer.json', 'tokenizer.json', pin.tokenizer_git_blob, pin.tokenizer_size],
]) {
  const destination = join(dir, target);
  if (existsSync(destination)) {
    verify(readFileSync(destination), kind, hash, size);
    continue;
  }
  const url = `https://huggingface.co/${pin.repository}/resolve/${pin.revision}/${file}`;
  const response = await fetch(url, {signal: AbortSignal.timeout(120000)});
  if (!response.ok) throw Error(`${kind} download failed: ${response.status}`);
  const chunks = []; let length = 0;
  for await (const chunk of response.body) {
    length += chunk.length;
    if (length > size) throw Error(`${kind} exceeds pinned size`);
    chunks.push(chunk);
  }
  const data = Buffer.concat(chunks);
  verify(data, kind, hash, size);
  const partial = `${destination}.${process.pid}.part`;
  writeFileSync(partial, data, {flag: 'wx'});
  renameSync(partial, destination);
}
console.log(`Verified local embedding model: ${pin.repository}@${pin.revision}`);
