/**
 * The Kiro half of the mock endpoint.
 *
 * Kiro speaks neither the OpenAI nor the Anthropic model API. Its engine calls its
 * own service in AWS JSON 1.0: every call is `POST /`, the header `X-Amz-Target`
 * states the operation, and a model turn answers with an AWS event stream (see
 * `./awsEventStream`). This module answers that surface, and hands the turn it
 * decodes to the same scenario machinery every other provider uses.
 *
 * THREE FACTS DECIDE THE SHAPE HERE, and the research probes established each one.
 *
 * Every answer to a model turn ENDS with a `metadataEvent` that states a stop reason.
 * Kiro's engine reads an answer without one as a cut stream, and it sends the same
 * request again -- which consumes the next scripted step.
 *
 * The model catalogue comes from `ListAvailableModels`, not from the shared catalog
 * route, so it lists the models of `KIRO_MOCK_MODELS` alone. Without an answer the
 * session has no model at all.
 *
 * Every other operation may FAIL. Kiro falls back to defaults for its feature
 * configuration, its remote web tools and its usage limits, so a 400 is the whole
 * answer.
 */
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { MockModelStep, MockModelToolCall } from './mockModelScript'
import { Buffer } from 'node:buffer'
import { randomUUID } from 'node:crypto'
import { encodeEventStreamEvent, EVENT_STREAM_CONTENT_TYPE } from './awsEventStream'
import { KIRO_E2E_API_KEY } from './mockAgentEnvironment'
import { contentText, isRecord, textChunks } from './mockModelScript'
import { pauseBetweenChunks } from './responsePause'

/** The header that states the operation of an AWS JSON 1.0 call. */
export const KIRO_TARGET_HEADER = 'x-amz-target'

/** The content type of an AWS JSON 1.0 answer. */
const AWS_JSON_CONTENT_TYPE = 'application/x-amz-json-1.0'

/** The one credential that the mock takes, as Kiro states it in `Authorization`. */
const KIRO_E2E_AUTHORIZATION = `Bearer ${KIRO_E2E_API_KEY}`

/** One event of a model turn: its type, and its JSON payload. */
type KiroEvent = [eventType: string, payload: Record<string, unknown>]

/** The operations the surface answers, by the last segment of `X-Amz-Target`. */
export const KIRO_OPERATION = {
  GenerateAssistantResponse: 'GenerateAssistantResponse',
  ListAvailableModels: 'ListAvailableModels',
} as const

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
}

/**
 * The model catalogue the mock reports.
 *
 * Two models, so a settings spec can switch between them: the first takes an effort,
 * and the second takes none, so the switch also removes the effort axis. Kiro reads
 * an effort axis from `additionalModelRequestFieldsSchema.output_config.effort`, and
 * then states the chosen level to the model under `additionalModelRequestFields`.
 */
export const KIRO_MOCK_MODELS: readonly KiroMockModel[] = [
  { modelId: 'kiro-e2e', modelName: 'Kiro E2E', description: 'Mock model with effort', rateMultiplier: 1, effortLevels: ['low', 'medium', 'high'], defaultEffort: 'high' },
  { modelId: 'kiro-e2e-lite', modelName: 'Kiro E2E Lite', description: 'Mock model without effort', rateMultiplier: 0.4 },
]

/** The session's default model: the first of the catalogue. */
export const KIRO_DEFAULT_MOCK_MODEL = KIRO_MOCK_MODELS[0]!

/** Whether one request is a call of Kiro's service: a POST that states an operation. */
export function isKiroRequest(request: IncomingMessage): boolean {
  return request.method === 'POST' && typeof request.headers[KIRO_TARGET_HEADER] === 'string'
}

/** The operation of one call: the last segment of `Service.Operation`. */
export function kiroOperation(request: IncomingMessage): string {
  const target = request.headers[KIRO_TARGET_HEADER]
  const value = Array.isArray(target) ? target[0] ?? '' : target ?? ''
  return value.slice(value.lastIndexOf('.') + 1)
}

/** The answer of `ListAvailableModels`. */
export function kiroModelCatalog(): Record<string, unknown> {
  const models = KIRO_MOCK_MODELS.map(model => ({
    modelId: model.modelId,
    modelName: model.modelName,
    description: model.description,
    rateMultiplier: model.rateMultiplier,
    rateUnit: 'Credit',
    tokenLimits: { maxInputTokens: 200_000, maxOutputTokens: 32_000 },
    supportedInputTypes: ['TEXT', 'IMAGE'],
    ...(model.effortLevels
      ? {
          additionalModelRequestFieldsSchema: {
            type: 'object',
            properties: {
              output_config: {
                type: 'object',
                properties: { effort: { type: 'string', enum: [...model.effortLevels], default: model.defaultEffort } },
              },
            },
          },
        }
      : {}),
  }))
  return { models, defaultModel: models[0] }
}

/** The current message of one model turn: the user's text and its tool results. */
function kiroCurrentMessage(body: unknown): Record<string, unknown> | undefined {
  if (!isRecord(body) || !isRecord(body.conversationState) || !isRecord(body.conversationState.currentMessage))
    return undefined
  const message = body.conversationState.currentMessage.userInputMessage
  return isRecord(message) ? message : undefined
}

/**
 * The text of the newest user turn: the prompt, and the text of each tool result it
 * carries. A turn that continues after a tool call states an empty prompt and the
 * results alone.
 */
