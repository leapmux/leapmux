import type { CursorTaskCall } from './cursorWire'
import { Buffer } from 'node:buffer'
import { cursorProtobufNumber, cursorProtobufString, readCursorProtobufFields } from './cursorProtobuf'
import { encodeLengthDelimited, encodeStringField, encodeVarint } from './cursorWire'

function concat(parts: readonly Uint8Array[]): Uint8Array {
  return new Uint8Array(Buffer.concat(parts.map(part => Buffer.from(part))))
}

function encodeVarintField(field: number, value: number): Uint8Array {
  return concat([encodeVarint(field * 8), encodeVarint(value)])
}

export interface CursorSubagentExecutionCall extends CursorTaskCall {
  modelID: string
  parentConversationID: string
  readonly?: boolean
}

export type CursorSubagentExecutionReply = {
  id: number
  execID?: string
  rawResult: Uint8Array
} & (
  | { success: true, agentID: string, finalMessage?: string, toolCallCount?: number }
  | { success: false, agentID?: string, error: string }
)

/** Request actual child execution from the native Cursor client. */
export function cursorSubagentExecutionRequest(id: number, call: CursorSubagentExecutionCall): Uint8Array {
  if (!Number.isSafeInteger(id) || id < 0 || id > 0xFFFF_FFFF)
    throw new Error('The native Cursor subagent execution ID must fit uint32.')
  if (!call.callID || !call.modelID || !call.parentConversationID || !call.prompt)
    throw new Error('The native Cursor child requires its tool, model, parent, and prompt.')
  const args = concat([
    encodeStringField(1, call.callID),
    encodeStringField(2, 'explore'),
    encodeStringField(3, call.modelID),
    encodeStringField(4, call.prompt),
    encodeVarintField(5, call.readonly === false ? 0 : 1),
    encodeStringField(9, call.parentConversationID),
    encodeStringField(16, call.parentConversationID),
  ])
  return encodeLengthDelimited(2, concat([
    encodeVarintField(1, id),
    encodeStringField(15, call.callID),
    encodeLengthDelimited(28, args),
  ]))
}

/** Decode only the actual native subagent reply from an execution frame. */
export function cursorSubagentExecutionResponseOf(clientMessage: Uint8Array): CursorSubagentExecutionReply | undefined {
  const outer = readCursorProtobufFields(clientMessage)
  const envelope = outer.strings.get(2)?.at(-1)
  if (!envelope)
    return undefined
  const fields = readCursorProtobufFields(envelope)
  const rawResult = fields.strings.get(28)?.at(-1)
  if (!rawResult)
    return undefined
  const id = cursorProtobufNumber(fields, 1)
  if (id === undefined || id < 0 || id > 0xFFFF_FFFF)
    throw new Error('The native Cursor subagent reply has no valid execution ID.')
  const execID = cursorProtobufString(fields, 15)
  const result = readCursorProtobufFields(rawResult)
  const success = result.strings.get(1)?.at(-1)
  const error = result.strings.get(2)?.at(-1)
  if (success !== undefined && error !== undefined)
    throw new Error('The native Cursor subagent reply repeats its result choice.')
  const common = { id, rawResult, ...(execID !== undefined ? { execID } : {}) }
  if (success !== undefined) {
    const values = readCursorProtobufFields(success)
    const agentID = cursorProtobufString(values, 1)
    if (!agentID)
      throw new Error('The native Cursor child success has no child ID.')
    const finalMessage = cursorProtobufString(values, 2)
    const toolCallCount = cursorProtobufNumber(values, 3, true)
    if (toolCallCount !== undefined && toolCallCount < 0)
      throw new Error('The native Cursor child tool count is negative.')
    return { ...common, success: true, agentID, ...(finalMessage !== undefined ? { finalMessage } : {}), ...(toolCallCount !== undefined ? { toolCallCount } : {}) }
  }
  if (error !== undefined) {
    const values = readCursorProtobufFields(error)
    const message = cursorProtobufString(values, 2)
    if (message === undefined)
      throw new Error('The native Cursor child error has no message.')
    const agentID = cursorProtobufString(values, 1)
    return { ...common, success: false, error: message, ...(agentID !== undefined ? { agentID } : {}) }
  }
  throw new Error('The native Cursor subagent reply has no result.')
}

/** Complete a Task from its actual native child reply without a derived child ID. */
export function cursorTaskCompletedFromNativeReply(call: CursorTaskCall, reply: CursorSubagentExecutionReply): Uint8Array {
  const args = concat([
    encodeStringField(1, call.description),
    encodeStringField(2, call.prompt),
    encodeLengthDelimited(3, encodeLengthDelimited(4, new Uint8Array())),
    ...(reply.agentID !== undefined ? [encodeStringField(6, reply.agentID)] : []),
  ])
  const result = reply.success
    ? encodeLengthDelimited(1, concat([
        ...(reply.finalMessage !== undefined ? [encodeLengthDelimited(1, encodeLengthDelimited(1, encodeStringField(1, reply.finalMessage)))] : []),
        encodeStringField(2, reply.agentID),
        encodeVarintField(3, 0),
      ]))
    : encodeLengthDelimited(2, encodeStringField(1, reply.error))
  const tool = concat([
    encodeLengthDelimited(19, concat([encodeLengthDelimited(1, args), encodeLengthDelimited(2, result)])),
    encodeStringField(57, call.callID),
  ])
  return encodeLengthDelimited(1, encodeLengthDelimited(3, concat([encodeStringField(1, call.callID), encodeLengthDelimited(2, tool)])))
}
