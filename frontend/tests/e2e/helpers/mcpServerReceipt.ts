/**
 * The one parser of the receipt that a private MCP server writes through `./mcpReceiptRuntime.ts`.
 * A receipt holds the protocol events that a proof reads, and the raw exchange of the messages that the script of the
 * server received or sent.
 */
import { existsSync, readFileSync } from 'node:fs'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'

export type McpRequestId = string | number

export interface McpElicitationRequestReceipt {
  id: McpRequestId
  toolRequestId: McpRequestId
  params: Record<string, unknown>
}

export type McpElicitationReplyReceipt
  = { id: McpRequestId, kind: 'result', result: Record<string, unknown> }
    | { id: McpRequestId, kind: 'error', error: { code: number, message: string, data?: unknown } }

export interface McpFormToolResultReceipt {
  id: McpRequestId
  tool: string
  text: string
  isError: boolean
}

/** One message of the raw exchange, in the direction that the server saw it. */
export type McpExchangeEntry = { received: Record<string, unknown> } | { sent: Record<string, unknown> }

export interface McpServerReceipt {
  initializeCapabilities: Record<string, unknown> | null
  toolCatalogs: Array<{ id: McpRequestId, tools: Array<{ name: string, inputSchema: Record<string, unknown> }> }>
  elicitationRequests: McpElicitationRequestReceipt[]
  elicitationReplies: McpElicitationReplyReceipt[]
  toolResults: McpFormToolResultReceipt[]
  /**
   * Each message that the stdio runtime passed to the script, and each message that the script sent, in order. The
   * runtime drops a notification and answers an invalid envelope itself, so neither appears.
   */
  exchange: McpExchangeEntry[]
}

function record(value: unknown, label: string): Record<string, unknown> {
  if (!isObject(value))
    throw new Error(`The MCP receipt ${label} must be an object.`)
  return value
}

export function mcpReceiptRequestId(value: unknown, label: string): McpRequestId {
  if (typeof value === 'string' || (typeof value === 'number' && Number.isSafeInteger(value)))
    return value
  throw new Error(`The MCP receipt ${label} must be a string or a safe integer.`)
}

function receiptArray<T>(value: unknown, label: string, parse: (item: unknown, label: string) => T): T[] {
  if (!Array.isArray(value))
    throw new Error(`The MCP receipt ${label} must be an array.`)
  return value.map((item, index) => parse(item, `${label}[${index}]`))
}

