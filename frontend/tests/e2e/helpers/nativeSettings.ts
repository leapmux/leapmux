import type { Page } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sendNativeAnswer } from './nativeConversation'
import { currentNativeAgent, expectNativeOptionValue, nativeAgentById, nativeOptionGroup, nativeOptionValue } from './nativeScenario'
import { retryUntilPass } from './retryUntilPass'
import { uniqueMarker } from './shellArguments'
import { chooseSettingsOption, expectSettingsOptionChosen, offeredSettingsOptions, waitForNativeSettingsHydrated, waitForSettingsIdle } from './ui'

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
  return retryUntilPass(async () => {
    const current = await nativeAgentById(context, id)
    if (!current)
      throw new Error(`The Worker holds no native agent ${id}.`)
    expect(current.status, `the native agent ${id} is active`).toBe(AgentStatus.ACTIVE)
    expect(current.agentSessionId, `the native agent ${id} has a native session`).not.toBe('')
    expect(nativeOptionValue(current, groupId), `the Worker applied ${groupId}=${value}`).toBe(value)
    return current
  })
}

/** Restore one selected setting and prove its next actual native request. */
export async function exerciseRestoredNativeOption(context: ManagedNativeScenarioContext, options: NativeOptionProof): Promise<void> {
  await context.page.reload()
  await waitForNativeSettingsHydrated(context.page)
  await expectSettingsOptionChosen(context.page, `${options.groupId}-${options.value}`)
  await expectNativeOptionValue(context, options.groupId, options.value)
  const next = await sendNativeAnswer(context, 'Reply once after restoring the selected native setting.', `The restored native setting reached the next turn: ${uniqueMarker('RESTORED')}.`)
  await options.nativeProof(next)
}

/**
 * Run a prepare step of an option scenario on the live catalog of the active agent.
 *
 * The wait for the live catalog comes first. Before the agent runs, the Worker offers a read-only model group, so
 * `waitForSettingsHydrated` can end before the live catalog arrives. A prepare step that reads or changes a setting
 * before that arrival acts on the wrong options. A settings menu that it opens also keeps the list that it showed when
 * it opened, until it closes. The wait comes again after the prepare step, because a setting that the step changes can
 * restart the agent.
 */
async function prepareOnLiveCatalog(page: Page, prepare: (() => Promise<void>) | undefined): Promise<void> {
  await waitForNativeSettingsHydrated(page)
  if (prepare === undefined)
    return
  await prepare()
  await waitForNativeSettingsHydrated(page)
}

/** Prove one offered setting through its provider-owned native request fields. */
export async function exerciseNativeOption(
  context: ManagedNativeScenarioContext,
  options: NativeOptionProof & {
    /** Runs on the live catalog, before the choice. See {@link prepareOnLiveCatalog}. */
    prepare?: () => Promise<void>
  },
): Promise<void> {
  await prepareOnLiveCatalog(context.page, options.prepare)
  const before = await currentNativeAgent(context)
  const group = nativeOptionGroup(before, options.groupId)
  expect(group?.mutable, `the live catalog lets the user change ${options.groupId}`).toBe(true)
  expect(group?.options.map(option => option.id), `the live catalog offers ${options.groupId}=${options.value}`).toContain(options.value)
  const optionId = `${options.groupId}-${options.value}`
  await chooseSettingsOption(context.page, optionId)
  await waitForSettingsIdle(context.page)
  await expectSettingsOptionChosen(context.page, optionId)
  const first = await sendNativeAnswer(context, 'Reply once with the selected native setting.', `The selected native setting reached this turn: ${uniqueMarker('SELECTED')}.`)
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
    /** Runs on the live catalog, before the choices. See {@link prepareOnLiveCatalog}. */
    prepare?: () => Promise<void>
  },
): Promise<void> {
  await prepareOnLiveCatalog(context.page, options.prepare)
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
  const first = await sendNativeAnswer(context, 'Reply once after the model switch.', `The kept setting reached the new model: ${uniqueMarker('KEPT')}.`)
  await options.nativeProof(first)
  await exerciseRestoredNativeOption(context, { groupId: options.kept.groupId, value: options.kept.value, nativeProof: options.nativeProof })
}

