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
import { chooseSettingsOption, closeComposerMenus, expectSettingsOptionChosen, offeredSettingsOptions, openPlusMenu, settingsGroupTrigger, waitForNativeSettingsHydrated, waitForSettingsIdle } from './ui'

interface NativeOptionProof {
  groupId: string
  value: string
  nativeProof: (request: MockModelRequestRecord) => void | Promise<void>
}

/**
 * Wait for an active Worker agent that satisfies `check` and holds a native session.
 * Return the agent that satisfies both conditions.
 * A provider can restart its process to apply a setting.
 * A restart before the first user message opens a new session, because an empty session has nothing to resume.
 */
async function waitForNativeAgentState(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>, check: (agent: AgentInfo) => void): Promise<AgentInfo> {
  const { id } = await currentNativeAgent(context)
  return retryUntilPass(async () => {
    const current = await nativeAgentById(context, id)
    if (!current)
      throw new Error(`The Worker holds no native agent ${id}.`)
    expect(current.status, `the native agent ${id} is active`).toBe(AgentStatus.ACTIVE)
    expect(current.agentSessionId, `the native agent ${id} has a native session`).not.toBe('')
    check(current)
    return current
  })
}

/**
 * Wait for the Worker to confirm one option on its active agent, then return that agent.
 * The settings chip changes before the Worker applies the value, so the chip cannot prove the applied state.
 */
export async function waitForNativeOptionApplied(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>, groupId: string, value: string): Promise<AgentInfo> {
  return waitForNativeAgentState(context, (agent) => {
    expect(nativeOptionValue(agent, groupId), `the Worker applied ${groupId}=${value}`).toBe(value)
  })
}

/** Choose `model` in the settings menu and wait until the Worker applies it. A model that the agent runs already stays. */
async function chooseNativeModel(context: ManagedNativeScenarioContext, model: string): Promise<void> {
  if (nativeOptionValue(await currentNativeAgent(context), 'model') === model)
    return
  const optionId = `model-${model}`
  await chooseSettingsOption(context.page, optionId)
  await waitForSettingsIdle(context.page)
  await expectSettingsOptionChosen(context.page, optionId)
  await waitForNativeOptionApplied(context, 'model', model)
}

/** The effort chip of the status bar. It carries one test ID whatever the effort group of the provider is. */
const EFFORT_CHIP_SELECTOR = '[data-testid="composer-effort-trigger"]'

/**
 * Switch to a model with no effort levels, then require the absence of both effort controls.
 * The Worker must run that model, and its live catalog must offer no level of `effortGroupId`.
 * The plus menu must offer no submenu for that group.
 * The status bar must show no effort chip.
 * The browser derives effort groups from the selected model before the Worker answers.
 * Thus the browser alone cannot prove the native model or its levels.
 */
export async function expectEffortHiddenForModel(
  context: ManagedNativeScenarioContext,
  options: {
    /** The option value of the model without an effort level. */
    model: string
    /** The effort group of the provider, such as `effort` or `reasoning_effort`. */
    effortGroupId: string
  },
): Promise<void> {
  const { page } = context
  const optionId = `model-${options.model}`
  await chooseSettingsOption(page, optionId)
  await waitForSettingsIdle(page)
  await expectSettingsOptionChosen(page, optionId)
  await waitForNativeAgentState(context, (agent) => {
    expect(nativeOptionValue(agent, 'model'), `the Worker runs ${options.model}`).toBe(options.model)
    const levels = nativeOptionGroup(agent, options.effortGroupId)?.options.map(option => option.id) ?? []
    expect(levels, `the live catalog offers no ${options.effortGroupId} level for ${options.model}`).toEqual([])
  })
  await openPlusMenu(page)
  await expect.poll(() => settingsGroupTrigger(page, options.effortGroupId).count(), `the plus menu offers no ${options.effortGroupId} submenu`).toBe(0)
  await closeComposerMenus(page)
  await expect.poll(() => page.locator(EFFORT_CHIP_SELECTOR).count(), 'the status bar shows no effort chip').toBe(0)
}

/**
 * Prove the settled effort after a trip through a model that lacks the selected level.
 * Choose `chosen` on `model`, switch to `via`, then return to `model`.
 * Wait for the Worker after each step, and require these states:
 * - On `via`, either both effort controls hide, or the menu offers exactly `viaEfforts`.
 * - Back on `model`, both catalogs offer the same levels as before the trip.
 *   A difference between the static fallback and the live catalog fails this check.
 * - Back on `model`, the selection equals `settled`, which states the provider's outcome.
 *   The next native request must use that level.
 */