/** Validate the complete record before a browser test uses it as native evidence. */
export function parseMcpServerReceipt(value: unknown): McpServerReceipt {
  const receipt = record(value, 'root')
  const initializeCapabilities = receipt.initializeCapabilities === null
    ? null
    : record(receipt.initializeCapabilities, 'initializeCapabilities')
  const toolCatalogs = receiptArray(receipt.toolCatalogs, 'toolCatalogs', (item, label) => {
    const catalog = record(item, label)
    return {
      id: mcpReceiptRequestId(catalog.id, `${label}.id`),
      tools: receiptArray(catalog.tools, `${label}.tools`, (value, toolLabel) => {
        const tool = record(value, toolLabel)
        if (typeof tool.name !== 'string' || tool.name.length === 0)
          throw new Error(`The MCP receipt ${toolLabel}.name must identify a tool.`)
        return { name: tool.name, inputSchema: record(tool.inputSchema, `${toolLabel}.inputSchema`) }
      }),
    }
  })
  const elicitationRequests = receiptArray(receipt.elicitationRequests, 'elicitationRequests', (item, label) => {
    const request = record(item, label)
    return {
      id: mcpReceiptRequestId(request.id, `${label}.id`),
      toolRequestId: mcpReceiptRequestId(request.toolRequestId, `${label}.toolRequestId`),
      params: record(request.params, `${label}.params`),
    }
  })
  if (new Set(elicitationRequests.map(request => request.id)).size !== elicitationRequests.length)
    throw new Error('The MCP receipt repeats an elicitation request ID.')
  const elicitationReplies = receiptArray(receipt.elicitationReplies, 'elicitationReplies', (item, label): McpElicitationReplyReceipt => {
    const reply = record(item, label)
    const id = mcpReceiptRequestId(reply.id, `${label}.id`)
    if (reply.kind === 'result')
      return { id, kind: 'result', result: record(reply.result, `${label}.result`) }
    if (reply.kind !== 'error')
      throw new Error(`The MCP receipt ${label}.kind must identify a result or an error.`)
    const error = record(reply.error, `${label}.error`)
    if (typeof error.code !== 'number' || !Number.isSafeInteger(error.code))
      throw new Error(`The MCP receipt ${label}.error.code must be a safe integer.`)
    if (typeof error.message !== 'string')
      throw new Error(`The MCP receipt ${label}.error.message must be a string.`)
    return {
      id,
      kind: 'error',
      error: {
        code: error.code,
        message: error.message,
        ...('data' in error ? { data: error.data } : {}),
      },
    }
  })
  const toolResults = receiptArray(receipt.toolResults, 'toolResults', (item, label) => {
    const result = record(item, label)
    if (typeof result.tool !== 'string' || typeof result.text !== 'string' || typeof result.isError !== 'boolean')
      throw new Error(`The MCP receipt ${label} must contain the tool, text, and error flag.`)
    return { id: mcpReceiptRequestId(result.id, `${label}.id`), tool: result.tool, text: result.text, isError: result.isError }
  })
  const exchange = receiptArray(receipt.exchange, 'exchange', (item, label): McpExchangeEntry => {
    const entry = record(item, label)
    const keys = Object.keys(entry)
    if (keys.length !== 1 || (keys[0] !== 'received' && keys[0] !== 'sent'))
      throw new Error(`The MCP receipt ${label} must hold one received or one sent message.`)
    return 'received' in entry
      ? { received: record(entry.received, `${label}.received`) }
      : { sent: record(entry.sent, `${label}.sent`) }
  })
  return { initializeCapabilities, toolCatalogs, elicitationRequests, elicitationReplies, toolResults, exchange }
}

export function readMcpServerReceipt(path: string): McpServerReceipt {
  const value: unknown = JSON.parse(readFileSync(path, 'utf8'))
  return parseMcpServerReceipt(value)
}

/** Whether a tool catalog of the receipt lists `toolName`. */
export function mcpReceiptListsTool(receipt: Pick<McpServerReceipt, 'toolCatalogs'>, toolName: string): boolean {
  if (toolName.trim() === '')
    throw new Error('The MCP catalog check needs a tool name.')
  return receipt.toolCatalogs.some(catalog => catalog.tools.some(tool => tool.name === toolName))
}

/**
 * Wait until the server of `receiptLog` answered `initialize` and lists `toolName` to an agent.
 *
 * The server writes its receipt when it starts, and an agent starts its servers at a time that the test does not
 * control, so the wait accepts an absent file until the poll ends. The receipt runtime replaces the file through a
 * rename (`./mcpReceiptRuntime.ts`), so a read never finds it half written, and `expect.poll` can read it. The server
 * writes the receipt again after each message, so a receipt can exist before the server lists a tool, and the wait
 * requires the tool, not only the file.
 */
export async function waitForMcpToolListed(receiptLog: string, toolName: string): Promise<void> {
  if (toolName.trim() === '')
    throw new Error('The MCP server check needs the name of a tool.')
  await expect.poll(
    () => existsSync(receiptLog) && mcpReceiptListsTool(readMcpServerReceipt(receiptLog), toolName),
    { message: `the MCP server of ${receiptLog} lists the tool ${toolName}` },
  ).toBe(true)
  expect(readMcpServerReceipt(receiptLog).initializeCapabilities, `the MCP server of ${receiptLog} answered initialize`).not.toBeNull()
}

