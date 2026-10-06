import { randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { createNativePermissionFileWrite, exerciseNativePermissionRefusal, exerciseNativePermissionWrite, expectDeclinedToolRow } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { COPILOT_USER_REJECTION, copilotToolCompletion } from './permissionRefusal'

copilotTest('permission-prompts: places queued guidance in the next native model request', async ({ native }) => {
  await exerciseSteerBeforeTool(native, { approveTool: true })
})

copilotTest('keeps actual file bytes unchanged until the native Allow decision', async ({ native }) => {
  await exerciseNativePermissionWrite(native)
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
    viewProof: () => expectDeclinedToolRow(native.page, callId),
  })
})
