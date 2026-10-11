/**
 * Serve Kiro's native Amazon Web Services (AWS) JSON 1.0 endpoints and remote startup operations.
 *
 * Each model call uses POST /. X-Amz-Target identifies its operation.
 * Model responses use the native AWS event stream in ./awsEventStream.
 * This module selects each decoded model turn through the shared script.
 * Native probes confirmed the following requirements.
 *
 * Each completed model response ends with metadataEvent and a stop reason.
 * Without that event, Kiro treats the stream as incomplete and repeats the request.
 * That repeat consumes the next scripted step.
 *
 * ListAvailableModels supplies only the KIRO_MOCK_MODELS catalog.
 * Without its response, the session has no model.
 * Other operations receive 400 responses.
 * Kiro then uses defaults for feature configuration, remote web tools, and usage limits.
 *
 * Remote startup calls use Smithy RPC v2 with Concise Binary Object Representation (CBOR).
 * Native account catalogs return empty lists. Cloud configuration returns the native disabled exception.
 */
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { MockModelScriptHost } from './mockModelRequest'
import type { MockModelDeliveredError, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { ModelStream } from './modelStream'
import { Buffer } from 'node:buffer'
import { randomUUID } from 'node:crypto'
import { isObject } from '../../../src/lib/jsonPick'
import { encodeEventStreamEvent, EVENT_STREAM_CONTENT_TYPE } from './awsEventStream'
import { KIRO_E2E_API_KEY } from './mockAgentEnvironment'
import { mockCredentialReceipt } from './mockCredentials'
import { readJSONBody, readMockBody, writeMockJSON, writeResponseHeaders } from './mockHttp'
import { contentText, scenarioIDFromTexts } from './mockModelScript'
import { createModelStream } from './modelStream'

/** The header that states the operation of an AWS JSON 1.0 call. */
export const KIRO_TARGET_HEADER = 'x-amz-target'

/** The content type of an AWS JSON 1.0 answer. */
const AWS_JSON_CONTENT_TYPE = 'application/x-amz-json-1.0'

/** The one credential that the mock takes, as Kiro states it in `Authorization`. */
const KIRO_E2E_AUTHORIZATION = `Bearer ${KIRO_E2E_API_KEY}`

const KIRO_REMOTE_RPC_PREFIX = '/service/KiroWebBearerService/operation/'
const KIRO_REMOTE_RPC_RESPONSES: ReadonlyMap<string, { status: number, body: Buffer }> = new Map([
  ['ListAvailableProviders', { status: 200, body: Buffer.from('a16970726f76696465727380', 'hex') }],
  ['ListSpaces', { status: 200, body: Buffer.from('a16673706163657380', 'hex') }],
  // Native CBOR: {__type:'CloudConfigNotEnabledException',message:'Cloud configuration is disabled in the fixture.'}.
  ['GetConfigManifest', { status: 403, body: Buffer.from('a2665f5f74797065781e436c6f7564436f6e6669674e6f74456e61626c6564457863657074696f6e676d657373616765782f436c6f756420636f6e66696775726174696f6e2069732064697361626c656420696e2074686520666978747572652e', 'hex') }],
])

/** Remote operations identify themselves in the native service path. */
function kiroRemoteOperation(path: string): string | undefined {
  if (!path.startsWith(KIRO_REMOTE_RPC_PREFIX))
    return undefined
  return path.slice(KIRO_REMOTE_RPC_PREFIX.length).split('?')[0]
}

/** Serve the native startup operations with their exact empty-input contract. */
async function handleKiroRemoteRpc(request: IncomingMessage, response: ServerResponse, operation: string): Promise<void> {
  if (request.method !== 'POST') {
    writeMockJSON(response, 405, { message: 'The native Kiro remote operation requires POST.' })
    return
  }
  if (request.headers.authorization !== KIRO_E2E_AUTHORIZATION) {
    writeMockJSON(response, 401, { message: 'The native Kiro remote credential is not the fixture credential.' })
    return
  }
  if (request.headers['smithy-protocol'] !== 'rpc-v2-cbor' || request.headers['content-type'] !== 'application/cbor') {
    writeMockJSON(response, 415, { message: 'The native Kiro remote operation requires Smithy RPC v2 with CBOR.' })
    return
  }
  const reply = KIRO_REMOTE_RPC_RESPONSES.get(operation)
  if (!reply) {
    writeMockJSON(response, 400, { message: 'The native Kiro remote operation is not supported by the fixture.' })
    return
  }
  const body = await readMockBody(request)
  // Smithy can encode its empty map with a definite or indefinite length.
  // A nonempty request needs a complete operation implementation before the fixture can accept it.
  const emptyMap = (body.length === 1 && body[0] === 0xA0)
    || (body.length === 2 && body[0] === 0xBF && body[1] === 0xFF)
  if (!emptyMap) {
    writeMockJSON(response, 400, { message: 'The native Kiro remote startup operation accepts only an empty CBOR map.' })
    return
  }
  writeResponseHeaders(response, reply.status, { 'content-type': 'application/cbor', 'smithy-protocol': 'rpc-v2-cbor' })
  response.end(reply.body)
}

/** One event of a model turn: its type, and its JSON payload. */
type KiroEvent = [eventType: string, payload: Record<string, unknown>]

/** The operations the surface answers, by the last segment of `X-Amz-Target`. */
const KIRO_OPERATION = {
  GenerateAssistantResponse: 'GenerateAssistantResponse',
  ListAvailableModels: 'ListAvailableModels',
} as const

/**
 * The values of `thinking.type` that make a model's thinking switchable.
 *
 * Kiro 2.24 offers its `thinking` option only for a model whose schema lists both values.
 * Its request then states `adaptive` for On and `disabled` for Off. Kiro never sends `enabled`.
 */
const KIRO_THINKING_TYPES = ['adaptive', 'disabled'] as const

/** One value of `thinking.type` that Kiro sends. */
type KiroThinkingType = typeof KIRO_THINKING_TYPES[number]

/** One model of the catalogue, in the shape `ListAvailableModels` answers with. */
export interface KiroMockModel {
  modelId: string
  modelName: string
  description: string
  rateMultiplier: number
  /** The effort levels the model takes, or none for a model with no effort axis. */
  effortLevels?: readonly string[]
  /** The level a session starts on. Required with `effortLevels`. */
  defaultEffort?: string
  /** The independent thinking axis, when the model can switch it. */
  thinkingDefault?: KiroThinkingType
}

/**
 * The model catalogue the mock reports.
 *
 * The first model has an effort axis. The second has no axis. The third has separate effort and thinking axes.
 * Kiro reads their native schema and copies selected values into `additionalModelRequestFields`.
 */
export const KIRO_MOCK_MODELS: readonly KiroMockModel[] = [
  { modelId: 'kiro-e2e', modelName: 'Kiro E2E', description: 'Mock model with effort', rateMultiplier: 1, effortLevels: ['low', 'medium', 'high'], defaultEffort: 'high' },
  { modelId: 'kiro-e2e-lite', modelName: 'Kiro E2E Lite', description: 'Mock model without effort', rateMultiplier: 0.4 },
  { modelId: 'kiro-e2e-thinking', modelName: 'Kiro E2E Thinking', description: 'Mock model with independent thinking and effort', rateMultiplier: 1, effortLevels: ['low', 'medium', 'high'], defaultEffort: 'high', thinkingDefault: 'disabled' },
]

/** The session's default model: the first of the catalogue. */
export const KIRO_DEFAULT_MOCK_MODEL = KIRO_MOCK_MODELS[0]!

/** Whether one request uses Kiro's AWS JSON service: a POST that states an operation. */
export function isKiroRequest(request: IncomingMessage): boolean {
  return request.method === 'POST' && typeof request.headers[KIRO_TARGET_HEADER] === 'string'
}

/** The operation of one call: the last segment of `Service.Operation`. */
export function kiroOperation(request: IncomingMessage): string {
  const target = request.headers[KIRO_TARGET_HEADER]
  const value = Array.isArray(target) ? target[0] ?? '' : target ?? ''
  return value.slice(value.lastIndexOf('.') + 1)
}

/** Supply native operation metadata to the neutral HTTP request log. */
export function kiroRequestMetadata(request: IncomingMessage): { operation?: string } {
  const remoteOperation = kiroRemoteOperation(request.url ?? '')
  if (remoteOperation !== undefined)
    return { operation: remoteOperation }
  return isKiroRequest(request) ? { operation: kiroOperation(request) } : {}
}

/** Decode and answer the Kiro service without changing the native conversation body. */
export async function handleKiroHttp(request: IncomingMessage, response: ServerResponse, url: URL, host: MockModelScriptHost): Promise<boolean> {
  const remoteOperation = kiroRemoteOperation(url.pathname)
  if (remoteOperation !== undefined) {
    await handleKiroRemoteRpc(request, response, remoteOperation)
    return true
  }
  if (!isKiroRequest(request))
    return false
  const body = await readJSONBody(request)
  let nativeError: MockModelDeliveredError | undefined
  await serveKiro(request, response, body, {
    answer: async (kiroBody) => {
      const answer = host.select({
        protocol: 'aws-event-stream',
        path: url.pathname,
        body: kiroBody,
        systemText: kiroSystemText(kiroBody),
        userText: kiroUserText(kiroBody),
        scenarioID: kiroScenarioID(kiroBody),
        mockCredential: mockCredentialReceipt(request.headers),
      })
      if (answer.kind === 'missing')
        return answer
      answer.recordHttpResponse(response, () => nativeError)
      if (!await answer.holdStep({ request, response }))
        return { kind: 'abandoned' }
      return { kind: 'step', step: answer.step, stream: answer.stream(response, request) }
    },
    errorResponse: error => (nativeError = error),
  })
  return true
}

/** The answer of `ListAvailableModels`. */
export function kiroModelCatalog() {
  const models = KIRO_MOCK_MODELS.map((model) => {
    const properties = {
      ...(model.effortLevels
        ? { output_config: {
            type: 'object',
            properties: { effort: { type: 'string', enum: [...model.effortLevels], default: model.defaultEffort } },
          } }
        : {}),
      ...(model.thinkingDefault !== undefined
        ? { thinking: {
            type: 'object',
            properties: { type: { type: 'string', enum: [...KIRO_THINKING_TYPES], default: model.thinkingDefault } },
          } }
        : {}),
    }
    return {
      modelId: model.modelId,
      modelName: model.modelName,
      description: model.description,
      rateMultiplier: model.rateMultiplier,
      rateUnit: 'Credit',
      tokenLimits: { maxInputTokens: 200_000, maxOutputTokens: 32_000 },
      supportedInputTypes: ['TEXT', 'IMAGE'],
      ...(Object.keys(properties).length > 0
        ? {
            additionalModelRequestFieldsSchema: {
              type: 'object',
              properties,
            },
          }
        : {}),
    }
  })
  return { models, defaultModel: models[0] }
}

/**
 * The user input message of the current model turn: the user's text, and the context that carries the tool results
 * and the offered tools. Undefined when the body states no current user input.
 */
export function kiroCurrentUserInput(body: unknown): Record<string, unknown> | undefined {
  if (!isObject(body) || !isObject(body.conversationState) || !isObject(body.conversationState.currentMessage))
    return undefined
  const message = body.conversationState.currentMessage.userInputMessage
  return isObject(message) ? message : undefined
}

/** Select from actual user prompts. Native system history and tool results cannot select a scenario. */
function kiroScenarioID(body: unknown): string {
  const prompts: string[] = []
  if (isObject(body) && isObject(body.conversationState) && Array.isArray(body.conversationState.history)) {
    // The first native history entry holds the system prompt, as kiroSystemText documents.
    for (const entry of body.conversationState.history.slice(1)) {
      if (isObject(entry) && isObject(entry.userInputMessage) && typeof entry.userInputMessage.content === 'string')
        prompts.push(entry.userInputMessage.content)
    }
  }
  const current = kiroCurrentUserInput(body)
  if (typeof current?.content === 'string')
    prompts.push(current.content)
  return scenarioIDFromTexts(prompts)
}

/**
 * The text of the newest user turn: the prompt, and the text of each tool result it
 * carries. A turn that continues after a tool call states an empty prompt and the
 * results alone.
 */
export function kiroUserText(body: unknown): string {
  const message = kiroCurrentUserInput(body)
  if (!message)
    return ''
  const parts = [typeof message.content === 'string' ? message.content : '']
  const context = message.userInputMessageContext
  if (isObject(context) && Array.isArray(context.toolResults)) {
    for (const result of context.toolResults) {
      if (isObject(result))
        parts.push(contentText(result.content))
    }
  }
  return parts.filter(Boolean).join('\n')
}

/**
 * The system prompt of one model turn.
 *
 * Kiro sends no system field. It states its system prompt as the first user message
 * of the history, and the model's acknowledgement follows it.
 */
export function kiroSystemText(body: unknown): string {
  if (!isObject(body) || !isObject(body.conversationState) || !Array.isArray(body.conversationState.history))
    return ''
  const first = body.conversationState.history[0]
  return isObject(first) && isObject(first.userInputMessage) ? contentText(first.userInputMessage.content) : ''
}

/** What the scenario machinery answers for one model turn. */
export type KiroTurnAnswer
  = | { kind: 'step', step: MockModelStep, stream?: ModelStream }
    /** No scenario answers. The message states why. */
    | { kind: 'missing', message: string }
    /** The client left while the step was held open. Nothing is written. */
    | { kind: 'abandoned' }

export interface KiroSurfaceOptions {
  /** Choose the answer for one model turn, from its decoded body. */
  answer: (body: unknown) => Promise<KiroTurnAnswer>
  /** The native emitter supplies the code that this response sends. */
  errorResponse?: (error: MockModelDeliveredError) => void
}

/**
 * Answer one call of Kiro's service.
 *
 * `body` is the JSON body of the call. The caller reads it before the call.
 */
async function serveKiro(request: IncomingMessage, response: ServerResponse, body: unknown, options: KiroSurfaceOptions): Promise<void> {
  const operation = kiroOperation(request)
  // Refuse a bearer that differs from the isolated E2E key, including a real login credential.
  // The request log retains the refusal. A request without a credential remains permitted.
  const authorization = request.headers.authorization
  if (authorization !== undefined && authorization !== KIRO_E2E_AUTHORIZATION) {
    writeAwsError(response, 401, 'UnauthorizedException', 'The mock takes only the API key of the E2E environment', options.errorResponse)
    return
  }
  if (operation === KIRO_OPERATION.ListAvailableModels) {
    writeAwsJSON(response, 200, kiroModelCatalog())
    return
  }
  if (operation !== KIRO_OPERATION.GenerateAssistantResponse) {
    writeAwsError(response, 400, 'ValidationException', `The mock does not serve the operation ${operation || '(none)'}`, options.errorResponse)
    return
  }
  const answer = await options.answer(body)
  if (answer.kind === 'abandoned')
    return
  if (answer.kind === 'missing') {
    writeAwsError(response, 409, 'ConflictException', answer.message, options.errorResponse)
    return
  }
  if (answer.step.error) {
    writeAwsError(response, answer.step.error.status, answer.step.error.code ?? 'InternalServerException', answer.step.error.message, options.errorResponse)
    return
  }
  // Validate tool events before sending headers.
  // Return the native AWS error when the step contains a tool that the service cannot represent.
  // Kiro cannot read a generic model error and would repeat that request.
  let toolEvents: KiroEvent[]
  try {
    toolEvents = (answer.step.toolCalls ?? []).flatMap(kiroToolUseEvents)
  }
  catch (error) {
    writeAwsError(response, 400, 'ValidationException', error instanceof Error ? error.message : String(error), options.errorResponse)
    return
  }
  await writeKiroTurn(response, body, answer.step, toolEvents, answer.stream ?? createModelStream(response, answer.step.stream))
}

/** The conversation id one turn states, which the answer echoes in its headers. */
function kiroConversationId(body: unknown): string {
  if (isObject(body) && isObject(body.conversationState) && typeof body.conversationState.conversationId === 'string')
    return body.conversationState.conversationId
  return randomUUID()
}

/**
 * The events of one tool call: the whole input in one piece, then the event that
 * closes the call. Kiro joins the `input` pieces of one `toolUseId` in order.
 */
export function kiroToolUseEvents(tool: MockModelToolCall): KiroEvent[] {
  // Kiro's service has no custom tool or tool namespace.
  // Refuse either shape before sending a response that the client cannot read.
  if (tool.input !== undefined)
    throw new Error(`Kiro's service has no custom tool, so tool call ${tool.name} cannot state raw input`)
  if (tool.namespace !== undefined)
    throw new Error(`Kiro's service has no tool namespace, so tool call ${tool.name} cannot state one`)
  return [
    ['toolUseEvent', { toolUseId: tool.id, name: tool.name, input: JSON.stringify(tool.arguments ?? {}) }],
    ['toolUseEvent', { toolUseId: tool.id, name: tool.name, stop: true }],
  ]
}

/** Write one scripted model turn as an event stream, with the events of its tool calls. */
async function writeKiroTurn(response: ServerResponse, body: unknown, step: MockModelStep, toolEvents: readonly KiroEvent[], stream: ModelStream): Promise<void> {
  const conversationId = kiroConversationId(body)
  writeResponseHeaders(response, 200, {
    'content-type': EVENT_STREAM_CONTENT_TYPE,
    'x-amzn-requestid': randomUUID(),
    'x-amzn-codewhisperer-conversation-id': conversationId,
    'x-amzn-kiro-conversation-id': conversationId,
  })
  for await (const chunk of stream.chunks(step.reasoning))
    response.write(encodeEventStreamEvent('reasoningContentEvent', { text: chunk }))
  for await (const chunk of stream.chunks(step.text)) {
    response.write(encodeEventStreamEvent('assistantResponseEvent', { content: chunk }))
  }
  if (!stream.active)
    return
  for (const [eventType, payload] of toolEvents)
    response.write(encodeEventStreamEvent(eventType, payload))
  response.end(encodeEventStreamEvent('metadataEvent', {
    tokenUsage: { uncachedInputTokens: 1, outputTokens: 1, totalTokens: 2 },
    stopReason: toolEvents.length > 0 ? 'TOOL_USE' : 'END_TURN',
  }))
}

function writeAwsJSON(response: ServerResponse, status: number, value: unknown): void {
  writeResponseHeaders(response, status, { 'content-type': AWS_JSON_CONTENT_TYPE, 'x-amzn-requestid': randomUUID() })
  response.end(Buffer.from(JSON.stringify(value), 'utf8'))
}

/**
 * An AWS JSON 1.0 error: the error type in `x-amzn-errortype` and in `__type`, which
 * the client reads to decide whether to retry.
 */
function writeAwsError(response: ServerResponse, status: number, errorType: string, message: string, observe?: KiroSurfaceOptions['errorResponse']): void {
  writeResponseHeaders(response, status, { 'content-type': AWS_JSON_CONTENT_TYPE, 'x-amzn-errortype': errorType, 'x-amzn-requestid': randomUUID() })
  observe?.({ code: errorType, message })
  response.end(Buffer.from(JSON.stringify({ __type: errorType, message }), 'utf8'))
}
