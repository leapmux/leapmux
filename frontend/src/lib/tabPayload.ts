// The client half of `leapmux.v1.TabPayload`: what a FILE or IMAGE tab needs to
// resolve WHAT IT SHOWS, kept on the worker behind the E2EE channel because the
// hub must never see it.
//
// The proto's oneof is awkward to read at a call site (`payload.kind.case`,
// `payload.kind.value`), and every consumer wants the same flat, discriminated
// shape. This module is the one place that translation lives, in both
// directions, so a new tab kind is one file to edit rather than a search.

import type { MessageInitShape } from '@bufbuild/protobuf'
import type { TabPayload, TabPayloadSchema } from '~/generated/proto/leapmux/v1/worker_private_pb'

/** A decoded payload, discriminated by tab kind. */
export type TabPayloadView
  = | { kind: 'file', workingDir: string, filePath: string }
    | {
      kind: 'image'
      workingDir: string
      /** The agent whose transcript holds the message. */
      agentId: string
      /** Per-agent message seq, the same addressing the scroll rail uses. */
      seq: bigint
      /** Which image of that message, in `Provider.toolResultImages` order. */
      imageIndex: number
      /** Display name for the tab strip. */
      title: string
    }

/**
 * Decode a wire payload. Returns null for an absent payload and for one whose
 * oneof case this client does not know — a tab kind added by a newer peer, which
 * must render as "not supported" rather than as a broken FILE tab.
 */
export function tabPayloadView(payload: TabPayload | undefined): TabPayloadView | null {
  const kind = payload?.kind
  if (!kind)
    return null
  const workingDir = payload?.workingDir ?? ''
  if (kind.case === 'file')
    return { kind: 'file', workingDir, filePath: kind.value.filePath }
  if (kind.case === 'image') {
    return {
      kind: 'image',
      workingDir,
      agentId: kind.value.agentId,
      seq: kind.value.seq,
      imageIndex: kind.value.imageIndex,
      title: kind.value.title,
    }
  }
  return null
}

/** Build the FILE case for `registerTabPayload`. */
export function fileTabPayload(filePath: string, workingDir: string): MessageInitShape<typeof TabPayloadSchema> {
  return { workingDir, kind: { case: 'file', value: { filePath } } }
}

/** Build the IMAGE case for `registerTabPayload`. */
export function imageTabPayload(image: {
  agentId: string
  seq: bigint
  imageIndex: number
  title: string
  workingDir: string
}): MessageInitShape<typeof TabPayloadSchema> {
  return {
    workingDir: image.workingDir,
    kind: {
      case: 'image',
      value: {
        agentId: image.agentId,
        seq: image.seq,
        imageIndex: image.imageIndex,
        title: image.title,
      },
    },
  }
}
