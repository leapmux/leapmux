import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import { Code } from '@connectrpc/connect'
import { expect } from '@playwright/test'
import { AMP_OPTION } from '../../../src/generated/contracts/amp-protocol'
import { CODEWHALE_OPTION } from '../../../src/generated/contracts/codewhale-protocol'
import { CODEX_OPTION } from '../../../src/generated/contracts/codex-protocol'
import { COPILOT_OPTION } from '../../../src/generated/contracts/copilot-protocol'
import { DIRAC_CONFIG } from '../../../src/generated/contracts/dirac-protocol'
import { GOOSE_CONFIG } from '../../../src/generated/contracts/goose-protocol'
import { GROK_OPTION } from '../../../src/generated/contracts/grok-protocol'
import { KIRO_CONFIG } from '../../../src/generated/contracts/kiro-protocol'
import { MIMO_OPTION } from '../../../src/generated/contracts/mimo-protocol'
import { REASONIX_CONFIG } from '../../../src/generated/contracts/reasonix-protocol'
import { AgentGoalAction, AgentStatus, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, UpdateAgentGoalRequestSchema, UpdateAgentGoalResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from './api'
import { expandGoalsAndTodosSection, goalAction, goalsAndTodosSection, openGoalMenu } from './goalsAndTodos'
import { sendNativeAnswer } from './nativeConversation'
import { currentNativeAgent } from './nativeScenario'
import { closeComposerMenus, expectPermissionShortcuts, openPlusMenu, waitForNativeSettingsHydrated } from './ui'

interface RelatedNativeProof {
  relatedProof: () => Promise<void>
}

/**
 * A settings feature of the feature matrix that a provider can lack.
 * Each value is a feature ID of `feature-matrix/features.json`, which is also the basename of the cell spec.
 */
export type SettingFeature = 'model' | 'reasoning-effort' | 'mode' | 'extended-thinking' | 'fast-mode' | 'output-style' | 'swarm-mode'

/**
 * Every known name of the option group of each settings feature: each group ID that a provider emits, each group
 * label that it shows, and each ID that a native agent passes through.
 *
 * The check compares names by {@link settingNameKey}, which keeps only lowercase letters and digits. So one entry
 * covers each spelling of one name: `fastMode` also covers `fast_mode` and the label "Fast Mode". An ACP agent sends
 * its own option ID and label, so a group that the check does not know by its ID still fails it by its label.
 * Each entry gives the spelling of its source, and a contract constant where a contract holds the ID.
 */
export const SETTING_GROUP_NAMES: { readonly [F in SettingFeature]: readonly string[] } = {
  // The well-known ID of every provider. The label "Model" has the same key.
  'model': ['model'],
  'reasoning-effort': [
    // The well-known ID. The default label "Effort" has the same key.
    'effort',
    // The ACP configuration ID of Dirac, Grok Build, and Qwen Code.
    DIRAC_CONFIG.ReasoningEffort,
    GOOSE_CONFIG.ThinkingEffort,
    KIRO_CONFIG.EffortLevel,
    // The effort label of Pi and Oh My Pi, whose CLIs set a thinking level.
    'Thinking Level',
  ],
  'mode': [
    // The well-known ID. Claude Code labels it "Permission Mode".
    'permissionMode',
    // The permission label of most native providers, and the permission option ID of DeepSeek Harness.
    'Permissions',
    // The ACP primary agent. Its label "Primary Agent" has the same key.
    'primaryAgent',
    // The ACP mode configuration ID, and the mode label of the ACP family, Cline, Copilot, ZCode, MiMo Code,
    // CodeWhale, Amp, and DeepSeek Harness.
    'mode',
    CODEX_OPTION.CollaborationMode,
    // The label of the Codex collaboration mode.
    'Workflow',
    // The label of the Codex approval policy, which Codex stores as `permissionMode`.
    'Approval Policy',
    AMP_OPTION.AgentMode,
    CODEWHALE_OPTION.Mode,
    // The label of the CodeWhale permission posture.
    'Permission Posture',
    COPILOT_OPTION.SessionMode,
    // The Grok Build approval ID. Oh My Pi labels its approval mode "Approval Mode", which has the same key.
    GROK_OPTION.ApprovalMode,
    // The label of the Grok Build approval mode.
    'Approvals',
    REASONIX_CONFIG.ToolApproval,
    MIMO_OPTION.PermissionPolicy,
  ],
  'extended-thinking': [
    // The Claude Code ID, labeled "Extended Thinking".
    'alwaysThinkingEnabled',
    // The Claude Code label.
    'Extended Thinking',
    // The ACP configuration ID of Kiro.
    'thinking',
    // The ACP configuration ID of Dirac.
    'thinking_budget',
    // The name that the Codex cell spec checked before.
    'thinkingEnabled',
  ],
  'fast-mode': [
    // The Claude Code ID. The label "Fast Mode" of Claude Code and Codex has the same key.
    'fastMode',
    CODEX_OPTION.ServiceTier,
    // The ACP configuration ID of Dirac.
    'inference_speed',
  ],
  // The Claude Code ID. The label "Output Style" has the same key.
  'output-style': ['outputStyle'],
  'swarm-mode': [
    // The Kimi Code ID.
    'swarmMode',
    // The Kimi Code label.
    'Swarm',
  ],
}

/** The comparison key of an option-group name: its lowercase letters and digits, in order. */
export function settingNameKey(name: string): string {
  return name.toLowerCase().replace(/[^a-z0-9]/g, '')
}

/** One option group as the Worker catalog or the settings menu states it. */
export interface SettingGroupName {
  id: string
  label: string
}

/**
 * Return a description of each group in `groups` whose ID or label is a known name of `feature`.
 * An empty result means that no group offers the feature under a known name.
 */
export function settingGroupMatches(feature: SettingFeature, groups: readonly SettingGroupName[]): string[] {
  if (!Object.hasOwn(SETTING_GROUP_NAMES, feature))
    throw new Error(`"${feature}" is not a settings feature of the feature matrix.`)
  const keys = new Set(SETTING_GROUP_NAMES[feature].map(settingNameKey))
  return groups
    .filter(group => keys.has(settingNameKey(group.id)) || keys.has(settingNameKey(group.label)))
    .map(group => `${group.id} ("${group.label}")`)
}

/** The prefix of the test ID of an option-group trigger in the `[+]` menu. */
const SETTINGS_GROUP_TRIGGER_PREFIX = 'composer-group-'

/**
 * Read the option-group triggers of the `[+]` menu with their labels, then close the menus.
 * Each trigger shows the label of its group, and its test ID holds the group ID.
 */
async function offeredSettingsGroups(page: Page): Promise<SettingGroupName[]> {
  const menu = await openPlusMenu(page)
  const triggers = await menu.locator(`[role="menuitem"][data-testid^="${SETTINGS_GROUP_TRIGGER_PREFIX}"]`)
    .evaluateAll(rows => rows.map(row => ({ testId: row.getAttribute('data-testid') ?? '', label: (row.textContent ?? '').trim() })))
  await closeComposerMenus(page)
  return triggers.map(trigger => ({ id: trigger.testId.slice(SETTINGS_GROUP_TRIGGER_PREFIX.length), label: trigger.label }))
}

/**
 * Prove that the provider offers no setting for `feature`, after a working native operation.
 *
 * The related proof runs first, so the session is live and its catalog is complete. Then the check reads the Worker
 * catalog and the `[+]` menu, before and after a reload, and refuses each group whose ID or label is a known name of
 * the feature ({@link SETTING_GROUP_NAMES}).
 */
export async function expectMissingSetting(
  context: ManagedNativeScenarioContext,
  options: RelatedNativeProof & { feature: SettingFeature },
): Promise<void> {
  await options.relatedProof()
  for (const reload of [false, true]) {
    const phase = reload ? 'after the reload' : 'before the reload'
    if (reload)
      await context.page.reload()
    await waitForNativeSettingsHydrated(context.page)
    const agent = await currentNativeAgent(context)
    expect(agent.status).toBe(AgentStatus.ACTIVE)
    expect(agent.optionGroups.length, `the native catalog has option groups ${phase}`).toBeGreaterThan(0)
    expect(settingGroupMatches(options.feature, agent.optionGroups), `the native catalog groups of ${options.feature} ${phase}`).toEqual([])
    expect(settingGroupMatches(options.feature, await offeredSettingsGroups(context.page)), `the settings menu groups of ${options.feature} ${phase}`).toEqual([])
  }
}

/**
 * The related proof of a provider whose cheapest working operation is one answered turn.
 * The native model answers a marked prompt, and the answer reaches the transcript.
 */
export async function exerciseCapabilityProbe(context: NativeScenarioContext): Promise<void> {
  await sendNativeAnswer(context, 'Complete the native setting capability probe.', 'The native setting capability probe completed.')
}

/** Check a missing permission preset after a real native permission or tool operation. */
export async function expectMissingPermissionShortcut(
  context: ManagedNativeScenarioContext,
  options: RelatedNativeProof & { preset: 'smart' | 'bypass' },
): Promise<void> {
  await options.relatedProof()
  await waitForNativeSettingsHydrated(context.page)
  const agent = await currentNativeAgent(context)
  expect(agent.status).toBe(AgentStatus.ACTIVE)
  expect(agent.optionGroups.length).toBeGreaterThan(0)
  const absent = options.preset === 'smart' ? { smart: 'absent' as const } : { bypass: 'absent' as const }
  await expectPermissionShortcuts(context.page, absent)
  await context.page.reload()
  await waitForNativeSettingsHydrated(context.page)
  await expectPermissionShortcuts(context.page, absent)
}

const GOAL_ACTION_WORDS = new Map<AgentGoalAction, 'set' | 'clear' | 'pause' | 'resume'>([
  [AgentGoalAction.SET, 'set'],
  [AgentGoalAction.CLEAR, 'clear'],
  [AgentGoalAction.PAUSE, 'pause'],
  [AgentGoalAction.RESUME, 'resume'],
])

/** Refuse only the goal actions that the actual running provider omits. */
export async function expectUnsupportedGoalActions(
  context: ManagedNativeScenarioContext,
  options: RelatedNativeProof & { actions: readonly AgentGoalAction[], objective?: string },
): Promise<void> {
  if (options.actions.length === 0)
    throw new Error('the negative goal proof requires an action')
  await options.relatedProof()
  const agent = await currentNativeAgent(context)
  expect(agent.status).toBe(AgentStatus.ACTIVE)
  const channel = await getTestChannel(context.leapmuxServer.hubUrl, context.leapmuxServer.adminToken)
  const before = await channel.callWorker(
    context.leapmuxServer.workerId,
    'ListAgentMessages',
    ListAgentMessagesRequestSchema,
    ListAgentMessagesResponseSchema,
    { agentId: agent.id, limit: 1 },
  )
  expect(before.goalLoaded).toBe(true)
  if (await goalsAndTodosSection(context.page).count() > 0)
    await expandGoalsAndTodosSection(context.page)
  const menu = context.page.locator('[data-testid="goal-actions-trigger"]:visible')
  if (await menu.count() > 0)
    await openGoalMenu(context.page)
  for (const action of options.actions) {
    const word = GOAL_ACTION_WORDS.get(action)
    if (!word)
      throw new Error('the negative goal proof received an unspecified action')
    expect(before.goalSupportedActions).not.toContain(action)
    await expect(goalAction(context.page, word)).toHaveCount(0)
    await expect(channel.callWorker(
      context.leapmuxServer.workerId,
      'UpdateAgentGoal',
      UpdateAgentGoalRequestSchema,
      UpdateAgentGoalResponseSchema,
      { agentId: agent.id, action, objective: options.objective ?? 'Keep this negative goal probe until the operator clears it.' },
    )).rejects.toMatchObject({
      source: 'rpc',
      code: Code.FailedPrecondition,
      message: 'this agent cannot perform that session-goal action',
    })
  }
  await context.page.keyboard.press('Escape')
  const after = await channel.callWorker(
    context.leapmuxServer.workerId,
    'ListAgentMessages',
    ListAgentMessagesRequestSchema,
    ListAgentMessagesResponseSchema,
    { agentId: agent.id, limit: 1 },
  )
  expect(after.goalSupportedActions).toEqual(before.goalSupportedActions)
  expect(after.goal).toEqual(before.goal)
}
