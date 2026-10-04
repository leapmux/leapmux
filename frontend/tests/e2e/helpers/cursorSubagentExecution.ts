import type { CursorSubagentExecutionCall, CursorSubagentExecutionReply } from './cursorSubagentWire'
import { cursorSubagentExecutionRequest, cursorSubagentExecutionResponseOf, cursorTaskCompletedFromNativeReply } from './cursorSubagentWire'
import { cursorTaskStarted } from './cursorWire'

/** Retain only the actual client result for one native child execution. */
export class CursorSubagentExecution {
  readonly started: Uint8Array
  private completed = false

  constructor(private readonly call: CursorSubagentExecutionCall, private readonly id: number) {
    this.started = cursorTaskStarted(call)
  }

  request(): Uint8Array {
    if (this.completed)
      throw new Error('The native Cursor child execution already completed.')
    return cursorSubagentExecutionRequest(this.id, this.call)
  }

  acceptReply(frame: Uint8Array): { update: Uint8Array, reply: CursorSubagentExecutionReply } | undefined {
    const reply = cursorSubagentExecutionResponseOf(frame)
    if (!reply)
      return undefined
    if (this.completed)
      throw new Error('The native Cursor child execution received another completed reply.')
    if (reply.id !== this.id || (reply.execID !== undefined && reply.execID !== this.call.callID))
      throw new Error('The native Cursor child reply has a different execution or tool ID.')
    this.completed = true
    return { update: cursorTaskCompletedFromNativeReply(this.call, reply), reply }
  }
}
