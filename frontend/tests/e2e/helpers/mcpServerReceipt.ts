import { readFileSync } from 'node:fs'
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

export interface McpServerReceipt {
  initializeCapabilities: Record<string, unknown> | null
  toolCatalogs: Array<{ id: McpRequestId, tools: Array<{ name: string, inputSchema: Record<string, unknown> }> }>
  elicitationRequests: McpElicitationRequestReceipt[]
  elicitationReplies: McpElicitationReplyReceipt[]
  toolResults: McpFormToolResultReceipt[]
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
  return { initializeCapabilities, toolCatalogs, elicitationRequests, elicitationReplies, toolResults }
}

export function readMcpServerReceipt(path: string): McpServerReceipt {
  const value: unknown = JSON.parse(readFileSync(path, 'utf8'))
  return parseMcpServerReceipt(value)
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
