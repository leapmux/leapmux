import type { Page } from '@playwright/test'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { droidTest, expect } from '../droid-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { editToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, savedControlAnswer, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { nativeDroidCallId } from './toolResult'

droidTest.describe('Factory Droid control requests', () => {
  function banner(page: Page) {
    return page.getByTestId('control-banner').filter({ visible: true })
  }

  const PROVIDER = AgentProvider.DROID

  droidTest('runs a command after the reader allows it', async ({ askingDroidWorkspace, page, modelScript }) => {
    const note = join(askingDroidWorkspace.workingDir, 'notes.txt')
    writeFileSync(note, 'a')
    // Normal autonomy asks before the native Edit tool.
    // It permits the native Execute tool. Use Edit to test an actual approval.
    await modelScript.queue(
      { toolCalls: [editToolCall(PROVIDER, 'allow-call', { path: 'notes.txt', before: 'a', after: 'b' })] },
      { text: 'The edit landed.' },
    )
    await sendMessage(page, modelScript.prompt('Edit the note.'))
    // The call waits on the banner, so the second step waits too.
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('Edit')
    expect(readFileSync(note, 'utf8')).toBe('a')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(banner(page)).toHaveCount(0)
    expect(readFileSync(note, 'utf8')).toBe('b')
    const followUp = status.requests.find(request => request.stepIndex === 1)
    const result = nativeToolResult(followUp, nativeDroidCallId(followUp, 'Edit', 'allow-call'))
    expect(result).not.toMatch(/error|denied/i)
    // The saved row reads Droid's own `proceed_once` reply as the button's word.
    await expect(savedControlAnswer(page)).toHaveText('Allow')
  })

  droidTest('keeps the command from running after the reader denies it', async ({ askingDroidWorkspace, page, modelScript }) => {
    const note = join(askingDroidWorkspace.workingDir, 'notes.txt')
    writeFileSync(note, 'a')
    // Cancellation ends this turn without another model request.
    // This script needs only the initial tool step.
    await modelScript.queue(
      { toolCalls: [editToolCall(PROVIDER, 'deny-call', { path: 'notes.txt', before: 'a', after: 'b' })] },
    )
    await sendMessage(page, modelScript.prompt('Edit the note.'))
    await modelScript.waitForSteps(1)

    expect(readFileSync(note, 'utf8')).toBe('a')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await waitForAgentIdle(page)

    await expect(banner(page)).toHaveCount(0)
    expect(readFileSync(note, 'utf8')).toBe('a')
    // The saved row reads Droid's own `cancel` reply as the button's word.
    await expect(savedControlAnswer(page)).toHaveText('Deny')
  })

  // Droid's reply cannot carry a rejection reason to the model: Droid discards a
  // `comment` beside `cancel`. The reason follows as the reader's next message, which
  // opens a turn of its own.
  droidTest('sends the reason for a denial as the next message', async ({ askingDroidWorkspace, page, modelScript }) => {
    const note = join(askingDroidWorkspace.workingDir, 'notes.txt')
    writeFileSync(note, 'a')
    // Cancellation ends the first turn without another model request. The reason
    // opens the second turn, which answers it.
    await modelScript.queue(
      { toolCalls: [editToolCall(PROVIDER, 'reason-call', { path: 'notes.txt', before: 'a', after: 'b' })] },
      { text: 'I will leave the note as it is.' },
    )
    await sendMessage(page, modelScript.prompt('Edit the note.'))
    await modelScript.waitForSteps(1)

    await expect(banner(page)).toContainText('Edit')
    // The composer's send is a denial that carries the typed text as its reason.
    await page.getByTestId('composer-editor').filter({ visible: true }).locator('.ProseMirror').fill('Keep the note unchanged.')
    await page.keyboard.press('Meta+Enter')
    await expect(banner(page)).toHaveCount(0)

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(readFileSync(note, 'utf8')).toBe('a')
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain('Keep the note unchanged.')
    await expect(userBubbles(page).filter({ hasText: 'Keep the note unchanged.' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I will leave the note as it is.' }).first()).toBeVisible()
    // The saved row reads Droid's own `cancel` reply as the button's word. The reason
    // is the row of the next message.
    await expect(savedControlAnswer(page)).toHaveText('Deny')
  })
})
