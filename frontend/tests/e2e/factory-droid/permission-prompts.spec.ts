import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { droidTest, expect } from '../droid-fixtures'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { expectTurnEndedAfter } from '../helpers/nativeStoredControlDecision'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { editToolCall } from '../helpers/providerToolCalls'
import { answerControl, assistantBubbles, enterControlFeedback, expectNoControlBanner, savedControlAnswer, sendMessage, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { nativeContext } from './scenarios'
import { nativeDroidCallId } from './toolResult'

droidTest.describe('Factory Droid control requests', () => {
  const PROVIDER = AgentProvider.DROID

  droidTest('runs a command after the reader allows it', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
    const note = join(askingDroidWorkspace.workingDir, 'notes.txt')
    writeFileSync(note, 'a')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDroidWorkspace.workspaceId })
    // Normal autonomy asks before the native Edit tool.
    // It permits the native Execute tool. Use Edit to test an actual approval.
    await exerciseNativePermissionDecision(context, {
      toolCall: editToolCall(context.provider, 'allow-call', { path: 'notes.txt', before: 'a', after: 'b' }),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('Edit')
        expect(readFileSync(note, 'utf8')).toBe('a')
      },
      nativeProof: (request) => {
        expect(readFileSync(note, 'utf8')).toBe('b')
        const result = nativeToolResult(request, nativeDroidCallId(request, 'Edit', 'allow-call'))
        expect(result).not.toMatch(/error|denied/i)
      },
      viewProof: async () => {
        await expectNoControlBanner(page)
        // The saved row reads Droid's own `proceed_once` reply as the button's word.
        await expect(savedControlAnswer(page)).toHaveText('Allow')
      },
    })
  })

  droidTest('keeps the command from running after the reader denies it', async ({ askingDroidWorkspace, page, modelScript }) => {
    const note = join(askingDroidWorkspace.workingDir, 'notes.txt')
    writeFileSync(note, 'a')
    // Cancellation ends this turn without another model request.
    // This script needs only the initial tool step.
    const start = await modelScript.queue(
      { toolCalls: [editToolCall(PROVIDER, 'deny-call', { path: 'notes.txt', before: 'a', after: 'b' })] },
    )
    await sendMessage(page, modelScript.prompt('Edit the note.'))
    await modelScript.waitForSteps(start + 1)

    await waitForControlBanner(page)
    expect(readFileSync(note, 'utf8')).toBe('a')
    await answerControl(page, 'deny')
    await waitForAgentIdle(page)

    await expectNoControlBanner(page)
    expect(readFileSync(note, 'utf8')).toBe('a')
    await expectTurnEndedAfter(modelScript, start + 1)
    // The saved row reads Droid's own `cancel` reply as the button's word.
    await expect(savedControlAnswer(page)).toHaveText('Deny')
  })

  // Droid's reply cannot carry a rejection reason to the model: Droid discards a
  // `comment` beside `cancel`. The reason follows as the reader's next message, which
  // opens a turn of its own.
  droidTest('sends the reason for a denial as the next message', async ({ askingDroidWorkspace, page, modelScript }) => {
    const note = join(askingDroidWorkspace.workingDir, 'notes.txt')
    writeFileSync(note, 'a')
    const reason = 'Keep the note unchanged.'
    // Cancellation ends the first turn without another model request. The reason
    // opens the second turn, which answers it.
    const start = await modelScript.queue(
      { toolCalls: [editToolCall(PROVIDER, 'reason-call', { path: 'notes.txt', before: 'a', after: 'b' })] },
      { text: 'I will leave the note as it is.' },
    )
    await sendMessage(page, modelScript.prompt('Edit the note.'))
    await modelScript.waitForSteps(start + 1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Edit')
    // The composer's send is a denial that carries the typed text as its reason.
    await enterControlFeedback(page, reason)
    await page.keyboard.press('Meta+Enter')
    await expect(banner).toHaveCount(0)

    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(readFileSync(note, 'utf8')).toBe('a')
    expect(JSON.stringify((await modelScript.requestAt(start + 1)).body)).toContain(reason)
    await expect(userBubbles(page).filter({ hasText: reason }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I will leave the note as it is.' }).first()).toBeVisible()
    // The saved row reads Droid's own `cancel` reply as the button's word. The reason
    // is the row of the next message.
    await expect(savedControlAnswer(page)).toHaveText('Deny')
  })
})
