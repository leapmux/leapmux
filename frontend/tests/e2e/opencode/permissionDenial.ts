import type { MockModelToolCall } from '../helpers/mockModelScript'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody } from '../helpers/nativeMessages'
import { exerciseNativePermissionRefusal, expectDeclinedToolRow } from '../helpers/nativePermission'
import { savedControlAnswer } from '../helpers/ui'

/**
 * The error that OpenCode and Kilo store for a tool call whose permission the reader rejected.
 * Both checkouts state it in `packages/core/src/v1/permission.ts`.
 */
export const OPENCODE_PERMISSION_REJECTION = 'The user rejected permission to use this specific tool call.'

/** The final native frame of one OpenCode-family tool call. */
export interface OpenCodeToolEnding {
  status: 'completed' | 'failed' | 'cancelled'
  /** The text blocks of the frame content, in order. */
  text: string[]
  /** The error of the raw output, when the frame has one. */
  error?: string
}

/** Read the one final native frame of a call in the native session of the snapshot. */
export function openCodeToolEnding(snapshot: NativeMessageSnapshot, callId: string): OpenCodeToolEnding {
  if (!callId || snapshot.agentId.trim() === '' || snapshot.agentSessionId.trim() === '')
    throw new Error('The native tool ending requires an exact agent, session, and call ID.')
  const endings: OpenCodeToolEnding[] = []
  for (const message of snapshot.messages) {
    if (message.agentSessionId !== snapshot.agentSessionId || message.spanId !== callId)
      continue
    const frame = nativeMessageBody(message)
    if (!isObject(frame))
      throw new Error('The paired native tool frame must contain an object.')
    if (frame.sessionUpdate !== 'tool_call_update')
      continue
    if (frame.toolCallId !== callId)
      throw new Error('The paired native tool frame identifies another call.')
    const status = frame.status
    if (status !== 'completed' && status !== 'failed' && status !== 'cancelled')
      continue
    const content: unknown[] = Array.isArray(frame.content) ? frame.content : []
    const text = content.flatMap(block => isObject(block) && block.type === 'content' && isObject(block.content)
      && block.content.type === 'text' && typeof block.content.text === 'string'
      ? [block.content.text]
      : [])
    const raw = isObject(frame.rawOutput) ? frame.rawOutput : undefined
    endings.push({ status, text, ...(typeof raw?.error === 'string' ? { error: raw.error } : {}) })
  }
  const ending = endings[0]
  if (endings.length !== 1 || !ending)
    throw new Error(`The native call ${callId} has ${endings.length} final frames. Exactly one is required.`)
  return ending
}

/**
 * Deny one native permission and prove the native refusal of OpenCode or Kilo.
 *
 * The runtime ends the turn after the refusal and sends no further model request.
 * The session processor sets `blocked = shouldBreak` for a rejected permission.
 * `shouldBreak` is true unless the config sets `experimental.continue_loop_on_deny`, and LeapMux never sets it.
 */
export async function exerciseOpenCodeFamilyDenial(context: ManagedNativeScenarioContext, options: {
  toolCall: MockModelToolCall
  prompt: string
  /** Text that identifies the operation in the native permission banner. */
  bannerText?: string
  /** Prove the exact file state. The scenario calls it before the decision, after it, and after a reload. */
  expectUnchanged: () => void
}): Promise<void> {
  await exerciseNativePermissionRefusal(context, {
    ...options,
    nativeRefusal: (snapshot) => {
      expect(openCodeToolEnding(snapshot, options.toolCall.id)).toEqual({
        status: 'failed',
        text: [OPENCODE_PERMISSION_REJECTION],
        error: OPENCODE_PERMISSION_REJECTION,
      })
    },
    viewProof: async () => {
      await expect(savedControlAnswer(context.page)).toHaveText('Reject')
      await expectDeclinedToolRow(context.page, options.toolCall.id, OPENCODE_PERMISSION_REJECTION)
    },
  })
}
