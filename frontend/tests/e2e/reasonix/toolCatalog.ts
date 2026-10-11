import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { toolDescriptor, toolInputSchema } from '../helpers/modelRequestBody'

export interface ReasonixCapability {
  id: string
  kind: string
  name: string
  description?: string
}

// Installed v1.38.7 identifies commit 036c7c50c5c154f747419aee6b75667f9044c8fa.
// Its builtin Name methods and tool/identity.go supply these native identities.
const registeredTools = new Set([
  'bash',
  'bash_output',
  'code_index',
  'complete_step',
  'compress',
  'delete_range',
  'delete_symbol',
  'edit_file',
  'glob',
  'grep',
  'kill_shell',
  'ls',
  'move_file',
  'multi_edit',
  'notebook_edit',
  'read_file',
  'todo_write',
  'update_goal',
  'view_image',
  'wait',
  'web_fetch',
  'write_file',
  'ask',
  'complete_subtask',
  'docs',
  'explore',
  'fleet',
  'forget',
  'history',
  'install_skill',
  'install_source',
  'list_sessions',
  'lsp_definition',
  'lsp_diagnostics',
  'lsp_hover',
  'lsp_references',
  'memory',
  'parallel_tasks',
  'read_only_skill',
  'read_only_task',
  'read_session',
  'read_skill',
  'read_subagent_result',
  'remember',
  'research',
  'review',
  'review_report',
  'run_skill',
  'security_review',
  'session_read_strategy_receipt',
  'session_tool_result',
  'set_session_title',
  'slash_command',
  'submit_plan',
  'task',
  'use_capability',
  'web_search',
])

const coreFields: Readonly<Record<string, readonly string[]>> = {
  ask: ['decision_id', 'new_evidence', 'questions'],
  bash: ['additional_write_dirs', 'command', 'justification', 'preserve_background_processes', 'run_in_background'],
  bash_output: ['filter', 'job_id'],
  complete_step: ['evidence', 'notes', 'operation_id', 'receipt_ids', 'result', 'step', 'step_id', 'step_index'],
  compress: ['anchor', 'direction', 'focus'],
  edit_file: ['new_string', 'old_string', 'path', 'source_token'],
  kill_shell: ['job_id'],
  read_file: ['cursor', 'intent', 'limit', 'offset', 'path'],
  todo_write: ['todos'],
  update_goal: ['completion', 'next_action', 'reason', 'status'],
  use_capability: ['action', 'arguments', 'capability_id', 'limit', 'query', 'reason'],
  view_image: ['path'],
  wait: ['job_ids', 'timeout_seconds'],
  write_file: ['content', 'path', 'source_token'],
}

function requiredString(record: Record<string, unknown>, field: string): string {
  const value = record[field]
  if (typeof value !== 'string' || !value.trim() || value !== value.trim())
    throw new Error(`The native Reasonix descriptor requires an exact nonempty ${field}.`)
  return value
}

function completeRecord(value: unknown): Record<string, unknown> {
  if (!isObject(value) || value.truncated !== undefined || value.schema_omitted !== undefined
    || value.next_cursor !== undefined || value.cursor !== undefined || value.has_more !== undefined) {
    throw new Error('The installed Reasonix discovery requires a complete unpaged record.')
  }
  return value
}

/** Check every native core descriptor against its complete installed argument interface. */
export function assertReasonixCoreCatalog(request: Pick<MockModelRequestRecord, 'body'>): void {
  const body = completeRecord(request.body)
  if (!Array.isArray(body.tools) || body.tools.length !== Object.keys(coreFields).length)
    throw new Error('The native Reasonix model catalog differs from the complete installed core inventory.')
  const seen = new Set<string>()
  for (const item of body.tools) {
    const tool = toolDescriptor(completeRecord(item))
    const name = requiredString(tool, 'name')
    requiredString(tool, 'description')
    const expected = coreFields[name]
    const schema = toolInputSchema(tool)
    if (!expected || seen.has(name) || !isObject(schema) || schema.type !== 'object' || !isObject(schema.properties)
      || JSON.stringify(Object.keys(schema.properties).sort()) !== JSON.stringify([...expected].sort())) {
      throw new Error(`The native Reasonix catalog contains an unaudited or incomplete descriptor: ${name}.`)
    }
    seen.add(name)
  }
}

