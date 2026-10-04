import type { Page } from '@playwright/test'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from '../droid-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { editToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeDroidCallId } from './toolResult'

droidTest.describe('Factory Droid control requests', () => {
  droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

  function banner(page: Page) {
    return page.getByTestId('control-banner').filter({ visible: true })
  }

  const PROVIDER = AgentProvider.DROID

  droidTest('runs a command after the reader allows it', async ({ askingDroidWorkspace, page, modelScript }) => {
    const note = join(askingDroidWorkspace.workingDir, 'notes.txt')
    writeFileSync(note, 'a')
    // Normal autonomy asks before the native Edit tool.
    // It permits the native Execute tool. Use Edit to test an actual approval.
    await modelScript.rule(DROID_TITLE_RULE)
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
    await waitForAgentIdle(page, 180_000)
    await expect(banner(page)).toHaveCount(0)
    expect(readFileSync(note, 'utf8')).toBe('b')
    const followUp = status.requests.find(request => request.stepIndex === 1)
    const result = nativeToolResult(followUp, nativeDroidCallId(followUp, 'Edit', 'allow-call'))
    expect(result).not.toMatch(/error|denied/i)
  })

  droidTest('keeps the command from running after the reader denies it', async ({ askingDroidWorkspace, page, modelScript }) => {
    const note = join(askingDroidWorkspace.workingDir, 'notes.txt')
    writeFileSync(note, 'a')
    await modelScript.rule(DROID_TITLE_RULE)
    // Cancellation ends this turn without another model request.
    // This script needs only the initial tool step.
    await modelScript.queue(
      { toolCalls: [editToolCall(PROVIDER, 'deny-call', { path: 'notes.txt', before: 'a', after: 'b' })] },
    )
    await sendMessage(page, modelScript.prompt('Edit the note.'))
    await modelScript.waitForSteps(1)

    expect(readFileSync(note, 'utf8')).toBe('a')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await waitForAgentIdle(page, 180_000)

    await expect(banner(page)).toHaveCount(0)
    expect(readFileSync(note, 'utf8')).toBe('a')
  })
})
