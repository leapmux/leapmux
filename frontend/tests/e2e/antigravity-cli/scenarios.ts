import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { ProviderAgent } from '../helpers/workspace'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent, managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { writeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { exerciseCapabilityProbe } from '../helpers/unsupportedConfiguration'
import { antigravityNativeToolResult } from './toolResult'

/** How an Antigravity CLI agent opens. */
export const ANTIGRAVITY_AGENT: ProviderAgent = { provider: AgentProvider.ANTIGRAVITY_CLI, prefix: 'antigravity-e2e' }

/** Build the scenario context of Antigravity CLI. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  await fixtures.modelScript.rule({
    name: 'the Antigravity conversation title request',
    when: { system: 'conversation title generator' },
    respond: { text: 'Native conversation title' },
  })
  return managedNativeContext(fixtures, ANTIGRAVITY_AGENT, {
    readToolResult: (request, callId) => ({
      text: antigravityNativeToolResult(request, callId),
    }),
  })
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, {
    binaryName: 'agy',
    holdWhen: ['acp'],
    lazy: false,
  })
}

/** Use a native file tool to prove automatic approval in autoEdit. */
export async function exerciseAntigravityAutoEditWrite(context: ManagedNativeScenarioContext): Promise<void> {
  const agent = await currentNativeAgent(context)
  const marker = randomUUID()
  const path = join(agent.workingDir, `native-autoedit-${marker}.txt`)
  const callId = `antigravity-autoedit-${marker}`
  const content = `ANTIGRAVITY_AUTOEDIT_${marker}\n`
  expect(existsSync(path)).toBe(false)
  const answer = 'The native automatic file write completed.'
  let start: number | undefined
  await expectNoNativeControl(context, {
    relatedProof: async () => {
      start = await context.modelScript.queue({
        toolCalls: [
          writeToolCall(context.provider, callId, { path, content }),
        ],
      }, { text: answer })
      await sendMessage(context.page, context.modelScript.prompt('Create the file through the native automatic file tool.'))
      await context.modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(context.page)
      expect(readFileSync(path, 'utf8')).toBe(content)
      await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
    },
    testId: 'control-banner',
  })
  if (start === undefined)
    throw new Error('The native automatic file write queued no step.')
}

/** The related proof of a missing-setting cell: the native model answers one marked prompt. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseCapabilityProbe(context)
}
