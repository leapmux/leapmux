import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { join } from 'node:path'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { getGlobalState } from '../helpers/server'

/** Locate the native transcript directory of the exact Antigravity conversation. */
export function antigravityNativeSessionDir(context: ManagedNativeScenarioContext, agent: AgentInfo): string {
  const home = context.leapmuxServer.agentEnv?.HOME
  if (!home || !agent.agentSessionId)
    throw new Error('The native Antigravity session has no private home or identity.')
  const project = join(home, '.gemini', 'antigravity-cli', 'brain', agent.agentSessionId)
  assertPrivateNativePath(project, getGlobalState().tmpDir)
  return project
}
