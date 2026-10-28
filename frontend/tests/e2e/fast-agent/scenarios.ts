import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { HeldNativeChild, NativeChildScriptContext, RunningChildOptions } from '../helpers/runningChildProof'
import type { ProviderAgent } from '../helpers/workspace'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { currentNativeAgent, managedNativeContext, nativeOptionGroup, nativeOptionValue } from '../helpers/nativeScenario'
import { waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { heldChildIdentity, heldChildOptions, nativeChildScriptContext, openRunningNativeChild } from '../helpers/runningChildProof'
import { uniqueMarker } from '../helpers/shellArguments'
import { chooseSettingsOption } from '../helpers/ui'
import { exerciseCapabilityProbe } from '../helpers/unsupportedConfiguration'

/** How a Fast Agent agent opens. */
export const FAST_AGENT_AGENT: ProviderAgent = { provider: AgentProvider.FAST_AGENT, prefix: 'fastagent-e2e' }

/** Build the scenario context of Fast Agent. Its native protocol needs no field beyond the provider. */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, FAST_AGENT_AGENT)
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'fast-agent', holdWhen: ['acp'] })
}

/**
 * Open this provider's actual child task and hold its native final answer.
 * The script comes from `runningChildOptions`, apart from this browser operation, because `scenarios.test.ts` checks
 * that script with no browser.
 */
export async function runningChild(context: ManagedNativeScenarioContext, options: { allowExistingRows?: boolean } = {}): Promise<HeldNativeChild> {
  return openRunningNativeChild(context, runningChildOptions(nativeChildScriptContext(context), options))
}

/**
 * Build the actual native child script without browser operations.
 * Fast Agent stores each child archive by its final report, so every child answers with a report of its own, and the
 * task states that report. The matcher reads the last user turn, which only the child's own turn holds.
 */
export function runningChildOptions(context: NativeChildScriptContext, options: { allowExistingRows?: boolean } = {}): RunningChildOptions {
  const report = uniqueMarker('NATIVECHILDCOMPLETE')
  const child = heldChildIdentity(context, { task: `${uniqueMarker('NATIVECHILDTASK')} report exactly ${report}.` })
  return heldChildOptions(context, child, {
    child: { matcher: { lastMessage: { role: 'user', text: child.task } }, finalStep: context.textStep(report) },
    allowExistingRows: options.allowExistingRows ?? false,
  })
}

/** The related proof of a missing-setting cell: the native model answers one marked prompt. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseCapabilityProbe(context)
}

/** Enable the native shell only after the user-selected Bypass policy takes effect. */
export async function enableNativeCommands(context: ManagedNativeScenarioContext): Promise<void> {
  if (nativeOptionValue(await currentNativeAgent(context), 'bypassPermissions') !== 'true') {
    await chooseSettingsOption(context.page, 'bypassPermissions-true')
    await waitForNativeOptionApplied(context, 'bypassPermissions', 'true')
  }
  await chooseSettingsOption(context.page, 'nativeCommands-true')
  await waitForNativeOptionApplied(context, 'nativeCommands', 'true')
  // The requested value can arrive before the restart's replacement catalog.
  await retryUntilPass(async () => {
    expect(nativeOptionGroup(await currentNativeAgent(context), 'bypassPermissions')?.mutable).toBe(false)
  })
}
