import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { createNativePermissionFileWrite, exerciseNativePermissionReason, exerciseNativePermissionRefusal, exerciseNativePermissionWrite, exerciseRememberedAllow, expectDeclinedToolRow, expectSavedRefusalFeedback } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { writeToolCall } from '../helpers/providerToolCalls'
import { savedControlAnswer } from '../helpers/ui'
import { COPILOT_USER_REJECTION, copilotToolCompletion } from './permissionRefusal'

copilotTest('keeps actual file bytes unchanged until the native Allow decision', async ({ native }) => {
  await exerciseNativePermissionWrite(native, {
    // The saved row reads the option that the decision selected.
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Allow once'),
  })
})

/**
 * Deny sends the native decision `{kind: "reject"}` with no feedback.
 * The Copilot runtime then ends the turn and sends no further model request.
 * The Copilot SDK traffic snapshot of a rejected permission holds one model request only.
 * Thus the test queues only the turn that asks for the tool, and reads the refusal from the stored native completion.
 */
copilotTest('keeps exact file bytes after a native Deny decision', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const fileName = 'native-denied-write.txt'
  const file = join(agent.workingDir, fileName)
  const initialContent = `KEEP_THE_NATIVE_FILE_${randomUUID()}\n`
  const callId = 'copilot-denied-write'
  const operation = await createNativePermissionFileWrite(native, { fileName, callId, outputPrefix: 'UNAPPROVED_WRITE', initialContent })
  await exerciseNativePermissionRefusal(native, {
    toolCall: operation.toolCall,
    prompt: 'Run the scripted permission probe.',
    expectUnchanged: () => expect(readFileSync(file, 'utf8')).toBe(initialContent),
    nativeRefusal: (snapshot) => {
      const completion = copilotToolCompletion(snapshot, callId)
      expect(completion.success).toBe(false)
      expect(completion.error?.message).toMatch(COPILOT_USER_REJECTION)
      // The structured code, not the message, states the refusal.
      expect(completion.error?.code).toBe('rejected')
    },
    viewProof: async () => {
      await expectDeclinedToolRow(native.page, callId)
      await expect(savedControlAnswer(native.page)).toHaveText('Reject')
    },
  })
})

// The reason rides in the `feedback` of Copilot's own rejection, and the runtime hands it to the model.
copilotTest('hands the reader\'s typed refusal reason to the model', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const initialContent = `KEEP_THE_NATIVE_FILE_${randomUUID()}\n`
  const file = join(agent.workingDir, 'native-reason-write.txt')
  const operation = await createNativePermissionFileWrite(native, { fileName: 'native-reason-write.txt', callId: 'copilot-reason-write', outputPrefix: 'UNAPPROVED_WRITE', initialContent })
  await exerciseNativePermissionReason(native, {
    toolCall: operation.toolCall,
    route: 'native-reply',
    expectNotRun: () => expect(readFileSync(file, 'utf8')).toBe(initialContent),
    viewProof: reason => expectSavedRefusalFeedback(native.page, reason),
  })
})

// A write request that can carry a session rule offers the Session scope. The Worker sends `approve-for-session` with
// a write rule, so a later write in the session runs with no request.
copilotTest('a session answer covers a later write in the next turn', async ({ native }) => {
  const agent = await currentNativeAgent(native)
  const first = join(agent.workingDir, 'native-session-first.txt')
  const second = join(agent.workingDir, 'native-session-second.txt')
  await exerciseRememberedAllow(native, {
    scope: 'Session',
    firstCall: writeToolCall(native.provider, 'copilot-session-first', { path: first, content: 'FIRST_SESSION_WRITE\n' }),
    secondCall: writeToolCall(native.provider, 'copilot-session-second', { path: second, content: 'SECOND_SESSION_WRITE\n' }),
    beforeDecision: () => {
      expect(existsSync(first)).toBe(false)
      expect(existsSync(second)).toBe(false)
    },
    firstProof: () => expect(readFileSync(first, 'utf8')).toBe('FIRST_SESSION_WRITE\n'),
    secondProof: () => expect(readFileSync(second, 'utf8')).toBe('SECOND_SESSION_WRITE\n'),
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Allow for this session'),
  })
})
