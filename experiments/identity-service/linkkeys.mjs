// Use the pinned sibling SDK. No copied cryptographic implementation.
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
export const checkout = resolve(import.meta.dirname, '../../../linkkeys');
const base = pathToFileURL(`${import.meta.dirname}/.sdk/`);
const mod = path => import(new URL(path.replace(/\.ts$/, '.js'), base));
export const codec = await mod('generated/codec.gen.ts');
export const sdk = await mod('src/index.ts');
export const rpc = await mod('src/rpc.ts');
export const carrier = await mod('src/vendor/csilgen-transport/index.ts');
export const tuple = await mod('src/cborTuple.ts');
export const keys = await mod('src/applicationKeys.ts');
export const keyClient = await mod('src/applicationKeyClient.ts');
export const socket = await mod('src/socket.ts');
export function transport(config) { return new rpc.PinnedRpcTransport({...config, requestTimeoutMs: 8000}); }
export async function call(t, service, operation, type, request, responseType) {
  return codec[`from${responseType}Cbor`](await t.call(service, operation, codec[`to${type}Cbor`](request)));
}