/** Read every deferred entry. This isolated configuration declares no remote server or additional source. */
export function parseReasonixCapabilities(text: string): ReasonixCapability[] {
  const record = completeRecord(JSON.parse(text))
  if ((record.capabilities !== null && !Array.isArray(record.capabilities)) || !Array.isArray(record.servers)
    || record.servers.length !== 0 || typeof record.note !== 'string' || !record.note) {
    throw new Error('The native Reasonix list differs from its complete isolated capability inventory.')
  }
  const seen = new Set<string>()
  return (record.capabilities ?? []).map((value: unknown) => {
    const entry = completeRecord(value)
    const id = requiredString(entry, 'id')
    const kind = requiredString(entry, 'kind')
    const name = requiredString(entry, 'name')
    if (seen.has(id) || !['tool', 'skill', 'session'].includes(kind)
      || (kind === 'tool' && (id !== `tool:${name}` || !registeredTools.has(name)))
      || (kind === 'skill' && id !== `skill:${name}`)
      || (kind === 'session' && !['session:tool_result', 'session:read_strategy_receipt'].includes(id))) {
      throw new Error(`The complete native Reasonix list contains an unaudited capability: ${id}.`)
    }
    seen.add(id)
    if (entry.description !== undefined && (typeof entry.description !== 'string' || !entry.description.trim()))
      throw new Error('The native Reasonix listed description must contain complete text when present.')
    return { id, kind, name, ...(typeof entry.description === 'string' ? { description: entry.description } : {}) }
  })
}

/** Require the exact inspected native descriptor. Ordinary installed tools expose descriptions without schemas. */
export function assertReasonixCapabilityInspection(text: string, entry: ReasonixCapability): Record<string, unknown> {
  const value = completeRecord(JSON.parse(text))
  if (value.id !== entry.id || value.kind !== entry.kind || value.name !== entry.name)
    throw new Error('The native Reasonix inspected descriptor belongs to another capability.')
  requiredString(value, 'description')
  if (entry.description !== undefined && value.description !== entry.description)
    throw new Error('The native Reasonix description changed between list and inspect.')
  if (entry.kind === 'tool' && (value.tool_name !== entry.name || !registeredTools.has(entry.name)))
    throw new Error('The native Reasonix inspected tool differs from its audited registration.')
  if (entry.kind === 'skill') {
    const schema = value.input_schema
    const properties = isObject(schema) ? schema.properties : undefined
    if (!isObject(schema) || schema.type !== 'object' || !isObject(properties)
      || ['script', 'source', 'code', 'language'].some(field => Object.hasOwn(properties, field))) {
      throw new Error('The native Reasonix deferred descriptor lacks its complete audited argument interface.')
    }
  }
  if (entry.kind === 'session') {
    const args = value.arguments
    if (!isObject(args))
      throw new Error('The native Reasonix session descriptor lacks its exact argument interface.')
    if (entry.id === 'session:tool_result') {
      if (args.tool_call_id !== 'required' || typeof args.result_ref !== 'string' || !args.result_ref
        || args.offset !== 0 || args.limit_default !== 16 * 1024 || args.limit_max !== 24 * 1024) {
        throw new Error('The native Reasonix result reader differs from its installed argument interface.')
      }
    }
    else if (args.type !== 'object' || !isObject(args.properties)
      || JSON.stringify(Object.keys(args.properties).sort()) !== JSON.stringify(['conclusion', 'read_id', 'read_tool_call_ids', 'search_tool_call_ids'])) {
      throw new Error('The native Reasonix read-strategy receipt differs from its installed argument interface.')
    }
  }
  return value
}
