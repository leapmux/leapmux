import { slh_dsa_shake_256f } from '@noble/post-quantum/slh-dsa.js'

// ---------------------------------------------------------------------------
// SLH-DSA signature worker
//
// A post-quantum channel open verifies the Worker's SLH-DSA-SHAKE-256f signature
// over the handshake transcript. The verification costs about 26 ms of pure
// JavaScript on a fast desktop, and several times that on a phone, once per
// channel open per Worker. Here it runs off the main thread.
// ---------------------------------------------------------------------------

export interface SlhDsaVerifyRequest {
  id: number
  signature: Uint8Array
  message: Uint8Array
  publicKey: Uint8Array
}

export interface SlhDsaVerifyResponse {
  id: number
  valid: boolean
}

/** Answer one request. A signature that the verifier cannot even parse is invalid. */
export function answerVerifyRequest(request: SlhDsaVerifyRequest): SlhDsaVerifyResponse {
  let valid = false
  try {
    valid = slh_dsa_shake_256f.verify(request.signature, request.message, request.publicKey)
  }
  catch {
    valid = false
  }
  return { id: request.id, valid }
}

globalThis.onmessage = (event: MessageEvent<SlhDsaVerifyRequest>) => {
  globalThis.postMessage(answerVerifyRequest(event.data))
}
