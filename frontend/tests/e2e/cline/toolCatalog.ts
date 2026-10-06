import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { requireBinary } from '../helpers/binaryOnPath'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { getGlobalState, hubSpawnEnv } from '../helpers/server'

export interface ClineCatalogTool {
  id: string
  description: string
  defaultEnabled: boolean
  headlessToolNames: string[]
}

// The native configuration command lists disabled builtins and plugin tools also.
// The table follows core/extensions/tools/runtime.ts and team/team-tools.ts.
const CORE_TOOL_NAMES: Readonly<Record<string, readonly string[]>> = {
  read_files: ['read_files'],
  search_codebase: ['search_codebase'],
  run_commands: ['run_commands'],
  editor: ['editor', 'apply_patch'],
  fetch_web_content: ['fetch_web_content'],
  skills: ['skills'],
  ask_question: ['ask_question'],
  spawn_agent: ['spawn_agent'],
  teams: [
    'team_spawn_teammate',
    'team_shutdown_teammate',
    'team_status',
    'team_task',
    'team_run_task',
    'team_cancel_run',
    'team_list_runs',
    'team_await_runs',
    'team_send_message',
    'team_broadcast',
    'team_read_mailbox',
    'team_mission_log',
    'team_cleanup',
    'team_create_outcome',
    'team_attach_outcome_fragment',
    'team_review_outcome_fragment',
    'team_finalize_outcome',
    'team_list_outcomes',
  ],
  web_search: ['web_search'],
}

/** Require the complete native CLI catalog, including disabled capabilities. */
export function parseClineCompleteCatalog(value: unknown): ClineCatalogTool[] {
  if (!Array.isArray(value) || value.length === 0)
    throw new Error('The native Cline configuration contains no complete tool catalog.')
  const tools = value.map((entry: unknown): ClineCatalogTool => {
    if (!isObject(entry) || entry.type === 'plugin')
      throw new Error('The private Cline catalog contains an invalid entry or an unaudited plugin.')
    if (typeof entry.id !== 'string' || !Object.hasOwn(CORE_TOOL_NAMES, entry.id)
      || typeof entry.description !== 'string' || !entry.description.trim()
      || typeof entry.defaultEnabled !== 'boolean' || !Array.isArray(entry.headlessToolNames)
      || entry.headlessToolNames.length === 0 || !entry.headlessToolNames.every((name: unknown): name is string => typeof name === 'string' && name.trim() !== '')) {
      throw new Error('The native Cline catalog contains an unaudited capability or an incomplete descriptor.')
    }
    const actual = [...entry.headlessToolNames].sort()
    const expected = [...CORE_TOOL_NAMES[entry.id]!].sort()
    const validEditor = entry.id === 'editor' && actual.length === 1 && expected.includes(actual[0]!)
    if (!validEditor && (actual.length !== expected.length || actual.some((name, index) => name !== expected[index])))
      throw new Error('The native Cline capability contains an unaudited callable tool.')
    return { id: entry.id, description: entry.description, defaultEnabled: entry.defaultEnabled, headlessToolNames: [...entry.headlessToolNames] }
  })
  const ids = tools.map(tool => tool.id)
  if (new Set(ids).size !== ids.length || Object.keys(CORE_TOOL_NAMES).some(id => id !== 'web_search' && !ids.includes(id)))
    throw new Error('The native Cline catalog repeats a capability or omits a required builtin.')
  return tools
}

export type ClineCatalogCommand = (
  executable: string,
  args: string[],
  options: { cwd: string, env: NodeJS.ProcessEnv, encoding: 'utf8', maxBuffer: number, timeout: number },
) => Promise<{ stdout: string, stderr: string }>

const command = promisify(execFile)
const CATALOG_COMMAND_LIMIT_MS = 60_000

interface ClineCatalogQuery {
  workingDir: string
  runDir: string
  environment: NodeJS.ProcessEnv
  deadline?: number
  onReceipt?: (receipt: { stdout: string, stderr: string }) => Promise<void>
}

/** Run only the installed metadata command with private paths and a finite deadline. */
export async function queryClineCompleteCatalog(query: ClineCatalogQuery, execute: ClineCatalogCommand = command): Promise<ClineCatalogTool[]> {
  const privateEnv = query.environment
  for (const key of ['HOME', 'CLINE_DIR', 'CLINE_DATA_DIR']) {
    const path = privateEnv[key]
    if (!path)
      throw new Error(`The native Cline catalog requires its private ${key}.`)
    assertPrivateNativePath(path, query.runDir)
  }
  assertPrivateNativePath(query.workingDir, query.runDir)
  const env = hubSpawnEnv(privateEnv)
  const executable = requireBinary('cline', 'The native Cline catalog requires the installed CLI', env)
  const deadline = query.deadline
  const remaining = deadline === undefined ? CATALOG_COMMAND_LIMIT_MS : Math.floor(deadline - Date.now())
  if (!Number.isFinite(remaining) || remaining <= 0)
    throw new Error('The native Cline catalog has no remaining command time.')
  const result = await execute(executable, ['config', 'tools', '--json'], { cwd: query.workingDir, env, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024, timeout: Math.min(remaining, CATALOG_COMMAND_LIMIT_MS) })
  await query.onReceipt?.(result)
  let decoded: unknown
  try {
    decoded = JSON.parse(result.stdout)
  }
  catch (cause) {
    throw new Error('The native Cline configuration returned invalid tool catalog JSON.', { cause })
  }
  return parseClineCompleteCatalog(decoded)
}

/** Read the complete catalog without changing the active native session or model scenario. */
export async function readClineCompleteCatalog(context: ManagedNativeScenarioContext, onReceipt: ClineCatalogQuery['onReceipt']): Promise<ClineCatalogTool[]> {
  const privateEnv = context.leapmuxServer.agentEnv
  if (!privateEnv)
    throw new Error('The native Cline catalog requires its isolated agent environment.')
  const agent = await currentNativeAgent(context)
  if (context.provider !== AgentProvider.CLINE || agent.agentProvider !== AgentProvider.CLINE || !agent.agentSessionId)
    throw new Error('The native Cline catalog requires the actual active Cline session.')
  const before = await context.modelScript.status()
  const deadline = context.modelScript.testDeadline()
  const tools = await queryClineCompleteCatalog({ environment: privateEnv, workingDir: agent.workingDir, runDir: getGlobalState().tmpDir, ...(deadline === undefined ? {} : { deadline }), ...(onReceipt === undefined ? {} : { onReceipt }) })
  expect(await context.modelScript.status()).toEqual(before)
  const after = await currentNativeAgent(context)
  expect(after.id).toBe(agent.id)
  expect(after.agentSessionId).toBe(agent.agentSessionId)
  return tools
}
