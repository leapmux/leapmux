import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { fileEditDrawsDiff } from '../../../src/components/chat/model/fileEditDiff'
import { mimoToolCall } from '../../../src/components/chat/providers/mimo/extractors/toolCall'
import { mimoToolPart } from '../../../src/components/chat/providers/mimo/extractors/toolCommon'
import { MIMO_TOOL_STATUS } from '../../../src/generated/contracts/mimo-protocol'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { exerciseFileEditSequence, PARITY_BEFORE, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { writeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, messageContents } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code tool execution', () => {
  mimoTest('reveals later native rows after an empty file write and a reload', async ({ native }) => {
    const agent = await currentNativeAgent(native)
    if (!agent.workingDir)
      throw new Error('The empty native file proof requires a private working directory.')
    const path = join(createNativeToolDirectory(agent.workingDir), 'empty-native-result.txt')
    expect(existsSync(path)).toBe(false)
    const answer = 'The actual empty native write ended.'
    await runNativeToolTurn(native, {
      toolCalls: [writeToolCall(native.provider, 'empty-native-write', { path, content: '' })],
      prompt: 'Create the scripted empty file through the native write tool.',
      answer,
    })
    expect(readFileSync(path).byteLength).toBe(0)
    const snapshot = await readNativeMessageSnapshot(native, agent.id)
    const results = snapshot.messages.flatMap((message) => {
      const part = mimoToolPart(nativeMessageBody(message))
      if (!part || part.status !== MIMO_TOOL_STATUS.Completed)
        return []
      const call = mimoToolCall({ own: part, rowFinal: true })
      if (call.kind !== 'write' || !call.request.changes.some(change => change.filePath === path))
        return []
      return [{ call, message, part }]
    })
    expect(results).toHaveLength(1)
    const result = results[0]
    if (!result || result.call.kind !== 'write')
      throw new Error('The native empty write supplied no saved result model.')
    expect(result.message.spanId).not.toBe('')
    expect(snapshot.messages.some((message) => {
      const request = mimoToolPart(nativeMessageBody(message))
      return message.spanId === result.message.spanId && message.seq < result.message.seq
        && request?.status === MIMO_TOOL_STATUS.Running && request.callId === result.part.callId
    })).toBe(true)
    expect(result.call.result).toHaveProperty('changes')
    const changes = result.call.result && 'changes' in result.call.result ? result.call.result.changes : undefined
    expect(changes?.every(change => !fileEditDrawsDiff(change))).toBe(true)
    expect(changes?.length).toBe(1)
    expect(result.call.images).toEqual([])
    expect(result.call.extraContent ?? []).toEqual([])
    expect(result.call.outputFilePaths ?? []).toEqual([])
    await expect(assistantBubbles(native.page).filter({ hasText: answer }).first()).toBeVisible()
    await expect(native.page.locator('[data-testid="result-divider"]:visible').last()).toContainText('Turn ended')
    await native.page.reload()
    await expect(assistantBubbles(native.page).filter({ hasText: answer }).first()).toBeVisible()
    await expect(native.page.locator('[data-testid="result-divider"]:visible').last()).toContainText('Turn ended')
    await sendNativeAnswer(native, 'Continue after the actual empty write.', 'The native continuation remains visible.')
  })
  // MiMo refuses an edit before the session reads the file.
  // The script creates the file. It reads the file before it edits the file.
  mimoTest('a read and an edit render the file body and the applied diff', async ({ native }) => {
    const agent = await currentNativeAgent(native)
    if (!agent.workingDir)
      throw new Error('The active native agent has no working directory.')
    // The Read is the second step of the edit sequence, so the request at `start + 2` holds the Read result.
    const start = await exerciseFileEditSequence(native, { workingDir: createNativeToolDirectory(agent.workingDir), fileName: 'parity.ts' })
    // A successful native Read adds MiMo's numbered format to the model request. The seed command contains no line number.
    // If Read fails, Edit also fails because the session did not read the file.
    const afterRead = await native.modelScript.requestAt(start + 2)
    expect(JSON.stringify(afterRead.body)).toContain(`1: ${PARITY_BEFORE}`)
    await expect(messageContents(native.page).filter({ hasText: 'has not been read' })).toHaveCount(0)
  })
})