/**
 * How one step of {@link exerciseNativeOptionSequence} reaches its value:
 * - `default`: the session starts with the value, so the step changes nothing.
 * - `choose`: the step chooses the value in the settings menu.
 * - `reload`: the step reloads the page, so the value comes from the stored state.
 */
export interface NativeOptionStep {
  value: string
  via: 'default' | 'choose' | 'reload'
}

/**
 * Drive one option group through a sequence of values, and prove each step in its next actual native request.
 * Each step requires its value as the chosen option before its turn. The proof receives the step and its index, so a
 * proof can compare a request with an earlier one.
 */
export async function exerciseNativeOptionSequence(
  context: ManagedNativeScenarioContext,
  options: {
    groupId: string
    steps: readonly NativeOptionStep[]
    nativeProof: (request: MockModelRequestRecord, step: NativeOptionStep, index: number) => void | Promise<void>
  },
): Promise<void> {
  if (options.steps.length === 0)
    throw new Error('A native option sequence needs at least one step.')
  // Each answer is unique to its sequence, so a later sequence of the same test cannot match an earlier bubble.
  const marker = uniqueMarker('OPTIONSTEP')
  await waitForNativeSettingsHydrated(context.page)
  for (const [index, step] of options.steps.entries()) {
    const optionId = `${options.groupId}-${step.value}`
    if (step.via === 'reload') {
      await context.page.reload()
      await waitForNativeSettingsHydrated(context.page)
    }
    else if (step.via === 'choose') {
      await chooseSettingsOption(context.page, optionId)
      await waitForSettingsIdle(context.page)
    }
    await expectSettingsOptionChosen(context.page, optionId)
    const request = await sendNativeAnswer(context, `Reply once at ${options.groupId} step ${index}.`, `The ${options.groupId} step ${index} answered: ${marker}.`)
    await options.nativeProof(request, step, index)
  }
}

/**
 * Require the settings menu of `groupId` to offer exactly `values`, in any order.
 * Use it where a contract or the provider states the set. A value that the provider adds later fails the check, which
 * is the purpose of a menu check.
 */
export async function expectSettingsOptionsOffered(page: Page, groupId: string, values: readonly string[]): Promise<void> {
  if (values.length === 0)
    throw new Error(`The ${groupId} menu check needs at least one value.`)
  const offered = await offeredSettingsOptions(page, groupId)
  expect([...offered].sort(), `the values that the ${groupId} menu offers`).toEqual([...values].sort())
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
  const build = await sendNativeAnswer(context, 'Reply once before the native Plan settings change.', `The build turn answered: ${uniqueMarker('BUILD')}.`)
  await options.nativeBuildProof(build)
  await chooseSettingsOption(context.page, `${options.effort.groupId}-${options.effort.value}`)
  await chooseSettingsOption(context.page, `${options.mode.groupId}-${options.mode.value}`)
  await waitForSettingsIdle(context.page)
  await expectSettingsOptionChosen(context.page, `${options.effort.groupId}-${options.effort.value}`)
  await expectSettingsOptionChosen(context.page, `${options.mode.groupId}-${options.mode.value}`)
  const selected = await sendNativeAnswer(context, 'Reply once after the native Plan settings change.', `The Plan settings applied: ${uniqueMarker('PLAN')}.`)
  await options.nativePlanProof(selected)
  await exerciseRestoredNativeOption(context, { ...options[options.restore], nativeProof: options.nativePlanProof })
  await expectSettingsOptionChosen(context.page, `${options.effort.groupId}-${options.effort.value}`)
  await expectSettingsOptionChosen(context.page, `${options.mode.groupId}-${options.mode.value}`)
}
