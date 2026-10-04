import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from './nativeConversation'
import { currentNativeAgent } from './nativeScenario'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from './ui'

interface NativeOptionProof {
  groupId: string
  value: string
  nativeProof: (request: MockModelRequestRecord) => void | Promise<void>
}

/** Restore one selected setting and prove its next actual native request. */
export async function exerciseRestoredNativeOption(context: ManagedNativeScenarioContext, options: NativeOptionProof): Promise<void> {
  await context.page.reload()
  await waitForSettingsHydrated(context.page)
  await expectSettingsOptionChosen(context.page, `${options.groupId}-${options.value}`)
  const restored = await currentNativeAgent(context)
  expect(restored.optionGroups.find(candidate => candidate.id === options.groupId)?.currentValue).toBe(options.value)
  const next = await sendNativeAnswer(context, 'Reply once after restoring the selected native setting.', 'The restored native setting reached the next turn.')
  await options.nativeProof(next)
}

/** Prove one offered setting through its provider-owned native request fields. */
export async function exerciseNativeOption(
  context: ManagedNativeScenarioContext,
  options: NativeOptionProof & {
    prepare?: () => Promise<void>
  },
): Promise<void> {
  await options.prepare?.()
  await waitForSettingsHydrated(context.page)
  const before = await currentNativeAgent(context)
  const group = before.optionGroups.find(candidate => candidate.id === options.groupId)
  expect(group?.mutable).toBe(true)
  expect(group?.options.map(option => option.id)).toContain(options.value)
  const optionId = `${options.groupId}-${options.value}`
  await chooseSettingsOption(context.page, optionId)
  await waitForSettingsIdle(context.page)
  await expectSettingsOptionChosen(context.page, optionId)
  const first = await sendNativeAnswer(context, 'Reply once with the selected native setting.', 'The selected native setting reached this turn.')
  await options.nativeProof(first)
  await exerciseRestoredNativeOption(context, options)
}

/** Keep a coupled mode and effort while the provider checks its actual native requests. */
export async function exerciseNativePlanWithEffort(
  context: ManagedNativeScenarioContext,
  options: {
    mode: { groupId: string, value: string }
    effort: { groupId: string, value: string }
    restore: 'mode' | 'effort'
    nativeBuildProof: (request: MockModelRequestRecord) => void | Promise<void>
    nativePlanProof: (request: MockModelRequestRecord) => void | Promise<void>
  },
): Promise<void> {
  const build = await sendNativeAnswer(context, 'Reply once before the native Plan settings change.', 'Settings applied.')
  await options.nativeBuildProof(build)
  await chooseSettingsOption(context.page, `${options.effort.groupId}-${options.effort.value}`)
  await chooseSettingsOption(context.page, `${options.mode.groupId}-${options.mode.value}`)
  await waitForSettingsIdle(context.page)
  await expectSettingsOptionChosen(context.page, `${options.effort.groupId}-${options.effort.value}`)
  await expectSettingsOptionChosen(context.page, `${options.mode.groupId}-${options.mode.value}`)
  const selected = await sendNativeAnswer(context, 'Reply once after the native Plan settings change.', 'Settings applied.')
  await options.nativePlanProof(selected)
  await exerciseRestoredNativeOption(context, { ...options[options.restore], nativeProof: options.nativePlanProof })
  await expectSettingsOptionChosen(context.page, `${options.effort.groupId}-${options.effort.value}`)
  await expectSettingsOptionChosen(context.page, `${options.mode.groupId}-${options.mode.value}`)
}
