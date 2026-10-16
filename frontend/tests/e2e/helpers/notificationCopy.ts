import type { Locator, Page } from '@playwright/test'
import type { RpcMark } from './timingFixture'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { NOTIFICATION_THREAD_TYPE, NOTIFICATION_TYPE } from '../../../src/generated/contracts/worker-vocab'
import { isObject } from '../../../src/lib/jsonPick'
import { installRpcListeners } from './timingFixture'

/** One setting transition that the copied notification must preserve. */
export interface SettingsNotificationChange {
  groupId: string
  oldValue: string
  value: string
}

/** Check the actual Copy envelope, including its received rendering supplement. */
export function expectSettingsNotificationEnvelope(text: string, change: SettingsNotificationChange, requireThread = false): void {
  const envelope: unknown = JSON.parse(text)
  if (!isObject(envelope) || !isObject(envelope.content))
    throw new Error('Copy must return a notification envelope with object content.')
  const content = envelope.content
  if (requireThread)
    expect(content.type, 'Copy must return a consolidated notification thread.').toBe(NOTIFICATION_THREAD_TYPE)
  const entries = content.type === NOTIFICATION_THREAD_TYPE ? content.messages : [content]
  if (!Array.isArray(entries))
    throw new Error('The copied notification thread must contain its messages.')
  const settings = entries.filter(isObject).filter(message => message.type === NOTIFICATION_TYPE.SettingsChanged)
  expect(settings, 'Copy must contain exactly one settings notification.').toHaveLength(1)
  expect(settings[0]?.changes, 'Copy must preserve the original setting transition.').toMatchObject({ [change.groupId]: { old: change.oldValue, new: change.value } })
  if (envelope.supplemental_content !== undefined) {
    expect(isObject(envelope.supplemental_content), 'The copied supplement must be an object.').toBe(true)
    if (isObject(envelope.supplemental_content) && envelope.supplemental_content.metadata !== undefined)
      expect(isObject(envelope.supplemental_content.metadata), 'The copied supplement metadata must be an object.').toBe(true)
  }
  expect(envelope, 'Copy must not synthesize notification entries.').not.toHaveProperty('supplemental_content.metadata.notification_entries')
  expect(envelope, 'Copy must not synthesize notification reduction.').not.toHaveProperty('supplemental_content.metadata.notification_reduction')
}

/** Read the RPC methods that the installed browser listener observed. */
export async function sentRpcMethods(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const marks = (window as Window & { __rpcMarks?: RpcMark[] }).__rpcMarks
    if (!marks)
      throw new Error('Install the RPC listener before reading its methods.')
    return marks.filter(mark => mark.type === 'rpc-send').map(mark => mark.method)
  })
}

/** Copy a visible settings row and require its delivered content without a history-fetch RPC. */
export async function copySettingsNotification(bubble: Locator, change: SettingsNotificationChange, requireThread = false): Promise<void> {
  const page = bubble.page()
  await expect(bubble).toHaveCount(1)
  await bubble.hover()
  const sentinel = `CLIPBOARD${randomUUID()}`
  await page.evaluate(value => navigator.clipboard.writeText(value), sentinel)
  await installRpcListeners(page)
  await bubble.locator('..').getByTestId('message-copy-json').click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).not.toBe(sentinel)
  expectSettingsNotificationEnvelope(await page.evaluate(() => navigator.clipboard.readText()), change, requireThread)
  const sends = await sentRpcMethods(page)
  for (const method of ['ListAgentMessages', 'GetAgentMessage', 'GetAgentSpanMessages'])
    expect(sends).not.toContain(method)
}
