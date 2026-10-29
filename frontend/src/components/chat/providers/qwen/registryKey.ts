import { sha256 } from '@noble/hashes/sha2.js'
import { bytesToHex } from '@noble/hashes/utils.js'
import { QWEN_REGISTRY_KEY, QWEN_TOOL } from '~/generated/contracts/qwen-protocol'

export type QwenRegistrySource = typeof QWEN_TOOL.Agent | typeof QWEN_TOOL.Workflow

const utf8 = new TextEncoder()

/** Scope a native Qwen ID using the shared length-prefixed UTF-8 hash format. */
export function qwenRegistryRowKey(source: QwenRegistrySource, sessionID: string | undefined, nativeToolCallID: string, nativeDispatchID = ''): string | undefined {
  if (!sessionID || !nativeToolCallID || (source === QWEN_TOOL.Agent && nativeDispatchID !== ''))
    return undefined
  const digest = sha256.create()
  const length = new Uint8Array(8)
  const view = new DataView(length.buffer)
  for (const component of [
    source,
    sessionID,
    nativeToolCallID,
    nativeDispatchID,
  ]) {
    const bytes = utf8.encode(component)
    view.setBigUint64(0, BigInt(bytes.length), false)
    digest.update(length)
    digest.update(bytes)
  }
  return QWEN_REGISTRY_KEY.Prefix + bytesToHex(digest.digest())
}
