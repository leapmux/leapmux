import { digestFields } from './fieldDigest'

// ---------------------------------------------------------------------------
// Field digest worker
//
// Computes the retry fingerprint of an agent input off the main thread (see
// fieldDigest). An input carries up to 10 MiB of attachments, and BLAKE2b in
// JavaScript takes about 18 ms per MiB. The client limits each request with a
// time budget, and hashes on the main thread in work slices when the worker
// fails (see fieldDigestClient).
//
// This module must not import fieldDigestClient, or the worker bundle would
// hold a `new Worker(...)` of its own.
// ---------------------------------------------------------------------------

export interface FieldDigestRequest {
  id: number
  fields: Uint8Array[]
}

export interface FieldDigestResponse {
  id: number
  /** The hex digest of the request's fields (see digestFields). */
  digest: string
}

/** Answer one request, under its id. */
export function answerFieldDigestRequest(request: FieldDigestRequest): FieldDigestResponse {
  return {
    id: request.id,
    digest: digestFields(request.fields),
  }
}

globalThis.onmessage = (event: MessageEvent<FieldDigestRequest>) => {
  globalThis.postMessage(answerFieldDigestRequest(event.data))
}
