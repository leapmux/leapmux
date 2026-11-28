import type { MockModelToolCall } from '../helpers/mockModelScript'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { exerciseNativePermissionRefusal, expectDeclinedToolRow } from '../helpers/nativePermission'
import { nativeToolRowId } from '../helpers/nativeToolRowId'
import { savedControlAnswer } from '../helpers/ui'
import { openCodeNativeToolRecords } from './nativeToolRecords'

/** The installed native runtime's refusal message. */
export const OPENCODE_PERMISSION_REJECTION = 'The user declined this tool call'

export interface OpenCodeToolEnding {
  status: 'completed' | 'failed' | 'cancelled'
  text: string[]
  error?: string
}

/** Read one original completed native tool result. */
export function openCodeToolEnding(snapshot: NativeMessageSnapshot, callId: string): OpenCodeToolEnding {
  const endings = openCodeNativeToolRecords(snapshot, callId).filter(record => record.finished)
  if (endings.length !== 1 || !endings[0])
    throw new Error(`The native call ${callId} has ${endings.length} final frames. Exactly one is required.`)
  const { data, status } = endings[0]
  const error = pickString(pickObject(data, 'error'), 'message')
  return {
    status: status === 'completed'
      ? 'completed'
      : status === 'error'
        ? 'failed'
        : 'cancelled',
    text: Array.isArray(data.content)
      ? data.content.flatMap(item => isObject(item) && item.type === 'text' && typeof item.text === 'string'
          ? [item.text]
          : [])
      : [],
    ...(error
      ? { error }
      : {}),
  }
}

/**
 * Deny one native permission and prove the native refusal.
 * The runtime interrupts the execution after this refusal without stopping its server.
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
        text: [],
        error: OPENCODE_PERMISSION_REJECTION,
      })
    },
    viewProof: async () => {
      await expect(savedControlAnswer(context.page)).toHaveText('Deny')
      await expectDeclinedToolRow(context.page, await nativeToolRowId(context, options.toolCall.id), OPENCODE_PERMISSION_REJECTION)
    },
  })
}
