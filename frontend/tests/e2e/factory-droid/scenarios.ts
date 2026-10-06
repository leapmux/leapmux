import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { HeldNativeChild, NativeChildScriptContext, RunningChildOptions } from '../helpers/runningChildProof'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { heldChildIdentity, heldChildOptions, nativeChildScriptContext, openRunningNativeChild } from '../helpers/runningChildProof'
import { DROID_CHILD_SYSTEM } from './childIdentity'
import { droidChildNoticeRule } from './childNotice'
import { readDroidToolResult } from './toolResult'

/**
 * Build the scenario context of Factory Droid, with every field that its native protocol needs.
 * The Droid test object registers the title rule of every test (`droid-fixtures.ts`), so the context registers none.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.DROID, readToolResult: readDroidToolResult }
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'droid', holdWhen: ['exec'] })
}

/** Open this provider's actual child task and hold its native final answer. */
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
