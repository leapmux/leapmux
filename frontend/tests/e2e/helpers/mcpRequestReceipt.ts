import type { McpRequestId } from './mcpServerReceipt'
import { readFileSync } from 'node:fs'
import { isObject } from '../../../src/lib/jsonPick'
import { mcpReceiptRequestId } from './mcpServerReceipt'

/** Read exact MCP call arguments from the existing server receipt. */
export function readMcpCallArguments(path: string): Record<string, unknown>[] {
  const receipt: unknown = JSON.parse(readFileSync(path, 'utf8'))
  if (!Array.isArray(receipt))
    throw new Error('The native MCP server receipt must contain an entry list.')
  const calls: Record<string, unknown>[] = []
  for (const entry of receipt) {
    if (!isObject(entry))
      throw new Error('The native MCP server receipt contains an invalid entry.')
    if (entry.request === undefined)
      continue
    if (!isObject(entry.request))
      throw new Error('The native MCP server receipt contains an invalid request.')
    if (entry.request.method !== 'tools/call')
      continue
    if (!isObject(entry.request.params) || typeof entry.request.params.name !== 'string' || !isObject(entry.request.params.arguments))
      throw new Error('The native MCP call receipt lacks exact tool arguments.')
    calls.push({ name: entry.request.params.name, arguments: entry.request.params.arguments })
  }
  return calls
}

export interface McpCallExchange {
  id: McpRequestId
  name: string
  arguments: Record<string, unknown>
  result: unknown
  request: Record<string, unknown>
  reply: Record<string, unknown>
}

/** Pair one actual MCP tool request with its exact server reply. */
export function parseMcpCallExchange(value: unknown): McpCallExchange {
  if (!Array.isArray(value))
    throw new Error('The native MCP server receipt must contain an entry list.')
  const calls: Record<string, unknown>[] = []
  const replies: Record<string, unknown>[] = []
  for (const entry of value) {
    if (!isObject(entry))
      throw new Error('The native MCP server receipt contains an invalid entry.')
    if (entry.request !== undefined) {
      const request = entry.request
      if (!isObject(request))
        throw new Error('The native MCP server receipt contains an invalid request.')
      if (request.method === 'tools/call')
        calls.push(request)
    }
    if (entry.reply !== undefined) {
      const reply = entry.reply
      if (!isObject(reply))
        throw new Error('The native MCP server receipt contains an invalid reply.')
      replies.push(reply)
    }
  }
  const request = calls[0]
  if (calls.length !== 1 || !request)
    throw new Error('The native MCP receipt must contain exactly one tool call.')
  const id = mcpReceiptRequestId(request.id, 'tool request ID')
  const params = request.params
  if (!isObject(params) || typeof params.name !== 'string' || params.name.trim().length === 0 || !isObject(params.arguments))
    throw new Error('The native MCP call receipt lacks exact tool arguments.')
  const matchingReplies = replies.filter(reply => mcpReceiptRequestId(reply.id, 'reply ID') === id)
  const reply = matchingReplies[0]
  if (matchingReplies.length !== 1 || !reply)
    throw new Error('The native MCP tool call must have exactly one matching reply.')
  if (Object.hasOwn(reply, 'error'))
    throw new Error('The native MCP tool call returned a protocol error.')
  if (!Object.hasOwn(reply, 'result') || reply.result === undefined)
    throw new Error('The native MCP tool reply contains no result.')
  return { id, name: params.name, arguments: params.arguments, result: reply.result, request, reply }
}

/** Read the existing server receipt without reading a native output file. */
export function readMcpCallExchange(path: string): McpCallExchange {
  const value: unknown = JSON.parse(readFileSync(path, 'utf8'))
  return parseMcpCallExchange(value)
}