export function kiroUserText(body: unknown): string {
  const message = kiroCurrentMessage(body)
  if (!message)
    return ''
  const parts = [typeof message.content === 'string' ? message.content : '']
  const context = message.userInputMessageContext
  if (isRecord(context) && Array.isArray(context.toolResults)) {
    for (const result of context.toolResults) {
      if (isRecord(result))
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
  if (!isRecord(body) || !isRecord(body.conversationState) || !Array.isArray(body.conversationState.history))
    return ''
  const first = body.conversationState.history[0]
  return isRecord(first) && isRecord(first.userInputMessage) ? contentText(first.userInputMessage.content) : ''
}

/** What the scenario machinery answers for one model turn. */
export type KiroTurnAnswer
  = | { kind: 'step', step: MockModelStep }
    /** No scenario answers. The message states why. */
    | { kind: 'missing', message: string }
    /** The client left while the step was held open. Nothing is written. */
    | { kind: 'abandoned' }

export interface KiroSurfaceOptions {
  /** Choose the answer for one model turn, from its decoded body. */
  answer: (body: unknown) => Promise<KiroTurnAnswer>
}

/**
 * Answer one call of Kiro's service.
 *
 * `body` is the JSON body of the call. The caller reads it before the call.
 */
export async function serveKiro(request: IncomingMessage, response: ServerResponse, body: unknown, options: KiroSurfaceOptions): Promise<void> {
  const operation = kiroOperation(request)
  // A bearer other than the key of the E2E environment is a credential that the
  // environment did not set, such as a real login. The refusal puts it in the
  // request log. A call that states no credential sends nothing of the developer's,
  // so it passes.
  const authorization = request.headers.authorization
  if (authorization !== undefined && authorization !== KIRO_E2E_AUTHORIZATION) {
    writeAwsError(response, 401, 'UnauthorizedException', 'The mock takes only the API key of the E2E environment')
    return
  }
  if (operation === KIRO_OPERATION.ListAvailableModels) {
    writeAwsJSON(response, 200, kiroModelCatalog())
    return
  }
  if (operation !== KIRO_OPERATION.GenerateAssistantResponse) {
    writeAwsError(response, 400, 'ValidationException', `The mock does not serve the operation ${operation || '(none)'}`)
    return
  }
  const answer = await options.answer(body)
  if (answer.kind === 'abandoned')
    return
  if (answer.kind === 'missing') {
    writeAwsError(response, 409, 'ConflictException', answer.message)
    return
  }
  if (answer.step.error) {
    writeAwsError(response, answer.step.error.status, answer.step.error.code ?? 'InternalServerException', answer.step.error.message)
    return
  }
  // The events of the tool calls exist before the head goes out. A step that Kiro's
  // service cannot state then fails the call with its reason, in the AWS error shape
  // that Kiro reads. The generic handler of the mock answers a shape that Kiro
  // cannot read, and Kiro would send the call again with no reason.
  let toolEvents: KiroEvent[]
  try {
    toolEvents = (answer.step.toolCalls ?? []).flatMap(kiroToolUseEvents)
  }
  catch (error) {
    writeAwsError(response, 400, 'ValidationException', error instanceof Error ? error.message : String(error))
    return
  }
  await writeKiroTurn(response, body, answer.step, toolEvents)
}

/** The conversation id one turn states, which the answer echoes in its headers. */
function kiroConversationId(body: unknown): string {
  if (isRecord(body) && isRecord(body.conversationState) && typeof body.conversationState.conversationId === 'string')
    return body.conversationState.conversationId
  return randomUUID()
}

/**
 * The events of one tool call: the whole input in one piece, then the event that
 * closes the call. Kiro joins the `input` pieces of one `toolUseId` in order.
 */
export function kiroToolUseEvents(tool: MockModelToolCall): KiroEvent[] {
  // Kiro's service has no custom tool and no tool namespace. A step that states
  // either cannot be expressed here, so say that rather than send a shape the
  // client will misread.
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
async function writeKiroTurn(response: ServerResponse, body: unknown, step: MockModelStep, toolEvents: readonly KiroEvent[]): Promise<void> {
  const conversationId = kiroConversationId(body)
  response.writeHead(200, {
    'content-type': EVENT_STREAM_CONTENT_TYPE,
    'x-amzn-requestid': randomUUID(),
    'x-amzn-codewhisperer-conversation-id': conversationId,
    'x-amzn-kiro-conversation-id': conversationId,
  })
  if (step.reasoning !== undefined)
    response.write(encodeEventStreamEvent('reasoningContentEvent', { text: step.reasoning }))
  for (const [index, chunk] of textChunks(step).entries()) {
    if (index > 0)
      await pauseBetweenChunks(response, step.stream?.delayMs ?? 0)
    if (response.writableEnded || response.destroyed)
      return
    response.write(encodeEventStreamEvent('assistantResponseEvent', { content: chunk }))
  }
  for (const [eventType, payload] of toolEvents)
    response.write(encodeEventStreamEvent(eventType, payload))
  response.end(encodeEventStreamEvent('metadataEvent', {
    tokenUsage: { uncachedInputTokens: 1, outputTokens: 1, totalTokens: 2 },
    stopReason: toolEvents.length > 0 ? 'TOOL_USE' : 'END_TURN',
  }))
}

function writeAwsJSON(response: ServerResponse, status: number, value: unknown): void {
  response.writeHead(status, { 'content-type': AWS_JSON_CONTENT_TYPE, 'x-amzn-requestid': randomUUID() })
  response.end(Buffer.from(JSON.stringify(value), 'utf8'))
}

/**
 * An AWS JSON 1.0 error: the error type in `x-amzn-errortype` and in `__type`, which
 * the client reads to decide whether to retry.
 */
function writeAwsError(response: ServerResponse, status: number, errorType: string, message: string): void {
  response.writeHead(status, { 'content-type': AWS_JSON_CONTENT_TYPE, 'x-amzn-errortype': errorType, 'x-amzn-requestid': randomUUID() })
  response.end(Buffer.from(JSON.stringify({ __type: errorType, message }), 'utf8'))
}
