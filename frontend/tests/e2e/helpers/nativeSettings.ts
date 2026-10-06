import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sendNativeAnswer } from './nativeConversation'
import { currentNativeAgent, expectNativeOptionValue, nativeAgentById, nativeOptionGroup, nativeOptionValue } from './nativeScenario'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForNativeSettingsHydrated, waitForSettingsIdle } from './ui'

interface NativeOptionProof {
  groupId: string
  value: string
  nativeProof: (request: MockModelRequestRecord) => void | Promise<void>
}

/**
 * Wait until the owning Worker reports one applied option value on its active agent, then return that agent.
 * A provider can restart its native process to apply a setting. Before the first user message, that restart
 * opens a new native session, because an empty session has nothing to resume.
 * The settings chip changes before the Worker applies the value, so the chip does not prove the applied state.
 */
export async function waitForNativeOptionApplied(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>, groupId: string, value: string): Promise<AgentInfo> {
  const { id } = await currentNativeAgent(context)
  await expect.poll(async () => {
    const current = await nativeAgentById(context, id)
    return current?.status === AgentStatus.ACTIVE && current.agentSessionId !== ''
      && nativeOptionValue(current, groupId) === value
  }).toBe(true)
  const applied = await nativeAgentById(context, id)
  if (!applied)
    throw new Error(`The native agent ${id} disappeared after it applied ${groupId}=${value}.`)
  return applied
}

/** Restore one selected setting and prove its next actual native request. */
export async function exerciseRestoredNativeOption(context: ManagedNativeScenarioContext, options: NativeOptionProof): Promise<void> {
  await context.page.reload()
  await waitForNativeSettingsHydrated(context.page)
  await expectSettingsOptionChosen(context.page, `${options.groupId}-${options.value}`)
  await expectNativeOptionValue(context, options.groupId, options.value)
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
  await waitForNativeSettingsHydrated(context.page)
  const before = await currentNativeAgent(context)
  const group = nativeOptionGroup(before, options.groupId)
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

/**
 * Prove that a model switch keeps a setting that the new model offers.
 *
 * The user changes the model alone, so the browser sends the model and nothing else. The setting must still
 * hold on screen, in the Worker row, and in the next native request, and it must survive a reload. Pick a
 * `kept` value that differs from the value that the new model starts with: a native server that resets the
 * setting on a model change then fails this proof, and a value equal to that start value proves nothing.
 */
export async function exerciseModelSwitchKeepsOption(
  context: ManagedNativeScenarioContext,
  options: {
    /** The setting that the user chooses before the switch. */
    kept: { groupId: string, value: string }
    /** The option value of the model to switch to. */
    model: string
    /** Check that the next native request carries the new model and the kept setting. */
    nativeProof: (request: MockModelRequestRecord) => void | Promise<void>
    prepare?: () => Promise<void>
  },
): Promise<void> {
  await options.prepare?.()
  await waitForNativeSettingsHydrated(context.page)
  const keptId = `${options.kept.groupId}-${options.kept.value}`
  const modelId = `model-${options.model}`
  await chooseSettingsOption(context.page, keptId)
  await waitForSettingsIdle(context.page)
  await expectSettingsOptionChosen(context.page, keptId)
  await chooseSettingsOption(context.page, modelId)
  await waitForSettingsIdle(context.page)
  await expectSettingsOptionChosen(context.page, modelId)
  await expectSettingsOptionChosen(context.page, keptId)
  await waitForNativeOptionApplied(context, options.kept.groupId, options.kept.value)
  const first = await sendNativeAnswer(context, 'Reply once after the model switch.', 'The kept setting reached the new model.')
  await options.nativeProof(first)
  await exerciseRestoredNativeOption(context, { groupId: options.kept.groupId, value: options.kept.value, nativeProof: options.nativeProof })
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
