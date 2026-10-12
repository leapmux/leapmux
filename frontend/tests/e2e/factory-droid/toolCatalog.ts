import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeToolDescriptor } from '../helpers/nativeScenario'
import { isObject } from '../../../src/lib/jsonPick'
import { requestToolDescriptors, toolInputSchema } from '../helpers/modelRequestBody'

export interface DroidCompleteToolCatalog {
  current: NativeToolDescriptor[]
  deferred: string[]
}

function requestText(body: Record<string, unknown>): string[] {
  const messages = Array.isArray(body.messages) ? body.messages : Array.isArray(body.input) ? body.input : []
  const values = [body.system, ...messages.filter(isObject).map(message => message.content)]
  return values.flatMap(value => typeof value === 'string'
    ? [value]
    : Array.isArray(value)
      ? value.filter(isObject).filter(block => typeof block.text === 'string').map(block => String(block.text))
      : [])
}

/** Read the current descriptors and the complete native hidden-tool reminder. */
export function droidCompleteToolCatalog(request: MockModelRequestRecord): DroidCompleteToolCatalog {
  const body = isObject(request.body) ? request.body : undefined
  const tools = requestToolDescriptors(request.protocol, body)
  if (!body || !tools || tools.length === 0)
    throw new Error('The native Droid request contains no current tool inventory.')
  const seen = new Set<string>()
  const current = tools.map((tool): NativeToolDescriptor => {
    const schema = toolInputSchema(tool)
    if (typeof tool.name !== 'string' || !tool.name.trim() || typeof tool.description !== 'string' || !isObject(schema) || schema.type !== 'object')
      throw new Error('The native Droid catalog contains an incomplete tool descriptor.')
    if (seen.has(tool.name))
      throw new Error('The native Droid catalog contains duplicate tool names.')
    seen.add(tool.name)
    return { name: tool.name, description: tool.description, inputSchema: schema }
  })
  const deferred: string[] = []
  const text = requestText(body)
  const reminders = text.flatMap(value => [...value.matchAll(/<system-reminder>\n([\s\S]*?)\n<\/system-reminder>/g)].map(match => match[1] ?? ''))
  for (const value of text) {
    const headings = value.match(/Deferred tools:/g)?.length ?? 0
    const delimited = [...value.matchAll(/<system-reminder>\n([\s\S]*?)\n<\/system-reminder>/g)].filter(match => match[1]?.includes('Deferred tools:')).length
    if (headings !== delimited)
      throw new Error('The native Droid deferred inventory is truncated or has invalid delimiters.')
  }
  const inventories = reminders.filter(value => value.includes('Deferred tools:'))
  if (inventories.length > 1)
    throw new Error('The native Droid request contains multiple deferred inventories.')
  for (const reminder of inventories) {
    const marker = '\n\nDeferred tools:\n'
    const offset = reminder.indexOf(marker)
    if (offset < 0 || !reminder.includes('schemas may be omitted from the current tool list') || !reminder.includes('select:<name>[,<name>...]'))
      throw new Error('The native Droid deferred inventory lacks its native completeness statement.')
    const lines = reminder.slice(offset + marker.length).split('\n')
    if (lines.length === 0 || lines.some(name => !/^[^\s<>]+$/.test(name)))
      throw new Error('The native Droid deferred inventory contains an invalid tool name.')
    for (const name of lines) {
      if (seen.has(name))
        throw new Error('The native Droid current and deferred inventories contain a duplicate tool.')
      seen.add(name)
      deferred.push(name)
    }
  }
  return { current, deferred }
}

/**
 * The argument types of the Script descriptor that Droid offers the model.
 *
 * Droid 0.233.0 declares `script` and `inputs` there. Its parser also accepts a
 * `waitForMs` field, but the model-facing schema (`llmInputSchema`) omits it, so a
 * catalog never lists it. A call may still send the field.
 */
export const DROID_SCRIPT_ARGUMENTS = { script: 'string', inputs: 'object' } as const

/** Detect execution capability from native descriptions and argument schemas. */
export function droidScriptExecutors(catalog: readonly NativeToolDescriptor[]): NativeToolDescriptor[] {
  return catalog.filter(tool => /javascript|typescript|python|code|script|repl|interpreter/i.test(`${tool.name} ${tool.description}`)
    && Object.keys(isObject(tool.inputSchema.properties) ? tool.inputSchema.properties : {}).some(name => /^(?:code|source|script|expression|javascript|python|input)$/i.test(name)))
}

/** Require the exact loaded schema for each announced deferred tool. */
export function droidLoadedToolSchemas(before: DroidCompleteToolCatalog, after: DroidCompleteToolCatalog, selected: readonly string[]): NativeToolDescriptor[] {
  if (selected.length === 0 || new Set(selected).size !== selected.length || selected.some(name => !before.deferred.includes(name)))
    throw new Error('The Droid schema receipt requires distinct announced deferred names.')
  return selected.map((name) => {
    const tool = after.current.find(candidate => candidate.name === name)
    if (!tool || after.deferred.includes(name))
      throw new Error(`The native Droid search supplied no loaded schema for ${name}.`)
    return tool
  })
}