/** The messages that the server received, in order. */
function receivedMessages(receipt: Pick<McpServerReceipt, 'exchange'>): Record<string, unknown>[] {
  return receipt.exchange.flatMap(entry => 'received' in entry ? [entry.received] : [])
}

/** The replies that the server sent: each sent message that is not a request of its own, in order. */
function sentReplies(receipt: Pick<McpServerReceipt, 'exchange'>): Record<string, unknown>[] {
  return receipt.exchange.flatMap(entry => 'sent' in entry && !Object.hasOwn(entry.sent, 'method') ? [entry.sent] : [])
}

/** One tool call that the server received: the tool name and its exact arguments. */
export interface McpCallArguments {
  name: string
  arguments: Record<string, unknown>
}

/** Read the exact arguments of each tool call that the server received, in call order. */
export function mcpCallArguments(receipt: Pick<McpServerReceipt, 'exchange'>): McpCallArguments[] {
  return receivedMessages(receipt).filter(message => message.method === 'tools/call').map((message) => {
    if (!isObject(message.params) || typeof message.params.name !== 'string' || !isObject(message.params.arguments))
      throw new Error('The native MCP call receipt lacks exact tool arguments.')
    return { name: message.params.name, arguments: message.params.arguments }
  })
}

/** Read the exact arguments of each tool call that the server of `path` received. */
export function readMcpCallArguments(path: string): McpCallArguments[] {
  return mcpCallArguments(readMcpServerReceipt(path))
}

export interface McpCallExchange {
  id: McpRequestId
  name: string
  arguments: Record<string, unknown>
  result: unknown
  request: Record<string, unknown>
  reply: Record<string, unknown>
}

/** Pair the one tool call that the server received with its one exact reply. */
export function mcpCallExchange(receipt: Pick<McpServerReceipt, 'exchange'>): McpCallExchange {
  const calls = receivedMessages(receipt).filter(message => message.method === 'tools/call')
  const request = calls[0]
  if (calls.length !== 1 || !request)
    throw new Error('The native MCP receipt must contain exactly one tool call.')
  const id = mcpReceiptRequestId(request.id, 'tool request ID')
  const params = request.params
  if (!isObject(params) || typeof params.name !== 'string' || params.name.trim().length === 0 || !isObject(params.arguments))
    throw new Error('The native MCP call receipt lacks exact tool arguments.')
  const matchingReplies = sentReplies(receipt).filter(reply => mcpReceiptRequestId(reply.id, 'reply ID') === id)
  const reply = matchingReplies[0]
  if (matchingReplies.length !== 1 || !reply)
    throw new Error('The native MCP tool call must have exactly one matching reply.')
  if (Object.hasOwn(reply, 'error'))
    throw new Error('The native MCP tool call returned a protocol error.')
  if (!Object.hasOwn(reply, 'result') || reply.result === undefined)
    throw new Error('The native MCP tool reply contains no result.')
  return { id, name: params.name, arguments: params.arguments, result: reply.result, request, reply }
}

/** Pair the one tool call that the server of `path` received with its one exact reply. */
export function readMcpCallExchange(path: string): McpCallExchange {
  return mcpCallExchange(readMcpServerReceipt(path))
}

export interface NativeMcpRefusal {
  request: McpElicitationRequestReceipt
  reply: Extract<McpElicitationReplyReceipt, { kind: 'error' }>
  toolResult: McpFormToolResultReceipt
  reason: string
}

