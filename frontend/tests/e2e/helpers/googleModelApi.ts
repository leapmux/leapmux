import type { IncomingMessage, ServerResponse } from 'node:http'
import type { MockModelScriptHost } from './mockModelRequest'
import type { MockModelStep } from './mockModelScript'
import type { ModelStream } from './modelStream'
import { Buffer } from 'node:buffer'
import { isObject } from '../../../src/lib/jsonPick'
import { googleLastUserText, googlePartsText } from './googleModelContent'
import { mockCredentialReceipt } from './mockCredentials'
import { holdOpen, readJSONBody, writeMockJSON } from './mockHttp'
import { rateLimitHeaders } from './mockRateLimitHeaders'
import { bufferModelOutput } from './modelStream'
import { writeResponseHeaders } from './responseHeaders'

const MODEL_ROUTE = /^\/v1beta\/models\/([\w.-]+):(streamGenerateContent|generateContent|countTokens)$/

function writeGoogleError(response: ServerResponse, code: number, status: string, message: string): void {
  writeMockJSON(response, code, { error: { code, status, message } })
}

/** Handle the native Google API through the shared scenario and accounting host. */
export async function handleGoogleModelHttp(request: IncomingMessage, response: ServerResponse, url: URL, host: MockModelScriptHost): Promise<boolean> {
  const route = MODEL_ROUTE.exec(url.pathname)
  if (!route)
    return false
  try {
    await handleGoogleRequest(request, response, url, route[1]!, route[2]!, host)
  }
  catch (error) {
    if (response.headersSent)
      response.destroy(error instanceof Error ? error : new Error(String(error)))
    else
      writeGoogleError(response, 400, 'INVALID_ARGUMENT', error instanceof Error ? error.message : String(error))
  }
  return true
}

async function handleGoogleRequest(request: IncomingMessage, response: ServerResponse, url: URL, model: string, operation: string, host: MockModelScriptHost): Promise<void> {
  if (request.method !== 'POST') {
    writeGoogleError(response, 405, 'INVALID_ARGUMENT', 'The Google model route requires POST.')
    return
  }
  const credential = mockCredentialReceipt(request.headers, url.searchParams.getAll('key'))
  if (!credential.accepted) {
    writeGoogleError(response, 401, 'UNAUTHENTICATED', 'The Google model route requires a mock credential.')
    return
  }
  const body = await readJSONBody(request)
  if (!isObject(body) || !Array.isArray(body.contents))
    throw new Error('The Google model request requires a contents array.')
  if (operation === 'countTokens') {
    // This estimate supplies metadata. It never consumes a scripted content turn.
    const totalTokens = body.contents.length === 0 ? 0 : Math.ceil(Buffer.byteLength(JSON.stringify(body.contents), 'utf8') / 4)
    writeMockJSON(response, 200, { totalTokens })
    return
  }
  const answer = host.select({
    protocol: 'google-generative-language',
    path: url.pathname,
    body,
    systemText: isObject(body.systemInstruction) ? googlePartsText(body.systemInstruction.parts) : '',
    userText: googleLastUserText(body.contents),
    mockCredential: credential,
  })
  if (answer.kind === 'missing') {
    writeGoogleError(response, 409, 'FAILED_PRECONDITION', answer.message)
    return
  }
  const { step } = answer
  answer.recordHttpResponse(response, () => step.error ? { code: step.error.code ?? 'UNKNOWN', message: step.error.message } : undefined)
  if (step.gate && !await answer.holdGate(step.gate, { request, response }))
    return
  if (step.delayMs && !await holdOpen(request, response, step.delayMs))
    return
  if (step.error) {
    writeGoogleError(response, step.error.status, step.error.code ?? 'UNKNOWN', step.error.message)
    return
  }
  if (step.toolCalls?.some(tool => tool.input !== undefined || tool.namespace !== undefined))
    throw new Error('The Google model route accepts JSON function calls only.')
  await writeGoogleResponse(response, operation === 'streamGenerateContent', model, step, answer.stream(response, request))
}

function googleToolParts(step: MockModelStep): Record<string, unknown>[] {
  return step.toolCalls?.map(tool => ({ functionCall: { id: tool.id, name: tool.name, args: tool.arguments ?? {} } })) ?? []
}

function googleUsage(step: MockModelStep) {
  const input = step.usage?.inputTokens ?? 1
  const output = step.usage?.outputTokens ?? 1
  return { promptTokenCount: input, candidatesTokenCount: output, totalTokenCount: input + output }
}

function googleResponse(model: string, parts: Record<string, unknown>[], step?: MockModelStep) {
  return {
    candidates: [{ index: 0, content: { role: 'model', parts }, ...(step ? { finishReason: 'STOP' } : {}) }],
    modelVersion: model,
    ...(step ? { usageMetadata: googleUsage(step) } : {}),
  }
}

async function writeGoogleResponse(response: ServerResponse, streaming: boolean, model: string, step: MockModelStep, stream: ModelStream): Promise<void> {
  const tools = googleToolParts(step)
  if (!streaming) {
    if (!await bufferModelOutput(stream, step))
      return
    const parts = [
      ...(step.reasoning === undefined ? [] : [{ text: step.reasoning, thought: true }]),
      ...(step.text === undefined ? [] : [{ text: step.text }]),
      ...tools,
    ]
    writeMockJSON(response, 200, googleResponse(model, parts, step), rateLimitHeaders(step))
    return
  }
  writeResponseHeaders(response, 200, { 'content-type': 'text/event-stream; charset=utf-8', 'cache-control': 'no-cache', ...rateLimitHeaders(step) })
  const emit = (parts: Record<string, unknown>[], final?: MockModelStep) => response.write(`data: ${JSON.stringify(googleResponse(model, parts, final))}\n\n`)
  for await (const text of stream.chunks(step.reasoning))
    emit([{ text, thought: true }])
  for await (const text of stream.chunks(step.text))
    emit([{ text }])
  if (!stream.active)
    return
  emit(tools, step)
  response.end()
}
