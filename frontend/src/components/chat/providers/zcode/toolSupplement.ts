import { ZCODE_SUPPLEMENT } from '~/generated/contracts/zcode-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'

/**
 * The two payloads a retained tool row keeps beside its `tool.updated` envelope.
 *
 * This is the browser's half of `zcodeToolResultEnvelope` in the worker: the worker
 * reads ZCode's own store for a call whose result never reached the stream, and stores
 * the native record with the artifact bodies it refers to. The key names are contract
 * constants (contracts/zcode-protocol.json) and the Go tags are pinned to the same
 * table by TestSupplementTagsMatchTheContract, so a rename cannot leave one language
 * writing a key the other stopped reading.
 */
export interface ZCodeToolSupplement {
  /** ZCode's own record of the call, as its store holds it. */
  nativeTool: Record<string, unknown> | undefined
  /** Every artifact body the record refers to, by artifact URI. */
  artifacts: Record<string, unknown> | undefined
}

/** The stored payloads, or a pair of undefined for a row that carries none. */
export function zcodeToolSupplement(supplemental: unknown): ZCodeToolSupplement {
  const supplement = isObject(supplemental) ? supplemental : undefined
  return {
    nativeTool: pickObject(supplement, ZCODE_SUPPLEMENT.NativeTool, undefined),
    artifacts: pickObject(supplement, ZCODE_SUPPLEMENT.Artifacts, undefined),
  }
}