/** A negative proof requires a matching native unsupported error and its tool result. */
export function nativeMcpRefusal(receipt: McpServerReceipt): NativeMcpRefusal {
  if (receipt.initializeCapabilities === null)
    throw new Error('The MCP refusal proof has no native initialize capabilities.')
  const request = receipt.elicitationRequests.at(-1)
  if (!request)
    throw new Error('The MCP refusal proof has no actual input request.')
  const reply = receipt.elicitationReplies.findLast(value => value.id === request.id)
  if (!reply)
    throw new Error('The MCP input request has no matching native reply.')
  if (reply.kind !== 'error')
    throw new Error('A declined or accepted MCP result does not prove an unsupported input request.')
  if (reply.error.code !== -32601 && !/unsupported|not support|does not.*elicitation|no .*elicitation|method not found/i.test(reply.error.message))
    throw new Error('The native MCP error does not prove an unsupported input request.')
  const toolResult = receipt.toolResults.findLast(value => value.id === request.toolRequestId && value.tool === 'ask')
  if (!toolResult)
    throw new Error('The refused MCP input has no matching native tool result.')
  const reason = `${reply.error.code} ${reply.error.message}`
  if (!toolResult.isError || toolResult.text !== `FORM_ROUND_TRIP_REFUSED: ${reason}`)
    throw new Error('The native MCP error lost its exact refused tool result.')
  return { request, reply, toolResult, reason }
}

export interface NativeMcpCancellation {
  request: McpElicitationRequestReceipt
  reply: Extract<McpElicitationReplyReceipt, { kind: 'result' }>
  toolResult: McpFormToolResultReceipt
}

/** The text that the form server returns for a cancel reply. */
const FORM_ROUND_TRIP_CANCELLED = 'FORM_ROUND_TRIP_CANCELLED'

/**
 * A negative proof for a client that declares elicitation but cancels the form without input.
 *
 * Such a client shows the form on a surface of its own that cannot reach the
 * browser, for example a terminal form while stdin carries its protocol. The
 * server receives a cancel that holds no content, and its tool result states
 * that cancel.
 */
export function nativeMcpCancellation(receipt: McpServerReceipt): NativeMcpCancellation {
  if (receipt.initializeCapabilities === null)
    throw new Error('The MCP cancellation proof has no native initialize capabilities.')
  if (!('elicitation' in receipt.initializeCapabilities))
    throw new Error('A client that declares no elicitation does not prove a cancelled input request.')
  const request = receipt.elicitationRequests.at(-1)
  if (!request)
    throw new Error('The MCP cancellation proof has no actual input request.')
  const reply = receipt.elicitationReplies.findLast(value => value.id === request.id)
  if (!reply)
    throw new Error('The MCP input request has no matching native reply.')
  if (reply.kind !== 'result' || reply.result.action !== 'cancel')
    throw new Error('Only a native cancel result proves a cancelled input request.')
  if ('content' in reply.result)
    throw new Error('A native cancel result must hold no form content.')
  const toolResult = receipt.toolResults.findLast(value => value.id === request.toolRequestId && value.tool === 'ask')
  if (!toolResult)
    throw new Error('The cancelled MCP input has no matching native tool result.')
  if (toolResult.isError || toolResult.text !== FORM_ROUND_TRIP_CANCELLED)
    throw new Error('The native MCP cancel lost its exact cancelled tool result.')
  return { request, reply, toolResult }
}

export interface NativeMcpUnansweredInput {
  request: McpElicitationRequestReceipt
}

/**
 * A negative proof for a client that never answers an input request.
 *
 * Such a client declares no elicitation capability, the server records the
 * request, and the receipt holds no reply to it. The tool call that waits on
 * the request therefore never completes on the server side either: the client
 * gives up on it with an error of its own, which the caller checks in the
 * model request.
 */
export function nativeMcpUnansweredInput(receipt: McpServerReceipt): NativeMcpUnansweredInput {
  if (receipt.initializeCapabilities === null)
    throw new Error('The unanswered MCP input proof has no native initialize capabilities.')
  if ('elicitation' in receipt.initializeCapabilities)
    throw new Error('A client that declares elicitation does not prove an unanswered input request.')
  const request = receipt.elicitationRequests.at(-1)
  if (!request)
    throw new Error('The unanswered MCP input proof has no actual input request.')
  if (receipt.elicitationReplies.some(reply => reply.id === request.id))
    throw new Error('The native client answered the MCP input request.')
  if (receipt.toolResults.some(result => result.id === request.toolRequestId))
    throw new Error('The MCP server completed the tool call whose input request has no reply.')
  return { request }
}
