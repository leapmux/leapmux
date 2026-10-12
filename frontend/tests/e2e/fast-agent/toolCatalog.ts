import type { MockModelScenarioStatus } from '../helpers/mockModelScript'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { randomUUID } from 'node:crypto'
import { ASSEMBLED_MESSAGE } from '../../../src/generated/contracts/worker-vocab'
import { AgentInputKind, EnqueueAgentInputRequestSchema, EnqueueAgentInputResponseSchema, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { getTestChannel } from '../helpers/api'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, expectSameNativeSession } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { waitForAgentIdle } from '../helpers/ui'

export interface FastAgentCatalogTool {
  name: string
  description: string
}

export interface FastAgentCompleteCatalog {
  tools: Array<FastAgentCatalogTool & { schema: Record<string, unknown> }>
  hosted: string[]
}

function markdownText(value: string): string {
  return value.replace(/\\([\\[\]*_`])/g, '$1')
}

/** Read every local tool from the native list. Keep hosted tools in a separate inventory. */
export function parseFastAgentCatalog(text: string): { tools: FastAgentCatalogTool[], hosted: string[] } {
  if (!text.startsWith('# tools\n') || !text.includes('## MCP / local tools\n'))
    throw new Error('The native Fast Agent catalog lacks its complete tool-list headings.')
  const tools: FastAgentCatalogTool[] = []
  const hosted: string[] = []
  const sections = new Set<string>()
  let section = ''
  let current: FastAgentCatalogTool | undefined
  for (const line of text.split('\n')) {
    if (line.startsWith('## ')) {
      if (!['## MCP / local tools', '## Provider-managed / hosted tools'].includes(line) || sections.has(line))
        throw new Error('The native Fast Agent catalog contains an unknown or repeated tool section.')
      sections.add(line)
      section = line
      current = undefined
      continue
    }
    const header = /^(\d+)\. \*\*((?:\\.|[^*])+)\*\*/.exec(line)
    if (section === '## MCP / local tools' && header) {
      if (Number(header[1]) !== tools.length + 1)
        throw new Error('The native Fast Agent catalog has an incomplete tool sequence.')
      current = { name: markdownText(header[2]!), description: '' }
      tools.push(current)
    }
    else if (section === '## MCP / local tools' && /^\d+\./.test(line)) {
      throw new Error('The native Fast Agent catalog contains a malformed tool heading.')
    }
    else if (section === '## MCP / local tools' && current && line.startsWith('    > ') && !line.startsWith('    > **')) {
      current.description += `${current.description ? '\n' : ''}${markdownText(line.slice(6))}`
    }
    else if (section === '## Provider-managed / hosted tools' && line.startsWith('- ')) {
      const name = /^- \*\*((?:\\.|[^*])+)\*\*/.exec(line)?.[1]
      if (!name)
        throw new Error('The native Fast Agent catalog contains a malformed hosted-tool heading.')
      hosted.push(markdownText(name))
    }
  }
  if (tools.length === 0 || tools.some(tool => !tool.name.trim() || !tool.description.trim()))
    throw new Error('The native Fast Agent catalog contains no complete local tool inventory.')
  if (new Set(tools.map(tool => tool.name)).size !== tools.length || new Set(hosted).size !== hosted.length)
    throw new Error('The native Fast Agent catalog repeats a native tool identity.')
  return { tools, hosted }
}

/** Require the schema for the exact listed native tool. */
export function parseFastAgentToolSchema(text: string, name: string): Record<string, unknown> {
  if (!name || !text.startsWith(`# Tool schema: ${name.replace(/[\\[\]*_`]/g, '\\$&')}\n`))
    throw new Error('The native Fast Agent schema belongs to another tool.')
  const blocks = [...text.matchAll(/## Input schema\n\n```json\n([\s\S]*?)\n```/g)]
  if (blocks.length !== 1)
    throw new Error('The native Fast Agent command supplied no unique input schema.')
  const schema: unknown = JSON.parse(blocks[0]![1]!)
  if (!isObject(schema) || schema.type !== 'object' || (schema.properties !== undefined && !isObject(schema.properties)))
    throw new Error('The native Fast Agent tool input schema must be an object schema.')
  return schema
}

/** Match only new, completed native text from the same Worker agent and native session. */
export function fastAgentCatalogCommandReply(before: NativeMessageSnapshot, after: NativeMessageSnapshot, heading: string): string | null {
  if (!before.agentId || !before.agentSessionId || before.agentId !== after.agentId || before.agentSessionId !== after.agentSessionId)
    throw new Error('The native Fast Agent identity changed during its catalog command.')
  const afterSeq = before.messages.at(-1)?.seq ?? -1n
  const texts = after.messages
    .filter(message => message.seq > afterSeq && message.source === MessageSource.AGENT && message.agentSessionId === before.agentSessionId)
    .map(nativeMessageBody)
    .filter(frame => isObject(frame) && frame.type === ASSEMBLED_MESSAGE.Type && frame.kind === ASSEMBLED_MESSAGE.KindText
      && frame.completion === ASSEMBLED_MESSAGE.CompletionComplete && typeof frame.text === 'string')
    .map(frame => isObject(frame) && typeof frame.text === 'string' ? frame.text : '')
    .filter(text => text.startsWith(heading))
  if (texts.length > 1)
    throw new Error('The native Fast Agent command produced ambiguous catalog replies.')
  return texts[0] ?? null
}

/** A slash command must consume no queued, rule, fallback, or unexpected model request. */
export function assertFastAgentCatalogNoInference(before: MockModelScenarioStatus, after: MockModelScenarioStatus): void {
  if (before.stepCount !== after.stepCount || before.nextStep !== after.nextStep || before.requests.length !== after.requests.length
    || before.unexpectedRequests.length !== after.unexpectedRequests.length || JSON.stringify(before.ruleMatches) !== JSON.stringify(after.ruleMatches)
    || JSON.stringify(before.pendingGates) !== JSON.stringify(after.pendingGates)) {
    throw new Error('The native Fast Agent catalog command changed the model scenario or started model inference.')
  }
}

/** Send native metadata arguments unchanged through the same Worker queue that the composer uses. */
export async function sendFastAgentCatalogCommand(server: ManagedNativeScenarioContext['leapmuxServer'], agentId: string, command: string): Promise<void> {
  if (!agentId.trim() || (command !== '/tools' && !command.startsWith('/tools ')) || /[\r\n]/.test(command))
    throw new Error('The Fast Agent native catalog requires an exact agent and single-line tools command.')
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const response = await channel.callWorker(server.workerId, 'EnqueueAgentInput', EnqueueAgentInputRequestSchema, EnqueueAgentInputResponseSchema, {
    agentId,
    inputId: randomUUID(),
    text: command,
    attachments: [],
    kind: AgentInputKind.USER_MESSAGE,
  })
  if (!response.snapshot || response.snapshot.agentId !== agentId || response.snapshot.paused)
    throw new Error('The exact Fast Agent metadata command reached no active queue for its Worker agent.')
}

/** Read this command's new native transcript response and require zero model inference. */
async function nativeToolsCommand(context: ManagedNativeScenarioContext, command: string, heading: string): Promise<string> {
  const agent = await currentNativeAgent(context)
  const before = await readNativeMessageSnapshot(context, agent.id)
  const modelBefore = await context.modelScript.status()
  await sendFastAgentCatalogCommand(context.leapmuxServer, agent.id, command)
  const response = await retryUntilPass(async () => {
    const reply = fastAgentCatalogCommandReply(before, await readNativeMessageSnapshot(context, agent.id), heading)
    if (reply === null)
      throw new Error('The native Fast Agent catalog command supplied no completed response.')
    return reply
  })
  await waitForAgentIdle(context.page)
  assertFastAgentCatalogNoInference(modelBefore, await context.modelScript.status())
  expectSameNativeSession(agent, await currentNativeAgent(context), `The native Fast Agent command ${command}`)
  return response
}

/** The native slash command lists every local tool that the native configuration makes available. */
export async function readFastAgentCompleteCatalog(context: ManagedNativeScenarioContext): Promise<FastAgentCompleteCatalog> {
  const catalog = parseFastAgentCatalog(await nativeToolsCommand(context, '/tools', '# tools\n'))
  const tools: FastAgentCompleteCatalog['tools'] = []
  for (const tool of catalog.tools) {
    const schema = parseFastAgentToolSchema(await nativeToolsCommand(context, `/tools ${tool.name}`, '# Tool schema: '), tool.name)
    tools.push({ ...tool, schema })
  }
  return { tools, hosted: catalog.hosted }
}

/** This private default configuration supplies shell, filesystem, and subagent tools only. */
export function assertFastAgentShellCatalog(catalog: FastAgentCompleteCatalog): void {
  const fields: Record<string, readonly string[]> = {
    execute: ['command', 'args', 'env', 'cwd'],
    read_text_file: ['path', 'line', 'limit'],
    write_text_file: ['path', 'content'],
    subagent: ['message', 'model', 'label', 'include_user_message'],
  }
  if (catalog.hosted.length !== 0 || catalog.tools.length !== Object.keys(fields).length)
    throw new Error('The complete native Fast Agent catalog differs from its audited private configuration.')
  const seen = new Set<string>()
  for (const tool of catalog.tools) {
    const expected = fields[tool.name]
    if (!expected || seen.has(tool.name) || !isObject(tool.schema.properties)
      || JSON.stringify(Object.keys(tool.schema.properties).sort()) !== JSON.stringify([...expected].sort())) {
      throw new Error('The complete native Fast Agent catalog contains an unaudited tool or input schema.')
    }
    seen.add(tool.name)
  }
  const shell = catalog.tools.find(tool => tool.name === 'execute')
  if (shell?.description !== 'Execute a shell command.')
    throw new Error('The native Fast Agent execute tool does not describe its audited shell behavior.')
}
