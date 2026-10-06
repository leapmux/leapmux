import type { MockModelProtocol } from './mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { googleFunctionDeclarations } from './googleModelContent'

/*
 * The protocol accessors of a recorded model request body. Each one states WHERE a
 * protocol keeps one part of a request, and no policy: the conversation rows, the
 * content of a row, the rows that carry system instructions, the system fields
 * outside the rows, and the tool catalog. The matcher readers in mockModelScript.ts
 * stay lenient, because provider prompts vary, and the assertion readers in
 * nativeScenario.ts stay strict. Both read through these accessors, so a new
 * protocol is one edit here.
 */

/** The protocols that state their conversation in a generic body. The AWS event stream of Kiro does not. */
export type GenericModelProtocol = Exclude<MockModelProtocol, 'aws-event-stream'>

/** The body field that holds the conversation rows of each generic protocol. */
const ROWS_FIELD: Readonly<Record<GenericModelProtocol, string>> = {
  'openai-chat-completions': 'messages',
  'openai-responses': 'input',
  'anthropic-messages': 'messages',
  'google-generative-language': 'contents',
}

/**
 * The conversation rows of a request body, unchanged: `messages`, `input`, or `contents`, by protocol. A Responses
 * `input` can be one string. The answer is undefined for a body that is not an object, and for the AWS event stream,
 * whose service states no generic rows.
 */
export function requestRows(protocol: MockModelProtocol, body: unknown): unknown {
  if (protocol === 'aws-event-stream' || !isObject(body))
    return undefined
  return body[ROWS_FIELD[protocol]]
}

/** The content of one conversation row, unchanged: `parts` for Google, and `content` for each other protocol. */
export function rowContent(protocol: GenericModelProtocol, row: Record<string, unknown>): unknown {
  return protocol === 'google-generative-language' ? row.parts : row.content
}

/** The roles of a conversation row that carries system instructions. Anthropic and Google keep them outside the rows. */
const SYSTEM_ROW_ROLES: Readonly<Record<GenericModelProtocol, ReadonlySet<unknown>>> = {
  'openai-chat-completions': new Set(['system', 'developer']),
  'openai-responses': new Set(['system', 'developer']),
  'anthropic-messages': new Set(),
  'google-generative-language': new Set(),
}

/** Whether one conversation row carries system instructions: a `system` or `developer` row of an OpenAI protocol. */
export function isSystemRow(protocol: GenericModelProtocol, row: Record<string, unknown>): boolean {
  return SYSTEM_ROW_ROLES[protocol].has(row.role)
}

/**
 * The system instructions that a request body keeps outside its conversation rows, unchanged, one content value for
 * each field that the body states:
 *
 * - Responses: `instructions`.
 * - Anthropic: `system`, a string or an array of blocks.
 * - Google: the `parts` of `systemInstruction`.
 * - Chat Completions: none, because it keeps its system instructions in its rows.
 *
 * The answer is empty for a body that is not an object, and for the AWS event stream.
 */
export function requestSystemFields(protocol: MockModelProtocol, body: unknown): unknown[] {
  if (!isObject(body))
    return []
  switch (protocol) {
    case 'openai-responses':
      return body.instructions === undefined ? [] : [body.instructions]
    case 'anthropic-messages':
      return body.system === undefined ? [] : [body.system]
    case 'google-generative-language':
      return isObject(body.systemInstruction) && body.systemInstruction.parts !== undefined ? [body.systemInstruction.parts] : []
    case 'openai-chat-completions':
    case 'aws-event-stream':
      return []
  }
}

/**
 * The descriptor of one tool entry: the entry itself when it states a name, else its Chat Completions `function`
 * object when that states a name, else the `custom` object of a custom tool when that states a name. An entry that
 * states no name comes back unchanged, so a strict reader can refuse it.
 */
export function toolDescriptor(entry: Record<string, unknown>): Record<string, unknown> {
  if (entry.name !== undefined && entry.name !== null)
    return entry
  if (isObject(entry.function) && entry.function.name !== undefined && entry.function.name !== null)
    return entry.function
  if (entry.type === 'custom' && isObject(entry.custom) && entry.custom.name !== undefined && entry.custom.name !== null)
    return entry.custom
  return entry
}

/**
 * The tool descriptors that a request body offers, in their native order, each taken out of its envelope by
 * {@link toolDescriptor}, or the Google function declarations. The answer is undefined when the body holds no `tools`
 * array. An entry that is not an object fails, because no protocol states one.
 */
export function requestToolDescriptors(protocol: MockModelProtocol, body: unknown): Record<string, unknown>[] | undefined {
  if (protocol === 'aws-event-stream' || !isObject(body) || !Array.isArray(body.tools))
    return undefined
  if (protocol === 'google-generative-language')
    return googleFunctionDeclarations(body.tools)
  return body.tools.map((entry: unknown) => {
    if (!isObject(entry))
      throw new Error('The native model tool catalog contains an invalid entry.')
    return toolDescriptor(entry)
  })
}

/**
 * The argument schema of one descriptor, unchanged: `parametersJsonSchema` (Google), `parameters` (OpenAI), or
 * `input_schema` (Anthropic), the first one that the descriptor states.
 */
export function toolInputSchema(descriptor: Record<string, unknown>): unknown {
  return descriptor.parametersJsonSchema ?? descriptor.parameters ?? descriptor.input_schema
}
