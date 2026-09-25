import type { MockModelRequestRecord } from './helpers/mockModelScript'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { KIRO_OPTION, KIRO_POLICY_PRESET } from '../../src/generated/contracts/kiro-protocol'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { KIRO_MOCK_MODELS } from './helpers/kiroSurface'
import { bashToolCall, kiroSwitchToExecutionToolCall } from './helpers/providerToolCalls'
import {
  applyPermissionPreset,
  assistantBubbles,
  chooseSettingsOption,
  closeComposerMenus,
  expectNoSettingsChip,
  expectSettingsChip,
  expectSettingsOptionChosen,
  messageBubbles,
  openPlusMenu,
  openWorkspace,
  sendMessage,
  waitForAgentIdle,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'
import { expect, KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from './kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

const [EFFORT_MODEL, PLAIN_MODEL] = KIRO_MOCK_MODELS

/** The body of the last model request that a step of this script answered. */
function lastStepBody(requests: MockModelRequestRecord[]): Record<string, unknown> {
  const last = requests.filter(request => request.stepIndex !== undefined).at(-1)
  if (!last || typeof last.body !== 'object' || last.body === null)
    throw new Error('no scripted step answered a model request')
  return { ...last.body }
}

/** The model a Kiro turn states, in its current message. */
function requestModel(body: Record<string, unknown>): unknown {
  const state = body.conversationState as { currentMessage?: { userInputMessage?: { modelId?: unknown } } } | undefined
  return state?.currentMessage?.userInputMessage?.modelId
}

/**
 * 229 -- Kiro settings.
 *
 * Each setting is read off the next model request, which is where Kiro applies it:
 * the model id, the effort and the mode. The policy preset is LeapMux's own option,
 * because Kiro reads it when a session opens and never reports it.
 */
kiroTest.describe('Kiro settings', () => {
  kiroTest('switches the effort, the mode and the model for the next prompt, and keeps them after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')
    // The open request stated the effort, which differs from the model's own default.
    await expectSettingsChip(page, 'Medium')

    await chooseSettingsOption(page, 'effortLevel-high')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'High')
    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue({ text: 'SETTINGS_APPLIED' })
    await sendMessage(page, modelScript.prompt('Describe the plan in one word.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    const planned = lastStepBody((await modelScript.status()).requests)
    expect(planned.agentMode).toBe('plan')
    expect(planned.additionalModelRequestFields).toEqual({ output_config: { effort: 'high' } })
    expect(requestModel(planned)).toBe(EFFORT_MODEL!.modelId)

    // A model with no effort axis drops the axis, and its turns state no effort.
    await chooseSettingsOption(page, `model-${PLAIN_MODEL!.modelId}`)
    await waitForSettingsIdle(page)
    await expectNoSettingsChip(page, 'High')
    await modelScript.queue({ text: 'MODEL_SWITCHED' })
    await sendMessage(page, modelScript.prompt('Answer with the other model.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    const switched = lastStepBody((await modelScript.status()).requests)
    expect(requestModel(switched)).toBe(PLAIN_MODEL!.modelId)
    expect(switched).not.toHaveProperty('additionalModelRequestFields')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsOptionChosen(page, `model-${PLAIN_MODEL!.modelId}`)
    await expectNoSettingsChip(page, 'High')
  })

  // Kiro has no preset between its own rules and every call, so Smart has no match.
  // Bypass states the allow-all preset, which Kiro reads when the session opens
  // again, and a write then runs without a request.
  kiroTest('bypass runs a write without asking', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, `${KIRO_OPTION.PolicyPreset}-ask`)

    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await closeComposerMenus(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsOptionChosen(page, `${KIRO_OPTION.PolicyPreset}-${KIRO_POLICY_PRESET.AllowAll}`)

    const written = join(workingDir, 'bypass.txt')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.KIRO, 'kiro-bypass', `printf bypass > ${written}`)] },
      { text: 'WROTE_WITHOUT_ASKING' },
    )
    await sendMessage(page, modelScript.prompt('Write the scripted file.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    expect(existsSync(written)).toBe(true)
    await expect(assistantBubbles(page).filter({ hasText: 'WROTE_WITHOUT_ASKING' })).toBeVisible()
  })

  // Kiro's plan mode ends with `switch_to_execution`, which raises no approval. The
  // plan mode answers the call's result, and Kiro then hands the plan to its default
  // mode in the same turn, with no update between the two answers. The mode chip
  // follows, and each answer is a message of its own.
  kiroTest('leaves plan mode through the plan switch', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { permissionMode: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue(
      { toolCalls: [kiroSwitchToExecutionToolCall('kiro-plan', '1. Write the parser.\n2. Test it.')] },
      { text: 'PLAN_HANDED_OFF' },
      { text: 'EXECUTING_THE_PLAN' },
    )
    await sendMessage(page, modelScript.prompt('Finish planning and start.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expectSettingsChip(page, 'Default')
    const requests = (await modelScript.status()).requests
    expect(JSON.stringify(requests.find(request => request.stepIndex === 1)?.body), 'the plan mode answers the switch').toContain('"agentMode":"plan"')
    expect(JSON.stringify(requests.find(request => request.stepIndex === 2)?.body), 'the default mode runs the plan').toContain('"agentMode":"vibe"')
    // The call's row states the plan. A result row beside its call row draws no
    // tool-message wrapper of its own, so the row is found by its bubble.
    await expect(messageBubbles(page).filter({ hasText: 'Switch to execution' }).first()).toBeVisible()
    await expect(messageBubbles(page).filter({ hasText: 'Write the parser.' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'PLAN_HANDED_OFF' })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: 'EXECUTING_THE_PLAN' })).toHaveCount(1)
    const joined = assistantBubbles(page).filter({ hasText: 'PLAN_HANDED_OFF' }).filter({ hasText: 'EXECUTING_THE_PLAN' })
    await expect(joined, 'the two answers are two messages').toHaveCount(0)
  })
})
