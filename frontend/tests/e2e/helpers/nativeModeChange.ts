import type { Locator } from '@playwright/test'
import type { AgentChatMessage, AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { NOTIFICATION_FIELD, NOTIFICATION_THREAD_TYPE, NOTIFICATION_TYPE } from '../../../src/generated/contracts/worker-vocab'
import { MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { escapeRegExp } from '../../../src/lib/regexp'
import { withCleanup } from './cleanup'
import { isStoredMessageSeq, nativeMessageBody, readAllAgentMessages, sameAgentOwnership } from './nativeMessages'
import { currentNativeAgent } from './nativeScenario'
import { copySettingsNotification } from './notificationCopy'
import { thinkingIndicatorShownDuring } from './thinkingIndicatorWatch'
import { chatScrollContainer, chooseSettingsOption, expectSettingsChip, scrollChatRowIntoView, visibleOnly, waitForSettingsIdle } from './ui'

/** The initial rendered notice and a later check of its exact stored entry. */
export interface NativeModeNotice extends Locator {
  assertStored: () => Promise<void>
}

export interface NativeModeNoticeChange {
  groupId: string
  oldValue: string
  value: string
  label: string
  previousLabel: string
  noticeLabel: string
}

interface StoredModeChange extends NativeModeNoticeChange {
  sessionId: string
}

/** Count neutral settings entries. A combined thread keeps each matching entry in the count. */
function matchingStoredNotices(messages: readonly AgentChatMessage[], owner: AgentInfo, change: StoredModeChange): AgentChatMessage[] {
  const matches: AgentChatMessage[] = []
  for (const message of messages) {
    if (message.source !== MessageSource.LEAPMUX || message.agentProvider !== owner.agentProvider || message.agentSessionId !== change.sessionId)
      continue
    const body = nativeMessageBody(message)
    const entries = isObject(body) && body.type === NOTIFICATION_THREAD_TYPE && Array.isArray(body.messages) ? body.messages : [body]
    for (const entry of entries) {
      if (!isObject(entry) || entry[NOTIFICATION_FIELD.Type] !== NOTIFICATION_TYPE.SettingsChanged)
        continue
      const changes = entry[NOTIFICATION_FIELD.Changes]
      const option = isObject(changes) ? changes[change.groupId] : undefined
      if (isObject(option)
        && option[NOTIFICATION_FIELD.Old] === change.oldValue
        && option[NOTIFICATION_FIELD.New] === change.value
        && option[NOTIFICATION_FIELD.OldLabel] === change.previousLabel
        && option[NOTIFICATION_FIELD.NewLabel] === change.label
        && option[NOTIFICATION_FIELD.Label] === change.noticeLabel) {
        matches.push(message)
      }
    }
  }
  return matches
}

/** Read the selected owner's transcript while all Worker ownership fields stay unchanged. */
async function storedModeMessages(context: ManagedNativeScenarioContext, owner: AgentInfo): Promise<{ agent: AgentInfo, messages: AgentChatMessage[] }> {
  const before = await currentNativeAgent(context)
  // A provider can allocate its native session after the first notice.
  // The notice retains its captured session while the same agent continues.
  if (!sameAgentOwnership({ ...owner, agentSessionId: before.agentSessionId }, before))
    throw new Error('The selected native mode notice owner differs from the captured owner.')
  const messages = await readAllAgentMessages(context, owner.id)
  const after = await currentNativeAgent(context)
  if (!sameAgentOwnership(before, after))
    throw new Error('The native mode notice owner changed during the Worker message read.')
  return { agent: after, messages }
}

/** Select one native mode and require its single notice without a model turn. */
export async function chooseNativeModeWithNotice(
  context: ManagedNativeScenarioContext,
  change: { groupId: string, value: string, label: string, previousLabel: string, noticeLabel: string },
): Promise<NativeModeNotice> {
  const { page, modelScript } = context
  const owner = { ...await currentNativeAgent(context) }
  const group = owner.optionGroups.find(group => group.id === change.groupId)
  if (!group || group.currentValue === '')
    throw new Error(`The native mode change requires a current value for option group ${change.groupId}.`)
  const oldValue = group.currentValue
  const before = (await modelScript.status()).requests.length
  const shown = await thinkingIndicatorShownDuring(page, async () => {
    await chooseSettingsOption(page, `${change.groupId}-${change.value}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, change.label)
  })
  expect(shown).toBe(false)
  expect((await modelScript.status()).requests).toHaveLength(before)
  const text = `${change.noticeLabel} (${change.previousLabel} → ${change.label})`
  // Adjacent notification text blocks share one comma-separated paragraph.
  const notice = visibleOnly(page.getByText(new RegExp(`(?:^|,\\s*)${escapeRegExp(text)}(?=,\\s*|$)`)))
  await expect(notice).toHaveCount(1)
  return captureStoredNativeModeNotice(context, owner, { ...change, oldValue }, notice)
}

/** Capture the stored notice after the caller proves its rendered entry and native mode change. */
export async function captureStoredNativeModeNotice(
  context: ManagedNativeScenarioContext,
  ownerBefore: AgentInfo,
  change: NativeModeNoticeChange,
  locator: Locator,
): Promise<NativeModeNotice> {
  const owner = { ...ownerBefore }
  const captured = await storedModeMessages(context, owner)
  const storedChange = { ...change, sessionId: captured.agent.agentSessionId }
  const requireNotice = (messages: readonly AgentChatMessage[]) => {
    const matches = matchingStoredNotices(messages, owner, storedChange)
    if (matches.length !== 1 || !matches[0])
      throw new Error(`The native mode change requires exactly one stored native mode notice, found ${matches.length}.`)
    if (!isStoredMessageSeq(matches[0].seq))
      throw new Error('The stored mode notice must have a positive Worker sequence.')
    return matches[0]
  }
  const capturedNotice = requireNotice(captured.messages)
  const messageId = capturedNotice.id
  const assertCopy = async (message: AgentChatMessage) => {
    const scroller = chatScrollContainer(context.page)
    const position = await scroller.evaluate(element => ({ top: element.scrollTop, atEnd: element.scrollHeight - element.clientHeight - element.scrollTop < 1 }))
    await withCleanup(async () => {
      await scrollChatRowIntoView(context.page, message.seq)
      await copySettingsNotification(
        scroller.locator(`[data-seq="${message.seq}"]`).locator('[data-testid="message-bubble"]:visible'),
        { groupId: change.groupId, oldValue: change.oldValue, value: change.value },
      )
    }, () => scroller.evaluate((element, position) => {
      element.scrollTop = position.atEnd ? element.scrollHeight : position.top
    }, position))
  }
  await assertCopy(capturedNotice)
  return Object.assign(locator, {
    assertStored: async () => {
      const snapshot = await storedModeMessages(context, owner)
      const restoredNotice = requireNotice(snapshot.messages)
      if (restoredNotice.id !== messageId)
        throw new Error('The stored native mode notice differs from its captured message owner.')
      await assertCopy(restoredNotice)
    },
  })
}
