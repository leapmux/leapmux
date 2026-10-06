import type { CursorProtobufFields } from './cursorProtobuf'
import type { CursorContextRule } from './cursorWire'
import { cursorProtobufBytes, cursorProtobufNumber, cursorProtobufRepeated, cursorProtobufString, readCursorProtobufFields } from './cursorProtobuf'

export interface CursorRequestedModelWitness {
  modelId: string
  maxMode: boolean
  parameters: Array<{ id: string, value: string }>
  builtInModel: boolean
}

export interface CursorRunRequestWitness {
  requestedModel?: CursorRequestedModelWitness
  modelDetails?: { modelId: string, maxMode?: boolean }
  mode?: number
  /** The rules that the user attached to the message. A project's own rules are not among them. */
  cursorRules?: Array<{ path: string, content: string }>
  /**
   * The rules that the CLI stated in its answer to a request context query, which is
   * where it states the project rules it loaded. Absent until a spec asks for them.
   */
  contextRules?: CursorContextRule[]
}

function message(fields: CursorProtobufFields, field: number): CursorProtobufFields | undefined {
  const bytes = cursorProtobufBytes(fields, field)
  return bytes !== undefined ? readCursorProtobufFields(bytes) : undefined
}

function booleanField(fields: CursorProtobufFields, field: number): boolean | undefined {
  const value = fields.integers.get(field)
  if (value === undefined)
    return undefined
  if (value !== 0n && value !== 1n)
    throw new Error('The native Cursor request boolean must be zero or one.')
  return value === 1n
}

/** Read actual native settings from AgentClientMessage.run_request and its nested UserMessage. */
export function cursorRunRequestWitness(frame: Uint8Array): CursorRunRequestWitness | undefined {
  const run = message(readCursorProtobufFields(frame), 1)
  if (!run)
    return undefined
  const witness: CursorRunRequestWitness = {}
  const requested = message(run, 9)
  if (requested) {
    witness.requestedModel = {
      modelId: cursorProtobufString(requested, 1) ?? '',
      maxMode: booleanField(requested, 2) ?? false,
      parameters: cursorProtobufRepeated(requested, 3).map((bytes) => {
        const parameter = readCursorProtobufFields(bytes)
        return { id: cursorProtobufString(parameter, 1) ?? '', value: cursorProtobufString(parameter, 2) ?? '' }
      }),
      builtInModel: booleanField(requested, 7) ?? false,
    }
  }
  const details = message(run, 3)
  if (details) {
    const maxMode = booleanField(details, 7)
    witness.modelDetails = { modelId: cursorProtobufString(details, 1) ?? '', ...(maxMode !== undefined ? { maxMode } : {}) }
  }
  const action = message(run, 2)
  const userAction = action && message(action, 1)
  const user = userAction && message(userAction, 1)
  if (user) {
    const mode = cursorProtobufNumber(user, 4, true)
    if (mode === undefined && user.integers.has(4))
      throw new Error('The native Cursor request mode is invalid.')
    // AgentMode uses proto3 defaults: 0 UNSPECIFIED, 1 AGENT, 2 ASK, and 3 PLAN.
    witness.mode = mode ?? 0
    const selected = message(user, 3)
    if (selected) {
      witness.cursorRules = cursorProtobufRepeated(selected, 10).map((bytes) => {
        const wrapper = readCursorProtobufFields(bytes)
        const rule = message(wrapper, 1)
        if (!rule)
          throw new Error('The native Cursor selected rule contains no rule message.')
        return { path: cursorProtobufString(rule, 1) ?? '', content: cursorProtobufString(rule, 2) ?? '' }
      })
    }
  }
  return witness
}
