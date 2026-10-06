import type { Locator } from '@playwright/test'
import type { BackgroundTaskItem } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelMatcher, MockModelRequestRecord, MockModelRule, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import type { SubagentRequest } from './providerToolCalls'
import { expect } from '@playwright/test'
import { BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { escapeRegExp } from '../../../src/lib/regexp'
import { withCleanup } from './cleanup'
import { validateGateName } from './mockModelScript'
import { currentNativeAgent, nativeTextStep } from './nativeScenario'
import { readNativeSidebarSnapshot } from './nativeSidebarSnapshot'
import { spawnSubagentToolCall } from './providerToolCalls'
import { retryUntilPass } from './retryUntilPass'
import { uniqueMarker } from './shellArguments'
import { backgroundTaskRows, expandBackgroundTasksSection, expectNoRegistryRows, expectRowBecomesFinal } from './subagentRegistry'
import { sendMessage, tabById, waitForAgentIdle } from './ui'

/** The text of a held child's final answer when its script states no final step. */
export const NATIVE_CHILD_FINAL_REPLY = 'NATIVE_CHILD_FINAL_REPLY'

/** The rule ID of the tool turn of a scripted child, before the gate prefix. */
const NATIVE_CHILD_TOOL_RULE = 'the native child runs its actual tool'

/** The rule ID of the held final answer of a scripted child, before the gate prefix. */
const NATIVE_CHILD_FINAL_RULE = 'the native child holds its final reply'

/**
 * The script of the model turns of a native child.
 *
 * - Without `tool`, the first turn of the child is its final answer.
 * - With `tool`, the first turn of the child calls that tool, and the next turn is its final answer.
 *   `finalMatcher` selects that next turn when `matcher` cannot, for example when the tool result replaces the
 *   text that `matcher` reads. A `finalMatcher` without `tool` is a type error, because no second turn exists.
 *
 * The gate of the child holds the final answer, so the child keeps its running state until the caller releases it.
 */
export type NativeChildScript
  = | { matcher: MockModelMatcher, finalStep?: MockModelStep, tool?: never, finalMatcher?: never }
    | { matcher: MockModelMatcher, tool: MockModelToolCall, finalMatcher?: MockModelMatcher, finalStep?: MockModelStep }

export interface RunningChildOptions {
  /** The spawn call of the parent. */
  spawn: MockModelToolCall
  /** The gate that holds the final answer of the child. It also scopes the rule names of this child. */
  gate: string
  /**
   * The model turns of the child. Leave it out only when `rules` answer each turn of the child and hold its final
   * answer at `gate`, as the Cursor child does with a gated stream.
   */
  child?: NativeChildScript
  /**
   * The ordered turns of the parent. The default is the spawn call, then a text answer after the child reports.
   * A provider whose parent answers in the same turn as the spawn passes one step that holds both.
   */
  parentSteps?: readonly MockModelStep[]
  /** Rules that the provider owns, such as the notice of a completed child. Each rule name gets the gate prefix. */
  rules?: readonly MockModelRule[]
  /** Accept the rows of earlier children. Without it, the registry must be empty before the spawn. */
  allowExistingRows?: boolean
  /** Select the running task whose title holds this text. */
  rowText?: string
  /** Prepare the parent before the spawn, such as with a permission preset. */
  prepare?: () => Promise<void>
  /** Register report handling that the provider owns, before the held response can complete. */
  beforeRelease?: () => Promise<void>
  /** Resolve the exact native task ID from the native frames of the parent. */
  resolveTaskId?: (parentId: string) => Promise<string>
}

/** A native child that runs: its registry row, its identity, and the step that lets it finish. */
export interface RunningNativeChild {
  row: Locator
  childId: string
  parentId: string
  finish: () => Promise<void>
}

/** A running native child whose final answer the mock holds at the gate of the child. */
export interface HeldNativeChild extends RunningNativeChild {
  /**
   * Return the record of the held final request of the child. The record shows what the child sent to its model,
   * such as the result of its tool. Only a child with a `child` script has this request.
   */
  heldRequest: () => Promise<MockModelRequestRecord>
}

export interface NativeChildScriptContext {
  provider: ManagedNativeScenarioContext['provider']
  prompt: (text: string) => string
  textStep: (text: string) => MockModelStep
}

/** Build the script context of a scenario context, so a child script can be built without a browser. */
export function nativeChildScriptContext(context: NativeScenarioContext): NativeChildScriptContext {
  return {
    provider: context.provider,
    prompt: text => context.modelScript.prompt(text),
    textStep: text => nativeTextStep(context, text),
  }
}

/** The generated identity of one held child. Each call gives a new task, description, spawn call ID, and gate. */
export interface HeldChildIdentity {
  /** The task of the child, which starts with a unique marker unless the caller supplies the task. */
  task: string
  /** The task with the scenario marker, which the spawn call carries. */
  prompt: string
  /** The description of the spawn call. It is short enough for a provider label of 32 characters. */
  description: string
  spawn: MockModelToolCall
  gate: string
}

/** The start of the description of a held native child. A sidebar row of the child shows it. */
export const HELD_NATIVE_CHILD_DESCRIPTION = 'Native held child'

/**
 * Generate the identity of one held child.
 * `task` replaces the default task, and `spawn` adds the provider fields of the spawn call, such as `background`.
 */
export function heldChildIdentity(
  context: NativeChildScriptContext,
  options: { task?: string, spawn?: Omit<SubagentRequest, 'description' | 'prompt'> } = {},
): HeldChildIdentity {
  const suffix = uniqueMarker()
  const task = options.task ?? `NATIVECHILDTASK${suffix} report one word.`
  if (task.trim() === '')
    throw new Error('A held child needs a task that is not empty.')
  const description = `${HELD_NATIVE_CHILD_DESCRIPTION} ${suffix.slice(0, 8)}`
  const prompt = context.prompt(task)
  return {
    task,
    prompt,
    description,
    spawn: spawnSubagentToolCall(context.provider, `native-held-child-${suffix}`, { ...options.spawn, description, prompt }),
    gate: `native-child-${suffix}`,
  }
}

/**
 * The default options of a held child with `identity`:
 *
 * - The child answers its task at once, and the gate holds that answer.
 * - The parent calls the spawn tool, then answers once after the child reports.
 * - The registry must be empty before the spawn, and the helper selects the child by its description.
 *
 * A provider passes `overrides` for each fact that differs.
 */
export function heldChildOptions(
  context: NativeChildScriptContext,
  identity: HeldChildIdentity,
  overrides: Partial<Omit<RunningChildOptions, 'spawn' | 'gate'>> = {},
): RunningChildOptions {
  return {
    spawn: identity.spawn,
    gate: identity.gate,
    child: { matcher: { user: identity.task }, finalStep: context.textStep('NATIVECHILDCOMPLETE') },
    parentSteps: [{ toolCalls: [identity.spawn] }, context.textStep('The native parent completed.')],
    allowExistingRows: false,
    rowText: identity.description,
    ...overrides,
  }
}

/** Match a child turn whose last user text starts with `task`. */
export function childTaskAtStart(task: string): MockModelMatcher {
  return { user: `^${escapeRegExp(requireTask(task))}` }
}

/**
 * Match a child turn whose last user text holds `task` at any place.
 * Use it only for a provider that puts text of its own before the task and keeps the task out of the last user text
 * of each parent turn.
 */
export function childTaskAnywhere(task: string): MockModelMatcher {
  return { user: escapeRegExp(requireTask(task)) }
}

function requireTask(task: string): string {
  if (task.trim() === '')
    throw new Error('A child task matcher needs a task that is not empty.')
  return task
}

/** One held child of {@link openProfiledNativeChild}: its generated identity and its unique final answer. */
export interface ProfiledNativeChild extends HeldChildIdentity {
  report: string
}

/**
 * The facts of one provider that a held child, which only answers its task, needs.
 * Each provider states its profile once, in its own directory.
 */
export interface NativeChildProfile {
  /** Match the model turn of the child from its task text. The text can hold regular expression syntax. */
  childTask: (task: string) => MockModelMatcher
  /**
   * True when a spec of the provider shows that the registry row title holds the spawn description.
   * The helper then also selects the child by that title. A provider with `resolveTaskId` selects by the task ID.
   */
  rowTitleHoldsDescription: boolean
  /** Prepare the parent before the spawn, such as with a permission preset. */
  prepare?: (context: ManagedNativeScenarioContext) => Promise<void>
  /** Register report handling that the provider owns while the child is held. */
  beforeRelease?: (context: ManagedNativeScenarioContext, child: ProfiledNativeChild) => Promise<void>
  /** Resolve the exact native task ID of the child from the native frames of its parent. */
  resolveTaskId?: (context: ManagedNativeScenarioContext, parentId: string, child: ProfiledNativeChild) => Promise<string>
}

/** What a profiled child may vary: the rows of earlier children, and a tool turn before its final answer. */
export interface ProfiledChildOptions {
  allowExistingRows?: boolean
  childTool?: MockModelToolCall
}

/** Build the options of a profiled child without browser operations. */
export function profiledChildOptions(
  context: ManagedNativeScenarioContext,
  profile: NativeChildProfile,
  child: ProfiledNativeChild,
  options: ProfiledChildOptions = {},
): RunningChildOptions {
  const script = nativeChildScriptContext(context)
  const matcher = profile.childTask(child.task)
  const finalStep = script.textStep(child.report)
  const { prepare, beforeRelease, resolveTaskId } = profile
  return {
    spawn: child.spawn,
    gate: child.gate,
    child: options.childTool ? { matcher, tool: options.childTool, finalStep } : { matcher, finalStep },
    parentSteps: [{ toolCalls: [child.spawn] }, script.textStep('The native parent received its child report.')],
    allowExistingRows: options.allowExistingRows ?? false,
    ...(profile.rowTitleHoldsDescription ? { rowText: child.description } : {}),
    ...(prepare ? { prepare: () => prepare(context) } : {}),
    ...(beforeRelease ? { beforeRelease: () => beforeRelease(context, child) } : {}),
    ...(resolveTaskId ? { resolveTaskId: (parentId: string) => resolveTaskId(context, parentId, child) } : {}),
  }
}

/**
 * Spawn one uniquely identified child from the profile of its provider, and hold its final answer.
 * Each call generates a new task, description, spawn call ID, gate, and final answer.
 */
export async function openProfiledNativeChild(
  context: ManagedNativeScenarioContext,
  profile: NativeChildProfile,
  options: ProfiledChildOptions = {},
): Promise<HeldNativeChild> {
  const child: ProfiledNativeChild = { ...heldChildIdentity(nativeChildScriptContext(context)), report: uniqueMarker('NATIVECHILDREPORT') }
  return openRunningNativeChild(context, profiledChildOptions(context, profile, child, options))
}

export type NativeChildTask = Pick<BackgroundTaskItem, 'id' | 'kind' | 'status' | 'childAgentId' | 'parentAgentId' | 'title'>

export interface NativeChildTaskSelection {
  parentId: string
  rootAgentId: string
  previousChildIds: ReadonlySet<string>
  rowText?: string
  taskId?: string
}

/** Select one exact running native child from the authoritative parent snapshot. */
export function selectRunningChildTask(tasks: readonly NativeChildTask[], options: NativeChildTaskSelection): NativeChildTask | undefined {
  if (!options.parentId || !options.rootAgentId || (options.taskId !== undefined && options.taskId.trim() === ''))
    throw new Error('The native child selection requires exact parent, root, and supplied task identities.')
  const matches = tasks.filter(task => task.kind === BackgroundTaskKind.SUBAGENT
    && task.status === BackgroundTaskStatus.RUNNING && task.id.trim() !== '' && task.childAgentId.trim() !== ''
    && (task.parentAgentId === options.parentId || (task.parentAgentId === '' && options.parentId === options.rootAgentId))
    && !options.previousChildIds.has(task.childAgentId)
    && (options.taskId === undefined || task.id === options.taskId)
    && (options.taskId !== undefined || options.rowText === undefined || task.title.includes(options.rowText)))
  if (matches.length > 1)
    throw new Error('The native child selection matched more than one running task.')
  return matches[0]
}

/** Keep each native child's rules distinct within the shared model scenario. */
export function nativeChildRuleId(gate: string, ruleId: string): string {
  validateGateName(gate)
  return `[${gate}] ${ruleId}`
}

/** Build the native rules without changing their provider-owned matchers or answers. */
export function runningNativeChildRules(options: Pick<RunningChildOptions, 'gate' | 'child' | 'rules'>): MockModelRule[] {
  const scopedId = (ruleId: string) => nativeChildRuleId(options.gate, ruleId)
  const rules = (options.rules ?? []).map(rule => ({ ...rule, name: scopedId(rule.name) }))
  const child = options.child
  if (child) {
    if (child.tool)
      rules.push({ name: scopedId(NATIVE_CHILD_TOOL_RULE), when: child.matcher, respond: { toolCalls: [child.tool] }, once: true })
    rules.push({
      name: scopedId(NATIVE_CHILD_FINAL_RULE),
      when: child.finalMatcher ?? child.matcher,
      respond: { ...(child.finalStep ?? { text: NATIVE_CHILD_FINAL_REPLY }), gate: options.gate },
      once: true,
    })
  }
  return rules
}

/**
 * Return the recorded request that the final rule of the scripted child with `gate` answered.
 * Each child scopes its rule names to its gate, so the final request of another child never matches.
 */
export function heldChildFinalRequest(requests: readonly MockModelRequestRecord[], gate: string): MockModelRequestRecord {
  const finalRule = nativeChildRuleId(gate, NATIVE_CHILD_FINAL_RULE)
  const [request, ...others] = requests.filter(record => record.rule === finalRule)
  if (!request)
    throw new Error(`The held native child of gate ${gate} has no recorded final model request.`)
  if (others.length > 0)
    throw new Error(`The held native child of gate ${gate} has ${others.length + 1} final model requests, but its final rule answers once.`)
  return request
}

/** Open a real child and hold its native completion until the caller finishes its proof. */
export async function openRunningNativeChild(
  context: ManagedNativeScenarioContext,
  options: RunningChildOptions,
): Promise<HeldNativeChild> {
  if (options.parentSteps?.length === 0)
    throw new Error('The native child proof requires a parent step.')
  const rules = runningNativeChildRules(options)
  await options.prepare?.()
  const parent = await currentNativeAgent(context)
  if (!options.allowExistingRows)
    await expectNoRegistryRows(context.page, context.leapmuxServer)
  const previousChildIds = new Set((await readNativeSidebarSnapshot(context, parent.id)).backgroundTasks.map(task => task.childAgentId))
  try {
    await context.modelScript.rule(...rules)
    const parentSteps = options.parentSteps ?? [{ toolCalls: [options.spawn] }, { text: 'The native parent received its child report.' }]
    const start = await context.modelScript.queue(...parentSteps)
    await sendMessage(context.page, context.modelScript.prompt('Create the scripted native child for its capability proof.'))
    await context.modelScript.waitForGate(options.gate)
    await options.beforeRelease?.()
    const taskId = await options.resolveTaskId?.(parent.id)
    const childId = await retryUntilPass(async () => {
      const snapshot = await readNativeSidebarSnapshot(context, parent.id)
      const selected = selectRunningChildTask(snapshot.backgroundTasks, {
        parentId: parent.id,
        rootAgentId: parent.rootAgentId,
        previousChildIds,
        ...(options.rowText !== undefined ? { rowText: options.rowText } : {}),
        ...(taskId !== undefined ? { taskId } : {}),
      })
      const selectedId = selected?.childAgentId ?? ''
      expect(selectedId, 'the Worker holds the running task of the scripted native child').not.toBe('')
      return selectedId
    })
    await expandBackgroundTasksSection(context.page)
    const row = backgroundTaskRows(context.page, { kind: 'subagent', childAgentId: childId }).first()
    await expect(row).toBeVisible()
    await expect(row).toHaveAttribute('data-status', 'running')
    return {
      row,
      childId,
      parentId: parent.id,
      heldRequest: async () => {
        if (!options.child)
          throw new Error('The rules of the provider script this child, so the helper holds no final request of its own.')
        return heldChildFinalRequest((await context.modelScript.status()).requests, options.gate)
      },
      finish: async () => {
        await context.modelScript.releaseGateIfHeld(options.gate)
        await context.modelScript.waitForSteps(start + parentSteps.length)
        await tabById(context.page, parent.id).click()
        await waitForAgentIdle(context.page)
        await expectRowBecomesFinal(context.page, row)
      },
    }
  }
  catch (error) {
    try {
      await context.modelScript.releaseGateIfHeld(options.gate)
    }
    catch (cleanupError) {
      throw new AggregateError([error, cleanupError], 'The native child setup and its gate cleanup failed.')
    }
    throw error
  }
}

/**
 * Require that a held native child shows as one running subagent row of an agent of its own, then let the child finish
 * and require the completed row.
 *
 * - `rowText`: a text that the running row must hold, such as the description of the child.
 * - `reload`: also require the completed row after a reload of the page.
 *
 * The child finishes even when a check of the running row fails, so that no held request stays open after the test.
 */
export async function expectRunningChildCompletes(child: RunningNativeChild, options: { rowText?: string, reload?: boolean } = {}): Promise<void> {
  await withCleanup(async () => {
    if (options.rowText !== undefined)
      await expect(child.row).toContainText(options.rowText)
    await expect(child.row).toHaveAttribute('data-kind', 'subagent')
    await expect(child.row).toHaveAttribute('data-status', 'running')
    expect(child.childId, 'the child runs as an agent of its own').not.toBe(child.parentId)
  }, child.finish)
  await expect(child.row).toHaveAttribute('data-status', 'completed')
  if (options.reload) {
    await child.row.page().reload()
    await expect(child.row).toHaveAttribute('data-status', 'completed')
  }
}
