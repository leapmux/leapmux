import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { isObject } from '../../../src/lib/jsonPick'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { getGlobalState } from '../helpers/server'

/** Verify the native registry and project marker before reading a session path. */
export function geminiNativeProject(context: ManagedNativeScenarioContext, agent: AgentInfo): string {
  const home = context.leapmuxServer.agentEnv?.GEMINI_CLI_HOME
  if (!home || !agent.workingDir || !agent.agentSessionId)
    throw new Error('The native Gemini session has no private home or identity.')
  assertPrivateNativePath(home, getGlobalState().tmpDir)
  const root = join(home, '.gemini')
  const registry: unknown = JSON.parse(readFileSync(join(root, 'projects.json'), 'utf8'))
  if (!isObject(registry) || !isObject(registry.projects))
    throw new Error('The native Gemini registry is invalid.')
  const slug = registry.projects[agent.workingDir]
  if (typeof slug !== 'string' || !/^[a-z0-9-]+$/.test(slug))
    throw new Error('The native Gemini registry has no exact project owner.')
  const project = join(root, 'tmp', slug)
  assertPrivateNativePath(project, getGlobalState().tmpDir)
  if (readFileSync(join(project, '.project_root'), 'utf8').trim() !== agent.workingDir)
    throw new Error('The native Gemini project marker belongs to another directory.')
  return project
}
