import {cpSync} from 'node:fs';
import {join} from 'node:path';

// Only application code and pinned public dependencies enter the image.
export const releaseImagePaths = Object.freeze([
  'config/deployment.mjs', 'config/deployment.example.json',
  'release', 'protocol', 'generated/releases/typescript/releases.csil-schema.cbor',
  '.build/codec.gen.js', '.build/types.gen.js', '.build/releases', '.build/release-sdk',
]);

export function copyReleaseImage(root, app) {
  for (const path of releaseImagePaths) cpSync(join(root, path), join(app, path), {recursive: true});
}
