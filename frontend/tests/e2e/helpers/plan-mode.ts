import type { Locator, Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import { join } from 'node:path'
// A RELATIVE import, not `~/...`. See the note in `../agentSettings.ts`.
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { enterPlanModeToolCall, exitPlanModeToolCall, writeToolCall } from './providerToolCalls'
import { getGlobalState } from './server'
import { sendMessage, waitForAgentIdle, waitForControlBanner } from './ui'

// ──────────────────────────────────────────────
// Plan mode helpers
// ──────────────────────────────────────────────
//
// Each step scripts the tool call it needs, so the agent makes it exactly once
// and exactly when this helper says. The earlier version sent a prompt and
// re-sent it up to three times, because a real model skipped `ExitPlanMode`
// often enough to fail 033, 050 and 051 in one run. Nothing here retries now:
// a step that does not land is a defect in the app, not in the model's mood.

const PLAN_BODY = 'This is a dummy plan for testing the coding agent plan mode UI. Never execute this plan.'

/** The prompt that accompanies the scripted `EnterPlanMode` call. */
export const ENTER_PLAN_PROMPT = 'I am testing the coding agent plan mode UI. Please enter plan mode.'

/** The prompt that accompanies the scripted `ExitPlanMode` call. */
export const EXIT_PLAN_PROMPT = 'Please use ExitPlanMode tool to exit plan mode. Do not do anything else.'

/** The plan text the exit call raises for approval. `testId` keeps two runs apart. */
export function planText(testId?: string): string {
  return `# Dummy plan${testId ? ` ${testId}` : ''}\n\n${PLAN_BODY}`
}

/**
 * The path a plan file must take for the worker to recognize it.
 *
 * `providers/claude/output.go` tracks a `Write` or `Edit` whose `file_path` sits under
 * `<HOME>/.claude/plans/`, and reads the plan title and body straight out of
 * that tool input. A plan written anywhere else records no plan file, so the
 * agent-info card shows no plan row and the tab keeps its default name.
 */
function planFilePath(testId?: string): string {
  const home = getGlobalState().agentEnv.HOME
  if (!home)
    throw new Error('The run state carries no agent HOME, so no plan path can be built')
  return join(home, '.claude', 'plans', `dummy-plan${testId ? `-${testId}` : ''}.md`)
}

/**
 * Enter plan mode and write the plan file.
 *
 * Two turns, because the write must happen INSIDE plan mode: the provider
 * auto-approves `EnterPlanMode`, and a write to the plans directory is what
 * plan mode permits. The mode chip is the app's own confirmation that the first
 * one landed.
 */
export async function enterPlanMode(
  page: Page,
  script: ModelScript,
  options: { testId?: string, provider?: AgentProvider } = {},
): Promise<void> {
  const provider = options.provider ?? AgentProvider.CLAUDE_CODE
  await planFallback(script)
  await script.queue(
    { toolCalls: [enterPlanModeToolCall(provider, 'enter-plan')] },
    { toolCalls: [writeToolCall(provider, 'write-plan', { path: planFilePath(options.testId), content: script.prompt(planText(options.testId)) })] },
    { text: 'I am in plan mode and the plan is written.' },
  )
  await sendMessage(page, script.prompt(ENTER_PLAN_PROMPT))
  await script.waitForSteps()
  await waitForAgentIdle(page)
}

/**
 * Answer every plan-mode turn beyond the scripted ones.
 *
 * Plan mode drives the agent past what a caller can count: entering it starts a
 * mode the provider keeps working in, and approving, rejecting or commenting on
 * the plan each starts more turns. None of these specifications asserts HOW
 * MANY turns happen — they assert what the app draws — so the count is not
 * theirs to fix.
 */
function planFallback(script: ModelScript): Promise<void> {
  return script.fallback({ text: 'Working through the plan.' })
}

/**
 * Leave plan mode, and return the control-request banner it raises.
 *
 * The turn stops at the request: the provider waits for the answer, so no
 * further model request follows until the test approves or rejects.
 */
export async function exitPlanMode(
  page: Page,
  script: ModelScript,
  options: { testId?: string, provider?: AgentProvider } = {},
): Promise<Locator> {
  const provider = options.provider ?? AgentProvider.CLAUDE_CODE
  await planFallback(script)
  // The plan carries the scenario marker. An APPROVED plan restarts the agent
  // on a fresh session seeded from the plan, so the original user message — and
  // the marker with it — is gone from that history. Without the marker there,
  // the restarted agent's turns reach the ambient scenario and are refused.
  await script.queue({ toolCalls: [exitPlanModeToolCall(provider, 'exit-plan', script.prompt(planText(options.testId)))] })
  await sendMessage(page, script.prompt(EXIT_PLAN_PROMPT))
  await script.waitForSteps()
  return waitForControlBanner(page)
}

/**
 * Enter plan mode and leave it again, returning the banner the exit raises.
 *
 * `testId` is an optional unique ID embedded in the plan title, so two runs in
 * one transcript name different plans (e.g. "first" → "Dummy plan first").
 */
export async function enterAndExitPlanMode(page: Page, script: ModelScript, testId?: string): Promise<Locator> {
  const options = testId === undefined ? {} : { testId }
  await enterPlanMode(page, script, options)
  return exitPlanMode(page, script, options)
}

export { PLAN_BODY }
