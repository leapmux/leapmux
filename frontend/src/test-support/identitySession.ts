import type { CipherStateLike, Session } from '~/lib/noise'

/**
 * Create an identity session without storage hooks or a ChannelManager harness.
 * Encryption and decryption each return a fresh array with unchanged bytes.
 * Rekey methods do nothing. The nonce stays at zero, and needsRekey returns false.
 */
export function makeIdentitySession(): Session {
  return {
    send: createIdentityCipherState(),
    receive: createIdentityCipherState(),
  } satisfies Session
}

function createIdentityCipherState(): CipherStateLike {
  return {
    encrypt: input => new Uint8Array(input),
    decrypt: input => new Uint8Array(input),
    needsRekey: () => false,
    rekeyWithSecret: () => {},
    clearPrev: () => {},
    nonce: () => 0,
  } satisfies CipherStateLike
}
