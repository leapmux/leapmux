import type { CursorExecutionCall, CursorExecutionReply } from './cursorWire'
import type { MockModelToolCall } from './mockModelScript'
import { cursorExecutionRequest, cursorExecutionResponseOf, cursorExecutionToolCompleted, cursorExecutionToolStarted } from './cursorWire'

export interface CursorExecutionReceipt {
  callId: string
  kind: CursorExecutionCall['kind']
  success: boolean
  text: string
  exitCode?: number
}

export type CursorExecutionAdvance
  = { state: 'request', request: Uint8Array }
    | { state: 'completed', update: Uint8Array, receipt: CursorExecutionReceipt }

type FileStage = 'before' | 'write' | 'after'

/** The service requests real client execution and never performs local filesystem or shell work. */
export class CursorExecution {
  readonly started: Uint8Array
  private current: CursorExecutionCall
  private expectedId: number
  private stage: FileStage | null
  private before = ''
  private after = ''
  private completed = false

  constructor(private readonly call: CursorExecutionCall, private readonly nextId: () => number) {
    this.started = cursorExecutionToolStarted(call)
    this.stage = call.kind === 'write' || call.kind === 'edit' ? 'before' : null
    this.current = call.kind === 'write' || call.kind === 'edit' ? { kind: 'read', callID: call.callID, path: call.path } : call
    this.expectedId = nextId()
  }

  request(): Uint8Array {
    if (this.completed)
      throw new Error('The native Cursor execution already completed.')
    return cursorExecutionRequest(this.expectedId, this.current)
  }

  acceptReply(frame: Uint8Array): CursorExecutionAdvance | undefined {
    const reply = cursorExecutionResponseOf(frame)
    if (!reply)
      return undefined
    if (this.completed)
      throw new Error('The native Cursor execution received another completed reply.')
    if (reply.id !== this.expectedId)
      throw new Error(`Cursor replied to execution ${this.expectedId} with id ${reply.id}.`)
    if (reply.execID !== undefined && reply.execID !== this.call.callID)
      throw new Error('The native Cursor execution reply has a different tool call ID.')
    if (reply.kind !== this.current.kind)
      throw new Error('The native Cursor execution reply has a different operation.')

    if (this.stage === 'before') {
      if (reply.success) {
        if (reply.content === undefined)
          throw new Error('The native Cursor edit read returned no text content.')
        this.before = reply.content
      }
      else if (this.call.kind === 'write' && reply.failureKind === 'file-not-found') {
        this.before = ''
      }
      else {
        return this.finish(reply)
      }
      if (this.call.kind === 'edit') {
        const first = this.before.indexOf(this.call.before)
        const last = this.before.lastIndexOf(this.call.before)
        if (this.call.before === '' || first < 0 || first !== last) {
          return this.finish({ ...reply, success: false, text: 'The native edit target is absent or not unique.', failureKind: 'invalid-edit' })
        }
        this.after = this.before.slice(0, first) + this.call.after + this.before.slice(first + this.call.before.length)
      }
      else if (this.call.kind === 'write') {
        this.after = this.call.content
      }
      else {
        throw new Error('The native Cursor file stage has a non-file operation.')
      }
      this.stage = 'write'
      this.current = { kind: 'write', callID: this.call.callID, path: this.filePath(), content: this.after }
      this.expectedId = this.nextId()
      return { state: 'request', request: this.request() }
    }

    if (this.stage === 'write') {
      if (!reply.success)
        return this.finish(reply)
      if (reply.content !== undefined) {
        if (reply.content !== this.after)
          throw new Error('The native Cursor write returned different file contents.')
        return this.finish(reply)
      }
      this.stage = 'after'
      this.current = { kind: 'read', callID: this.call.callID, path: this.filePath() }
      this.expectedId = this.nextId()
      return { state: 'request', request: this.request() }
    }

    if (this.stage === 'after') {
      if (!reply.success)
        return this.finish(reply)
      if (reply.content !== this.after)
        throw new Error('The native Cursor read after write returned different file contents.')
      return this.finish(reply)
    }
    return this.finish(reply)
  }

  private filePath(): string {
    if (this.call.kind === 'shell')
      throw new Error('The native Cursor shell has no file path.')
    return this.call.path
  }

  private finish(reply: CursorExecutionReply): CursorExecutionAdvance {
    this.completed = true
    const receipt: CursorExecutionReceipt = {
      callId: this.call.callID,
      kind: this.call.kind,
      success: reply.success,
      text: reply.text,
      ...(reply.exitCode !== undefined ? { exitCode: reply.exitCode } : {}),
    }
    return {
      state: 'completed',
      update: cursorExecutionToolCompleted(this.call, reply, { before: this.before, after: reply.content ?? this.after }),
      receipt,
    }
  }
}

/** Validate one scripted operation before its native client request starts. */
export function cursorExecutionCallFrom(call: MockModelToolCall, kind: CursorExecutionCall['kind']): CursorExecutionCall {
  const args = call.arguments
  if (!call.id || !args)
    throw new Error('The native Cursor execution requires an ID and arguments.')
  if (kind === 'shell') {
    if (typeof args.command !== 'string' || args.command.length === 0)
      throw new Error('The native Cursor shell requires a command.')
    const workingDirectory = typeof args.workingDirectory === 'string' ? args.workingDirectory : undefined
    return { kind, callID: call.id, command: args.command, ...(workingDirectory !== undefined ? { workingDirectory } : {}) }
  }
  if (typeof args.path !== 'string' || args.path.length === 0)
    throw new Error('The native Cursor file operation requires a path.')
  if (kind === 'read')
    return { kind, callID: call.id, path: args.path }
  if (kind === 'write') {
    if (typeof args.content !== 'string')
      throw new Error('The native Cursor write requires file contents.')
    return { kind, callID: call.id, path: args.path, content: args.content }
  }
  if (typeof args.before !== 'string' || typeof args.after !== 'string')
    throw new Error('The native Cursor edit requires before and after text.')
  return { kind, callID: call.id, path: args.path, before: args.before, after: args.after }
}