export async function exerciseEffortModelRoundTrip(
  context: ManagedNativeScenarioContext,
  options: {
    /** The effort group of the provider, such as `effort` or `reasoning_effort`. */
    effortGroupId: string
    /** The option value of the model that the trip starts and ends on. */
    model: string
    /** The level that the user chooses on `model` before the trip. */
    chosen: string
    /** The option value of the model of the trip, which must lack `chosen`. */
    via: string
    /** `hidden` for a model that offers no level, or the exact levels of the menu of `via`, in any order. */
    viaEfforts: 'hidden' | readonly string[]
    /** The level that `model` holds after the trip. */
    settled: string
    /** Check that the next native request runs on `model` at `settled`. */
    nativeProof: (request: MockModelRequestRecord) => void | Promise<void>
    /** Runs on the live catalog, before the trip. See {@link prepareOnLiveCatalog}. */
    prepare?: () => Promise<void>
  },
): Promise<void> {
  const { page } = context
  const group = options.effortGroupId
  if (options.model === options.via)
    throw new Error('An effort round trip needs a second model.')
  if (options.viaEfforts !== 'hidden' && options.viaEfforts.includes(options.chosen))
    throw new Error(`The model of an effort round trip must lack the chosen level ${options.chosen}.`)
  const levelsOf = (agent: AgentInfo): string[] => nativeOptionGroup(agent, group)?.options.map(option => option.id) ?? []
  const sorted = (levels: readonly string[]): string[] => [...levels].sort()
  await prepareOnLiveCatalog(page, options.prepare)
  await chooseNativeModel(context, options.model)
  const chosenId = `${group}-${options.chosen}`
  await chooseSettingsOption(page, chosenId)
  await waitForSettingsIdle(page)
  await expectSettingsOptionChosen(page, chosenId)
  const before = levelsOf(await waitForNativeOptionApplied(context, group, options.chosen))
  // The browser draws the catalog that the Worker sends, so its menu follows the Worker's levels.
  const menu = await retryUntilPass(async () => {
    const offered = await offeredSettingsOptions(page, group)
    expect(sorted(offered), `the ${group} menu of ${options.model}`).toEqual(sorted(before))
    return offered
  })

  if (options.viaEfforts === 'hidden') {
    await expectEffortHiddenForModel(context, { model: options.via, effortGroupId: group })
  }
  else {
    const viaEfforts = sorted(options.viaEfforts)
    await chooseNativeModel(context, options.via)
    await waitForNativeAgentState(context, (agent) => {
      expect(sorted(levelsOf(agent)), `the live catalog offers the ${group} levels of ${options.via}`).toEqual(viaEfforts)
    })
    await retryUntilPass(async () => {
      expect(sorted(await offeredSettingsOptions(page, group)), `the ${group} menu of ${options.via}`).toEqual(viaEfforts)
    })
  }

  await chooseNativeModel(context, options.model)
  const after = await waitForNativeAgentState(context, (agent) => {
    expect(nativeOptionValue(agent, group), `the Worker settles ${group} on ${options.model} after the trip`).toBe(options.settled)
  })
  expect(levelsOf(after), `the live catalog offers the ${group} levels of ${options.model} again`).toEqual(before)
  await retryUntilPass(async () => {
    expect(await offeredSettingsOptions(page, group), `the ${group} menu of ${options.model} after the trip`).toEqual(menu)
  })
  await expectSettingsOptionChosen(page, `${group}-${options.settled}`)
  const request = await sendNativeAnswer(context, 'Reply once after the model round trip.', `The settled effort reached the next turn: ${uniqueMarker('ROUNDTRIP')}.`)
  await options.nativeProof(request)
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
 * Run an option scenario's prepare step after the active agent supplies its live catalog.
 * Before startup ends, the Worker offers a read-only model group.
 * `waitForSettingsHydrated` can return before the live catalog arrives.
 * A prepare step that reads or changes a setting at that point uses the wrong options.
 * A menu keeps the options that it shows until it closes.
 * Wait again after the prepare step, because a setting that the step changes can restart the agent.
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
 * The browser sends only the model that the user changes.
 * Require the selected setting on screen and in the Worker row.
 * Prove the setting in the next native request, then repeat the proof after reload.
 * Choose a `kept` value that differs from the new model's default.
 * Otherwise a native reset to that default would pass the same checks.
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
 * Choose Auto and require the level that the agent reports, on screen and in the next native request.
 * Auto sends no level, so the agent chooses one.
 * A provider that reports that level replaces Auto in the menu and stores the level in the Worker row.
 * `runs` specifies that level, and the proof checks it in the next request.
 */
export async function exerciseAutomaticEffort(
  context: ManagedNativeScenarioContext,
  options: {
    /** The effort group of the provider, such as `effort` or `reasoning_effort`. */
    effortGroupId: string
    /** The level that the agent chooses for the automatic effort. */
    runs: string
    /** Check that the next native request runs at `runs`. */
    nativeProof: (request: MockModelRequestRecord) => void | Promise<void>
  },
): Promise<void> {
  if (options.runs === 'auto')
    throw new Error('An automatic effort check needs the concrete level that the agent runs.')
  const { page } = context
  const group = options.effortGroupId
  await waitForNativeSettingsHydrated(page)
  await chooseSettingsOption(page, `${group}-auto`)
  await waitForSettingsIdle(page)
  await waitForNativeOptionApplied(context, group, options.runs)
  await expectSettingsOptionChosen(page, `${group}-${options.runs}`)
  const request = await sendNativeAnswer(context, 'Reply once at the automatic effort.', `The automatic effort reached the next turn: ${uniqueMarker('AUTOMATIC')}.`)
  await options.nativeProof(request)
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
