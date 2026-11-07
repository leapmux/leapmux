import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { join } from 'node:path'
import { assertPrivateNativePath } from '../helpers/nativePrivatePath'
import { getGlobalState } from '../helpers/server'

/** Construct the native session path before the runtime creates its directory. */
export function antigravityNativeSessionPath(context: ManagedNativeScenarioContext, agent: AgentInfo): string {
  const home = context.leapmuxServer.agentEnv?.HOME
  if (!home || !agent.agentSessionId)
    throw new Error('The native Antigravity session has no private home or identity.')
  return join(home, '.gemini', 'antigravity-cli', 'brain', agent.agentSessionId)
}

/** Locate an existing native session directory inside the private test run. */
export function antigravityNativeSessionDir(context: ManagedNativeScenarioContext, agent: AgentInfo): string {
  const directory = antigravityNativeSessionPath(context, agent)
  assertPrivateNativePath(directory, getGlobalState().tmpDir)
  return directory
}
