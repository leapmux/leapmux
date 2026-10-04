import { Buffer } from 'node:buffer'
import { encodeLengthDelimited, encodeStringField, encodeVarint } from './cursorWire'

export function cursorSubagentReplyFixture(id: number, outcome: Uint8Array, execID = 'native-task'): Uint8Array {
  const envelope = Buffer.concat([
    Buffer.from(encodeVarint(8)),
    Buffer.from(encodeVarint(id)),
    Buffer.from(encodeStringField(15, execID)),
    Buffer.from(encodeLengthDelimited(28, outcome)),
  ])
  return encodeLengthDelimited(2, envelope)
}

export function cursorSubagentSuccessFixture(options: { agentID?: string, finalMessage?: string, toolCallCount?: number } = {}): Uint8Array {
  return encodeLengthDelimited(1, new Uint8Array(Buffer.concat([
    ...(options.agentID !== undefined ? [Buffer.from(encodeStringField(1, options.agentID))] : []),
    ...(options.finalMessage !== undefined ? [Buffer.from(encodeStringField(2, options.finalMessage))] : []),
    ...(options.toolCallCount !== undefined ? [Buffer.from(encodeVarint(24)), Buffer.from(encodeVarint(options.toolCallCount))] : []),
  ])))
}
