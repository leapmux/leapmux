import { KIRO_META } from '~/generated/contracts/kiro-protocol'
import { pickObject } from '~/lib/jsonPick'

/**
 * The `_meta.kiro` object of one frame: a request's params, a tool call, an update or
 * a reply. Kiro states each of its own fields there. Undefined when the frame carries
 * none.
 */
export function kiroMeta(frame: Record<string, unknown> | null | undefined): Record<string, unknown> | undefined {
  return pickObject(pickObject(frame, '_meta'), KIRO_META.Namespace) ?? undefined
}

/** Native fields for tool result pointers. */
export const KIRO_OUTPUT_TRANSFORMATION = {
  Kind: 'kind',
  Path: 'absFilePath',
  TotalChars: 'totalChars',
} as const

/** Native fields for tool result pointers. */
export const KIRO_OUTPUT_TRANSFORMATION_KIND = {
  Offloaded: 'offloaded',
} as const

/** The native field that holds the output transformation. */
export const KIRO_OUTPUT_TRANSFORMATION_KEY = 'outputTransformation'
