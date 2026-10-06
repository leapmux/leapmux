import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import type { NativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import type { HeldNativeChild } from '../helpers/runningChildProof'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { heldChildIdentity, heldChildOptions, nativeChildRuleId, nativeChildScriptContext, openRunningNativeChild } from '../helpers/runningChildProof'
import { registerLettaChildNoticeRule } from './childNoticeRule'

/**
 * Build the scenario context of Letta Code, with every field that its native protocol needs.
 * The Letta test object registers the title rule of every test (`letta-fixtures.ts`), so the context registers none.
 */
export async function nativeContext(fixtures: NativeContextFixtures): Promise<ManagedNativeScenarioContext> {
  return { ...fixtures, provider: AgentProvider.LETTA }
}

/** Select the actual isolated executable and hold only its native runtime invocation. */
export function nativeLaunch(context: ManagedNativeScenarioContext): NativeStartupLaunch {
  return resolveNativeStartupLaunch(context.leapmuxServer.agentEnv, { binaryName: 'letta', holdWhen: ['server'] })
}

/**
 * Open this provider's actual child task and hold its native final answer.
 * Letta Code reports a completed child to the root in a notice that names the spawn call, so the child registers the
 * rule that answers that notice before its answer can complete.
 */
export async function runningChild(context: ManagedNativeScenarioContext, options: { allowExistingRows?: boolean } = {}): Promise<HeldNativeChild> {
  const script = nativeChildScriptContext(context)
  const child = heldChildIdentity(script)
  return openRunningNativeChild(context, heldChildOptions(script, child, {
    allowExistingRows: options.allowExistingRows ?? false,
    beforeRelease: async () => {
      await registerLettaChildNoticeRule(context, { name: nativeChildRuleId(child.gate, 'letta-native-child-notice'), spawnCallId: child.spawn.id, description: child.description, report: 'NATIVECHILDCOMPLETE', reply: 'The native child completed.' })
    },
  }))
}
