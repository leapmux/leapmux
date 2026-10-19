import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { KIMI_BLOCKED_PROMPT_MARKER, KIMI_BLOCKED_PROMPT_REASON } from '../helpers/kimiEnvironment'
import { newNativeWorkingDir, openNativeAgent } from '../helpers/nativeAgentOpen'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseModelRetry } from '../helpers/nativeModelRetry'
import { currentNativeAgent, managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kimiModelContextText } from './modelContextText'
import { kimiModelTurns } from './modelTurns'

/** How a Kimi Code agent opens. */
export const KIMI_AGENT: ProviderAgent = { provider: AgentProvider.KIMI_CODE, prefix: 'kimi-e2e' }

/**
 * Build the scenario context of Kimi Code, with every field that its native protocol needs.
 * The generic model-context reader reads the JSON body, where a quote arrives escaped, so the context reads the text
 * of each native message. That reader refuses a message whose content is not text.
 * The turn reader classifies the engine's own reminder rows as context, so a prompt check
 * never steps over them by hand.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, KIMI_AGENT, { readModelContext: kimiModelContextText, readConversationTurns: kimiModelTurns })
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'kimi', holdWhen: ['web'], lazy: false })
}

/** The related proof of a missing-setting cell: a real native shell command runs, and its output returns. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseShellToolExecution(context, { includeFailure: false })
}

/** Verify Kimi's retained failed attempt through the shared retry scenario. */
export async function exerciseKimiModelRetry(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseModelRetry(context, {
    errors: [{ status: 500, message: 'The stream failed.', midStream: true }],
    answer: 'RETRIED-ANSWER',
    failedAttempt: 'interrupted',
  })
}

/** A blocked prompt starts no turn, and its reply releases the queue for the next prompt. */
export async function exerciseKimiBlockedPrompt(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  await sendMessage(page, modelScript.prompt(`${KIMI_BLOCKED_PROMPT_MARKER} Do not run this prompt.`))
  const detail = `Hook UserPromptSubmit blocked: ${KIMI_BLOCKED_PROMPT_REASON}`
  await expect(messageContents(page).filter({ hasText: detail })).toHaveCount(1)
  await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(0)
  await waitForAgentIdle(page)
  await sendNativeAnswer(context, 'Run the prompt after the hook refusal.', 'The next prompt completed.')
  await page.reload()
  await waitForAgentIdle(page)
  await expect(messageContents(page).filter({ hasText: detail })).toHaveCount(1)
}

/** Start a real profile after its instruction file exceeds Kimi's recommended size. */
export async function exerciseKimiInstructionWarning(context: ManagedNativeScenarioContext): Promise<void> {
  const workingDir = newNativeWorkingDir(context, 'kimi-large-instructions-')
  const instructions = 'Keep changes in this workspace.\n'.repeat(1500).slice(0, 44 * 1024)
  writeFileSync(join(workingDir, 'AGENTS.md'), instructions)
  await openNativeAgent(context, { workingDir })
  await currentNativeAgent(context)
  const warning = () => messageContents(context.page).filter({ hasText: 'AGENTS.md total' })
  await expect(warning()).toHaveCount(1)
  await sendNativeAnswer(context, 'Confirm the profile warning does not block this prompt.', 'The instruction profile remains usable.')
  await expect(warning()).toHaveCount(1)
  await context.page.reload()
  await waitForAgentIdle(context.page)
  await expect(warning()).toHaveCount(1)
}
