import { sign } from 'node:crypto';
import * as codec from '../.build/events/codec.gen.js';
import { EventsAsyncClient as EventsClient, DiscoveryAsyncClient as DiscoveryClient } from '../.build/events/client.async.gen.js';
import { HTTPTransport } from '../protocol/http.mjs';
import { decode } from '../protocol/profile.mjs';

export function transport(endpoint) {
  return new HTTPTransport(endpoint,{codec,responses:{
    'Discovery/describe':'ServiceDescription','Discovery/read-schema':'SchemaDocument',
    'Events/publish':'PublicationReceipt','Events/subscribe':'SubscriptionReceipt',
    'Events/pull':'DeliveryBatch','Events/acknowledge':'DeliveryReceipt',
    'Events/read-publication':'PublicationReceipt',
  }});
}
// The caller selects the receiver identity from its own trusted configuration.
// Fresh proof can carry an unchanged event from an old durable outbox.
export function signed(operation,body,{application,recipient,schema,keyId,privateKey,at=new Date()}) {
  const input=codec.toSignatureInputCbor({context:`catalyst.events/${operation}`,schema,signer:application,recipient,keyId,signedAt:at,body});
  decode('SignatureInput',input,codec);
  return {input,signature:sign(null,input,privateKey)};
}
export {codec,EventsClient,DiscoveryClient};
