/**
 * Worker Noise public-key material shared by transport open, TOFU pin resolve,
 * and ChannelSession handshake. One type so the three seams cannot drift.
 */
export interface WorkerKeyBundle {
  x25519PublicKey: Uint8Array
  mlkemPublicKey: Uint8Array
  slhdsaPublicKey: Uint8Array
}
