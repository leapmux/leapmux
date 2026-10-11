import type { Page } from '@playwright/test'
import type { RpcMark } from './helpers/timingFixture'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { NOTIFICATION_THREAD_TYPE, NOTIFICATION_TYPE } from '../../src/generated/contracts/worker-vocab'
import { isObject } from '../../src/lib/jsonPick'
import { CLAUDE_AGENT } from './claude-code/scenarios'
import { claudeTest } from './claude-fixtures'
import { installRpcListeners } from './helpers/timingFixture'
import { chooseSettingsOption, expectSettingsChip, messageBubbles, waitForSettingsHydrated, waitForSettingsIdle, workspaceRow } from './helpers/ui'
import { authenticatedAgentWorkspace } from './helpers/workspace'

const test = claudeTest.extend({
  authenticatedClaudeWorkspace: authenticatedAgentWorkspace({
    ...CLAUDE_AGENT,
    openOptions: { optionValues: { permissionMode: 'default' } },
  }),
})

async function rpcSends(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const marks = (window as Window & { __rpcMarks?: RpcMark[] }).__rpcMarks
    if (!marks)
      throw new Error('The RPC listener must exist before the Copy check.')
    return marks.filter(mark => mark.type === 'rpc-send').map(mark => mark.method)
  })
}

/** Require actual consolidated settings content and the received rendering supplement. */
function expectModeEnvelope(text: string, mode: string): void {
  const envelope: unknown = JSON.parse(text)
  if (!isObject(envelope) || !isObject(envelope.content) || !Array.isArray(envelope.content.messages))
    throw new Error('Copy must return a notification thread with its consolidated messages.')
  expect(envelope.content.type).toBe(NOTIFICATION_THREAD_TYPE)
  const settings = envelope.content.messages.filter(isObject).filter(message => message.type === NOTIFICATION_TYPE.SettingsChanged)
  expect(settings).toHaveLength(1)
  expect(settings[0]?.changes).toMatchObject({ permissionMode: { old: 'default', new: mode } })
  if (envelope.supplemental_content !== undefined) {
    expect(isObject(envelope.supplemental_content)).toBe(true)
    if (isObject(envelope.supplemental_content) && envelope.supplemental_content.metadata !== undefined)
      expect(isObject(envelope.supplemental_content.metadata)).toBe(true)
  }
  expect(envelope).not.toHaveProperty('supplemental_content.metadata.notification_entries')
  expect(envelope).not.toHaveProperty('supplemental_content.metadata.notification_reduction')
}

/** Clear prior marks and clipboard text, then exercise the actual row's Copy button. */
async function copyModeNotification(page: Page, label: string, mode: string): Promise<void> {
  const bubble = messageBubbles(page).filter({ hasText: label })
  await expect(bubble).toHaveCount(1)
  await bubble.hover()
  const sentinel = `CLIPBOARD${randomUUID()}`
  await page.evaluate(value => navigator.clipboard.writeText(value), sentinel)
  await installRpcListeners(page)
  await bubble.locator('..').getByTestId('message-copy-json').click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).not.toBe(sentinel)
  expectModeEnvelope(await page.evaluate(() => navigator.clipboard.readText()), mode)
  const sends = await rpcSends(page)
  for (const method of ['ListAgentMessages', 'GetAgentMessage', 'GetAgentSpanMessages'])
    expect(sends).not.toContain(method)
}

test.describe('notification consolidation', () => {
  test('copies native settings consolidation and received Raw JSON after reload', async ({ authenticatedClaudeWorkspace, page, modelScript }) => {
    await expect(workspaceRow(page, authenticatedClaudeWorkspace.workspaceId)).toHaveAttribute('data-active', 'true')
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')
    await installRpcListeners(page)

    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan Mode')
    await waitForSettingsIdle(page)
    await expect(messageBubbles(page).filter({ hasText: 'Mode (Default → Plan Mode)' })).toHaveCount(1)
    expect(await rpcSends(page)).toContain('UpdateAgentSettings')
    await copyModeNotification(page, 'Mode (Default → Plan Mode)', 'plan')

    // Returning to the starting mode cancels the effective change.
    await chooseSettingsOption(page, 'permissionMode-default')
    await expectSettingsChip(page, 'Default')
    await waitForSettingsIdle(page)
    await expect(messageBubbles(page).filter({ hasText: /Mode \(.*→/ })).toHaveCount(0)

    await chooseSettingsOption(page, 'permissionMode-acceptEdits')
    await expectSettingsChip(page, 'Accept Edits')
    await waitForSettingsIdle(page)
    await expect(messageBubbles(page).filter({ hasText: 'Mode (Default → Accept Edits)' })).toHaveCount(1)
    await expect(messageBubbles(page).filter({ hasText: 'Mode (Plan Mode → Accept Edits)' })).toHaveCount(0)
    expect(await rpcSends(page)).toContain('UpdateAgentSettings')
    await copyModeNotification(page, 'Mode (Default → Accept Edits)', 'acceptEdits')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Accept Edits')
    await expect(messageBubbles(page).filter({ hasText: 'Mode (Default → Accept Edits)' })).toHaveCount(1)

    // A new document needs its own real RPC positive control before Copy can prove the absence of history requests.
    await installRpcListeners(page)
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan Mode')
    await waitForSettingsIdle(page)
    await expect(messageBubbles(page).filter({ hasText: 'Mode (Default → Plan Mode)' })).toHaveCount(1)
    await chooseSettingsOption(page, 'permissionMode-acceptEdits')
    await expectSettingsChip(page, 'Accept Edits')
    await waitForSettingsIdle(page)
    await expect(messageBubbles(page).filter({ hasText: 'Mode (Default → Plan Mode)' })).toHaveCount(0)
    await expect(messageBubbles(page).filter({ hasText: 'Mode (Default → Accept Edits)' })).toHaveCount(1)
    expect(await rpcSends(page)).toContain('UpdateAgentSettings')
    await copyModeNotification(page, 'Mode (Default → Accept Edits)', 'acceptEdits')

    expect((await modelScript.status()).requests).toHaveLength(0)
  })
})
