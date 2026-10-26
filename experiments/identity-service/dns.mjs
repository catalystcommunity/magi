// Disposable, authoritative test DNS. No forwarding and no public listener.
import dgram from 'node:dgram';
import { readFileSync } from 'node:fs';
const sock = dgram.createSocket('udp4');
sock.on('message', (q, peer) => {
  try {
    if (q.length < 17 || q.length > 4096 || q.readUInt16BE(4) !== 1) return;
    let i = 12; const labels = [];
    while (q[i]) { const n = q[i++]; if (n > 63 || i + n >= q.length) return; labels.push(q.subarray(i, i += n).toString('ascii')); }
    i++; const type = q.readUInt16BE(i); const end = i + 4;
    const rows = JSON.parse(readFileSync('/fixture/records.json'));
    const value = rows[labels.join('.').toLowerCase()];
    const answers = [];
    if (value && type === 16 && value.txt) {
      const parts = []; for (let p = 0; p < value.txt.length; p += 240) {
        const s = Buffer.from(value.txt.slice(p, p + 240)); parts.push(Buffer.from([s.length]), s);
      }
      answers.push(Buffer.concat(parts));
    }
    if (value && type === 1 && value.ip) answers.push(Buffer.from(value.ip.split('.').map(Number)));
    const h = Buffer.alloc(12); q.copy(h, 0, 0, 2); h.writeUInt16BE(0x8400 | (value ? 0 : 3), 2);
    h.writeUInt16BE(1, 4); h.writeUInt16BE(answers.length, 6);
    const records = answers.flatMap(body => {
      const r = Buffer.alloc(12); r.writeUInt16BE(0xc00c); r.writeUInt16BE(type, 2); r.writeUInt16BE(1, 4); r.writeUInt32BE(1, 6); r.writeUInt16BE(body.length, 10); return [r, body];
    });
    sock.send(Buffer.concat([h, q.subarray(12, end), ...records]), peer.port, peer.address);
  } catch { /* Drop malformed test queries. */ }
});
sock.bind(53, '0.0.0.0');
