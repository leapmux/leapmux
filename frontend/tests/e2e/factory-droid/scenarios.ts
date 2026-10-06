import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { HeldNativeChild, NativeChildScriptContext, RunningChildOptions } from '../helpers/runningChildProof'
import type { ProviderAgent } from '../helpers/workspace'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { managedNativeContext } from '../helpers/nativeScenario'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { gitRepositoryWorkingDir } from '../helpers/providerWorkingDir'
import { heldChildIdentity, heldChildOptions, nativeChildScriptContext, openRunningNativeChild } from '../helpers/runningChildProof'
import { exerciseCapabilityProbe } from '../helpers/unsupportedConfiguration'
import { DROID_CHILD_SYSTEM } from './childIdentity'
import { droidChildNoticeRule } from './childNotice'
import { readDroidToolResult } from './toolResult'

/** How a Factory Droid agent opens. */
export const DROID_AGENT: ProviderAgent = { provider: AgentProvider.DROID, prefix: 'droid-e2e', workingDir: gitRepositoryWorkingDir }

/**
 * Build the scenario context of Factory Droid, with every field that its native protocol needs.
 * The Droid test object registers the title rule of every test (`droid-fixtures.ts`), so the context registers none.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return managedNativeContext(fixtures, DROID_AGENT, { readToolResult: readDroidToolResult })
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'droid', holdWhen: ['exec'] })
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
 * Droid runs the child as a background Task, its child turns state the read-only system prompt, and the root answers
 * the notice of the completed child through a rule.
 */
export function runningChildOptions(context: NativeChildScriptContext, options: { allowExistingRows?: boolean } = {}): RunningChildOptions {
  const child = heldChildIdentity(context, { spawn: { background: true } })
  return heldChildOptions(context, child, {
    child: { matcher: { system: DROID_CHILD_SYSTEM, body: child.task }, finalStep: context.textStep('NATIVECHILDCOMPLETE') },
    allowExistingRows: options.allowExistingRows ?? false,
    rules: [droidChildNoticeRule(child.description, { text: 'The native child completed.' })],
  })
}

/** The related proof of a missing-setting cell: the native model answers one marked prompt. */
export async function relatedNativeProof(context: ManagedNativeScenarioContext): Promise<void> {
  await exerciseCapabilityProbe(context)
}
