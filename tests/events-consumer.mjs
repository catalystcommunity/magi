// An independent SQLite consumer fixture. It does not use the Go helpers or Magi.
import { DatabaseSync } from 'node:sqlite';
import { chmodSync } from 'node:fs';
import { createHash,randomUUID } from 'node:crypto';
import * as w from '../.build/events/codec.gen.js';
export function consumerStore(path,application) {
 const db=new DatabaseSync(path);chmodSync(path,0o600);
 db.exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
 CREATE TABLE IF NOT EXISTS inbox(sender TEXT NOT NULL,id TEXT NOT NULL,body BLOB NOT NULL,receipt BLOB NOT NULL,PRIMARY KEY(sender,id));
 CREATE TABLE IF NOT EXISTS effects(sender TEXT NOT NULL,id TEXT NOT NULL,PRIMARY KEY(sender,id));`);
 return {close:()=>db.close(),accept(sender,d){
  if(d.recipient.domain!==application.identity.domain||d.recipient.accountId!==application.identity.accountId)throw Error('recipient');
  const source=JSON.stringify(sender),body=w.toDeliveryCbor(d);
  db.exec('BEGIN IMMEDIATE');try{
   let row=db.prepare('SELECT body,receipt FROM inbox WHERE sender=? AND id=?').get(source,d.deliveryId);
   if(row){if(!Buffer.from(row.body).equals(body))throw Error('conflict');}
   else{const event=w.fromEventCbor(d.event);const receipt=w.toDeliveryReceiptCbor({deliveryId:d.deliveryId,event:event.ref,recipient:application.identity,digest:createHash('sha256').update(d.event).digest(),acceptedAt:new Date(),receipt:{authority:application.identity,kind:'delivery-receipt',recordId:randomUUID()}});db.prepare('INSERT INTO inbox VALUES(?,?,?,?)').run(source,d.deliveryId,body,receipt);row={receipt};}
   db.exec('COMMIT');return w.fromDeliveryReceiptCbor(row.receipt);
  }catch(e){db.exec('ROLLBACK');throw e;}
 },apply(sender,d){
  const source=JSON.stringify(sender);db.exec('BEGIN IMMEDIATE');try{
   if(!db.prepare('SELECT 1 FROM inbox WHERE sender=? AND id=?').get(source,d.deliveryId))throw Error('not accepted');
   const result=db.prepare('INSERT OR IGNORE INTO effects VALUES(?,?)').run(source,d.deliveryId);db.exec('COMMIT');return result.changes;
  }catch(e){db.exec('ROLLBACK');throw e;}
 }};
}
