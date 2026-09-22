/**
 * The wire format `cursor-agent` speaks, in the small part of it a mock needs.
 *
 * Cursor's CLI does not use an OpenAI or Anthropic model API. It talks to its
 * own backend over Connect, and its whole turn travels on ONE bidirectional
 * stream: `POST /agent.v1.AgentService/Run`, over HTTP/2, as
 * `application/connect+proto`. Every other call the CLI makes at startup accepts
 * an all-defaults answer, so this module covers the stream alone.
 *
 * WHY A HAND-WRITTEN CODEC. The schema belongs to Cursor, not to this project:
 * `proto/` states LeapMux's own contracts and `contracts/` states values that
 * cross a language boundary, and a reverse-engineered third-party schema is
 * neither. The surface is also tiny -- three message types with one or two
 * fields each to write, and a five-field path to read -- so the field numbers
 * below are stated once, with their source, rather than generated.
 *
 * WHERE THE NUMBERS COME FROM. The CLI bundle ships `@bufbuild/protobuf` field
 * tables as plain object literals, at
 * `~/.local/share/cursor-agent/versions/<version>/index.js`, each reading
 * `X.typeName="agent.v1.Foo",X.fields=…newFieldList(()=>[{no,name,kind,T}])`.
 * Resolve a minified type reference by searching for the local whose `typeName`
 * MATCHES THE TYPE, never by nearest occurrence -- a one-character local repeats
 * in every neighbouring module, and nearest-match confidently returns the wrong
 * type.
 */

/** Protobuf wire type 2: a length-delimited field. */
const WIRE_LENGTH_DELIMITED = 2

/** `agent.v1.AgentClientMessage.run_request`, the client's whole turn request. */
const FIELD_RUN_REQUEST = 1
/** `agent.v1.AgentRunRequest.action`. */
const FIELD_ACTION = 2
/** `agent.v1.ConversationAction.user_message_action`. */
const FIELD_USER_MESSAGE_ACTION = 1
/** `agent.v1.UserMessageAction.user_message`. */
const FIELD_USER_MESSAGE = 1
/** `agent.v1.UserMessage.text`, and `agent.v1.TextDeltaUpdate.text`. */
const FIELD_TEXT = 1

/** `agent.v1.AgentServerMessage.interaction_update`. */
const FIELD_INTERACTION_UPDATE = 1
/** `agent.v1.InteractionUpdate.text_delta`. */
const FIELD_TEXT_DELTA = 1
/** `agent.v1.InteractionUpdate.turn_ended`. */
const FIELD_TURN_ENDED = 14

/**
 * The path from one `AgentClientMessage` down to the prompt the user sent.
 *
 * Every step is a length-delimited message field, so the walk needs no schema
 * beyond these numbers: a field number and a wire type are enough to descend
 * through a message that declares 37 fields this module never names.
 */
const PROMPT_PATH = [FIELD_RUN_REQUEST, FIELD_ACTION, FIELD_USER_MESSAGE_ACTION, FIELD_USER_MESSAGE, FIELD_TEXT]

/** A base-128 varint: seven bits per byte, high bit set on every byte but the last. */
export function encodeVarint(value: number): Uint8Array {
  if (!Number.isInteger(value) || value < 0)
    throw new Error(`A varint takes a non-negative integer, not ${value}`)
  const bytes: number[] = []
  let rest = value
  do {
    const byte = rest & 0x7F
    rest = Math.floor(rest / 128)
    bytes.push(rest > 0 ? byte | 0x80 : byte)
  } while (rest > 0)
  return Uint8Array.from(bytes)
}

/** One length-delimited field: its tag, its length, then the payload. */
export function encodeLengthDelimited(fieldNumber: number, payload: Uint8Array): Uint8Array {
  const tag = encodeVarint((fieldNumber << 3) | WIRE_LENGTH_DELIMITED)
  const length = encodeVarint(payload.byteLength)
  const out = new Uint8Array(tag.byteLength + length.byteLength + payload.byteLength)
  out.set(tag, 0)
  out.set(length, tag.byteLength)
  out.set(payload, tag.byteLength + length.byteLength)
  return out
}

/** One length-delimited field carrying UTF-8 text. */
export function encodeStringField(fieldNumber: number, value: string): Uint8Array {
  return encodeLengthDelimited(fieldNumber, new TextEncoder().encode(value))
}

function concat(parts: readonly Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((total, part) => total + part.byteLength, 0))
  let at = 0
  for (const part of parts) {
    out.set(part, at)
    at += part.byteLength
  }
  return out
}

/**
 * Every LENGTH-DELIMITED field of one message, by field number.
 *
 * A field of any other wire type is SKIPPED rather than refused: this reads one
 * path out of a message whose other fields it deliberately does not declare, so
 * an unknown field is the normal case and not an error. A repeated field keeps
 * every occurrence, in order.
 */
export function readLengthDelimitedFields(bytes: Uint8Array): Map<number, Uint8Array[]> {
  const found = new Map<number, Uint8Array[]>()
  let at = 0
  const readVarint = (): number => {
    let result = 0
    let shift = 0
    while (at < bytes.length) {
      const byte = bytes[at++]!
      result += (byte & 0x7F) * 2 ** shift
      if ((byte & 0x80) === 0)
        return result
      shift += 7
    }
    // A truncated varint ends the walk; the caller sees the fields read so far.
    return result
  }
  while (at < bytes.length) {
    const tag = readVarint()
    const fieldNumber = tag >>> 3
    switch (tag & 7) {
      // A varint, whose value this reader never needs but must step over.
      case 0:
        readVarint()
        break
      // A 64-bit fixed-width field.
      case 1:
        at += 8
        break
      case WIRE_LENGTH_DELIMITED: {
        const length = readVarint()
        const value = bytes.subarray(at, at + length)
        at += length
        const list = found.get(fieldNumber)
        if (list)
          list.push(value)
        else
          found.set(fieldNumber, [value])
        break
      }
      // A 32-bit fixed-width field.
      case 5:
        at += 4
        break
      // A group, or a wire type this format never defines. Neither appears here,
      // and reading on would misalign every field after it, so stop.
      default: at = bytes.length
    }
  }
  return found
}

/** Follow a chain of length-delimited field numbers, taking the first of each. */
export function descend(bytes: Uint8Array, path: readonly number[]): Uint8Array | undefined {
  let current: Uint8Array | undefined = bytes
  for (const fieldNumber of path) {
    if (!current)
      return undefined
    current = readLengthDelimitedFields(current).get(fieldNumber)?.[0]
  }
  return current
}

/**
 * The prompt inside one `AgentClientMessage`, or undefined when it carries none.
 *
 * Only the message that OPENS a turn holds one. The others -- a heartbeat, a
 * tool result, an interaction response -- answer undefined, which is how a
 * caller tells the turn's first frame from the rest of the stream.
 */
export function cursorPromptOf(clientMessage: Uint8Array): string | undefined {
  const text = descend(clientMessage, PROMPT_PATH)
  return text === undefined ? undefined : new TextDecoder().decode(text)
}

/** An `AgentServerMessage` carrying one piece of assistant text. */
export function cursorTextDelta(text: string): Uint8Array {
  return encodeLengthDelimited(
    FIELD_INTERACTION_UPDATE,
    encodeLengthDelimited(FIELD_TEXT_DELTA, encodeStringField(FIELD_TEXT, text)),
  )
}

/**
 * The `AgentServerMessage` that ends a turn.
 *
 * `TurnEndedUpdate` declares five token counts and every one of them is
 * optional, so the message an agent needs is EMPTY: the whole update encodes as
 * a tag and a zero length.
 */
export function cursorTurnEnded(): Uint8Array {
  return encodeLengthDelimited(FIELD_INTERACTION_UPDATE, encodeLengthDelimited(FIELD_TURN_ENDED, new Uint8Array(0)))
}

/**
 * `aiserver.v1.AvailableModelsResponse.models`, the catalogue the model picker reads.
 *
 * These numbers come from the RESPONSE BYTES, not from the bundle's field tables: a
 * recorded `AvailableModels` body decodes field by field, and each field lines up with the
 * name at the same position in the parsed object the CLI then holds. That is the only
 * source that covers a message the bundle itself never has to encode.
 */
const FIELD_MODELS = 2

/** `aiserver.v1.AvailableModel`, in the fields a picker entry needs. */
const FIELD_MODEL_NAME = 1
const FIELD_MODEL_DEFAULT_ON = 2
const FIELD_MODEL_SUPPORTS_AGENT = 5
const FIELD_MODEL_CLIENT_DISPLAY_NAME = 17
const FIELD_MODEL_SERVER_MODEL_NAME = 18
const FIELD_MODEL_SUPPORTS_NON_MAX_MODE = 19
const FIELD_MODEL_SUPPORTS_PLAN_MODE = 22
const FIELD_MODEL_VARIANTS = 30
const FIELD_MODEL_ID_ALIASES = 37

/** `aiserver.v1.ModelVariant`, likewise. */
const FIELD_VARIANT_DISPLAY_NAME = 2
const FIELD_VARIANT_IS_DEFAULT_MAX_CONFIG = 4
const FIELD_VARIANT_IS_DEFAULT_NON_MAX_CONFIG = 5
const FIELD_VARIANT_STRING_REPRESENTATION = 9

/** One selectable entry in Cursor's model picker. */
export interface CursorModelVariant {
  /**
   * The BRACKETED wire id, e.g. `grok-4.7[context=256k,reasoning_effort=low]`.
   *
   * Cursor bakes a variant's whole metadata into this id and reports it nowhere else, and
   * LeapMux parses it back out for the context window and the effort level. A variant
   * whose id carries no brackets therefore renders with no metadata, which is valid.
   */
  id: string
  displayName: string
  /** Marks the entry the agent starts on. At most one variant in a catalogue sets it. */
  isDefault?: boolean
}

/** One model in Cursor's catalogue, with the variants it explodes into. */
export interface CursorModel {
  /** The bare, bracket-less id, which is what the server reports as a model's name. */
  name: string
  displayName: string
  variants: readonly CursorModelVariant[]
  /** Alternative ids that select this model; Cursor's own `default` answers to `auto`. */
  aliases?: readonly string[]
}

/** One boolean field. Protobuf omits a false, so this writes the tag only for a true. */
function encodeBoolField(fieldNumber: number, value: boolean): Uint8Array {
  if (!value)
    return new Uint8Array(0)
  return concat([encodeVarint(fieldNumber << 3), encodeVarint(1)])
}

function encodeCursorVariant(variant: CursorModelVariant): Uint8Array {
  return concat([
    encodeStringField(FIELD_VARIANT_DISPLAY_NAME, variant.displayName),
    encodeBoolField(FIELD_VARIANT_IS_DEFAULT_MAX_CONFIG, variant.isDefault === true),
    encodeBoolField(FIELD_VARIANT_IS_DEFAULT_NON_MAX_CONFIG, variant.isDefault === true),
    encodeStringField(FIELD_VARIANT_STRING_REPRESENTATION, variant.id),
  ])
}

function encodeCursorModel(model: CursorModel): Uint8Array {
  return concat([
    encodeStringField(FIELD_MODEL_NAME, model.name),
    encodeBoolField(FIELD_MODEL_DEFAULT_ON, true),
    encodeBoolField(FIELD_MODEL_SUPPORTS_AGENT, true),
    encodeStringField(FIELD_MODEL_CLIENT_DISPLAY_NAME, model.displayName),
    encodeStringField(FIELD_MODEL_SERVER_MODEL_NAME, model.name),
    encodeBoolField(FIELD_MODEL_SUPPORTS_NON_MAX_MODE, true),
    encodeBoolField(FIELD_MODEL_SUPPORTS_PLAN_MODE, true),
    ...model.variants.map(variant => encodeLengthDelimited(FIELD_MODEL_VARIANTS, encodeCursorVariant(variant))),
    ...(model.aliases ?? []).map(alias => encodeStringField(FIELD_MODEL_ID_ALIASES, alias)),
  ])
}

/**
 * `aiserver.v1.ModelDetails`, which is a DIFFERENT message from the catalogue entry above.
 *
 * These names come from the bundle's own compact schema string:
 * `ModelDetails|1 model_id 9|3 display_model_id 9|4 display_name 9|5 display_name_short 9|
 * 6 aliases 9*|...`. Two calls answer with it -- `GetUsableModelsResponse|1 models #0*` and
 * `GetDefaultModelForCliResponse|1 model #0` -- so one encoder serves both.
 */
const FIELD_DETAILS_MODEL_ID = 1
const FIELD_DETAILS_DISPLAY_MODEL_ID = 3
const FIELD_DETAILS_DISPLAY_NAME = 4
const FIELD_DETAILS_DISPLAY_NAME_SHORT = 5
const FIELD_DETAILS_ALIASES = 6

/** `GetUsableModelsResponse.models`, and `GetDefaultModelForCliResponse.model`. */
const FIELD_MODEL_DETAILS = 1

function encodeCursorModelDetails(model: CursorModel): Uint8Array {
  return concat([
    encodeStringField(FIELD_DETAILS_MODEL_ID, model.name),
    encodeStringField(FIELD_DETAILS_DISPLAY_MODEL_ID, model.aliases?.[0] ?? model.name),
    encodeStringField(FIELD_DETAILS_DISPLAY_NAME, model.displayName),
    encodeStringField(FIELD_DETAILS_DISPLAY_NAME_SHORT, model.displayName),
    ...(model.aliases ?? []).map(alias => encodeStringField(FIELD_DETAILS_ALIASES, alias)),
  ])
}

/**
 * A `GetDefaultModelForCliResponse` naming this model.
 *
 * Without it the agent starts with no model selected and refuses `session/new`
 * outright: "No model found. Please check your model settings." A catalogue on
 * its own is not enough, because nothing in it says which entry to begin on. A
 * config directory that a previous run already wrote hides this, which is why
 * it reproduces only on a FRESH `CURSOR_CONFIG_DIR` -- the E2E's normal state.
 */
export function cursorDefaultModel(model: CursorModel): Uint8Array {
  return encodeLengthDelimited(FIELD_MODEL_DETAILS, encodeCursorModelDetails(model))
}

/** A `GetUsableModelsResponse` listing these models. */
export function cursorUsableModels(models: readonly CursorModel[]): Uint8Array {
  return concat(models.map(model => encodeLengthDelimited(FIELD_MODEL_DETAILS, encodeCursorModelDetails(model))))
}

/**
 * An `AvailableModelsResponse` carrying this catalogue.
 *
 * A variant reaches the picker only when it states BOTH a display name and a variant
 * string, because the CLI drops one that is missing either. An empty catalogue then reads
 * as "the endpoint answered" rather than as a fault, which is why this is the one startup
 * call the mock cannot answer all-defaults. See the note in `./cursorSurface`.
 */
export function cursorAvailableModels(models: readonly CursorModel[]): Uint8Array {
  return concat(models.map(model => encodeLengthDelimited(FIELD_MODELS, encodeCursorModel(model))))
}

/**
 * The two `InteractionUpdate` cases that carry a tool call.
 *
 * `InteractionUpdate|1 text_delta|2 tool_call_started|3 tool_call_completed|
 * 14 turn_ended|...`, and both updates share a shape:
 * `ToolCallStartedUpdate|1 call_id 9|2 tool_call #0|3 model_call_id 9`.
 */
const FIELD_TOOL_CALL_STARTED = 2
const FIELD_TOOL_CALL_COMPLETED = 3
const FIELD_UPDATE_CALL_ID = 1
const FIELD_UPDATE_TOOL_CALL = 2

/**
 * `agent.v1.ToolCall`, a union of 69 tools. Only the Task one matters here.
 *
 * Resolving `#14` took the class whose own `typeName` says `ToolCall` and whose
 * schema lists `19 task_tool_call`, NOT the nearest class of that minified name
 * -- two other classes share it, and one of them is an unrelated `ToolCall`.
 */
const FIELD_TOOLCALL_TASK = 19
const FIELD_TOOLCALL_TOOL_CALL_ID = 57

/** `TaskToolCall|1 args #0|2 result #1|3 cloud_agent_bc_id 9?`. */
const FIELD_TASK_ARGS = 1
const FIELD_TASK_RESULT = 2

/**
 * `TaskArgs|1 description 9|2 prompt 9|3 subagent_type #0|4 model 9?|...`.
 *
 * NOT `SubagentArgs`, which is a different message with `tool_call_id` at field
 * 1. Encoding this as that one put a string where a message belongs and the CLI
 * refused the whole frame: `parse binary: illegal tag: field no 12 wire type 6`.
 */
const FIELD_ARGS_DESCRIPTION = 1
const FIELD_ARGS_PROMPT = 2
const FIELD_ARGS_SUBAGENT_TYPE = 3

/**
 * `SubagentType|1 unspecified|2 computer_use|3 custom|4 explore|...`.
 *
 * A union of EMPTY marker messages, so the type is the field number alone.
 */
const FIELD_SUBAGENT_TYPE_EXPLORE = 4

/** `TaskResult|1 success #0 result|2 error #1 result`. */
const FIELD_RESULT_SUCCESS = 1

/**
 * `TaskSuccess|1 conversation_steps #0*|2 agent_id 9?|3 is_background 8|
 * 4 duration_ms 4?|5 result_suffix 9?|6 background_reason #1|7 transcript_path 9?`.
 */
const FIELD_SUCCESS_CONVERSATION_STEPS = 1
const FIELD_SUCCESS_AGENT_ID = 2
const FIELD_SUCCESS_RESULT_SUFFIX = 5

/**
 * `ConversationStep|1 assistant_message #0|2 tool_call #1|3 thinking_message #2`
 * and `AssistantMessage|1 text 9|2 started_at_ms 4?|3 completed_at_ms 4?`.
 *
 * The steps ARE the child's transcript. `result_suffix` alone left the row
 * complete with an empty child transcript, because that field is a suffix on
 * the parent's own text and not the child's answer.
 */
const FIELD_STEP_ASSISTANT_MESSAGE = 1
const FIELD_ASSISTANT_MESSAGE_TEXT = 1

/** One Task tool call for the mock to put on the Run stream. */
export interface CursorTaskCall {
  /** The tool-call id, which reaches the agent as the ACP `toolCallId`. */
  callID: string
  /** The row's label. The agent titles the call `Task: <description>`. */
  description: string
  /** The child's task. */
  prompt: string
}

function encodeCursorTask(call: CursorTaskCall, report: string | undefined): Uint8Array {
  const args = concat([
    encodeStringField(FIELD_ARGS_DESCRIPTION, call.description),
    encodeStringField(FIELD_ARGS_PROMPT, call.prompt),
    encodeLengthDelimited(
      FIELD_ARGS_SUBAGENT_TYPE,
      encodeLengthDelimited(FIELD_SUBAGENT_TYPE_EXPLORE, new Uint8Array(0)),
    ),
  ])
  const task = [encodeLengthDelimited(FIELD_TASK_ARGS, args)]
  if (report !== undefined) {
    task.push(encodeLengthDelimited(
      FIELD_TASK_RESULT,
      encodeLengthDelimited(FIELD_RESULT_SUCCESS, concat([
        encodeLengthDelimited(
          FIELD_SUCCESS_CONVERSATION_STEPS,
          encodeLengthDelimited(
            FIELD_STEP_ASSISTANT_MESSAGE,
            encodeStringField(FIELD_ASSISTANT_MESSAGE_TEXT, report),
          ),
        ),
        encodeStringField(FIELD_SUCCESS_AGENT_ID, `${call.callID}-child`),
        encodeStringField(FIELD_SUCCESS_RESULT_SUFFIX, report),
      ])),
    ))
  }
  return concat([
    encodeLengthDelimited(FIELD_TOOLCALL_TASK, concat(task)),
    encodeStringField(FIELD_TOOLCALL_TOOL_CALL_ID, call.callID),
  ])
}

function encodeToolCallUpdate(field: number, call: CursorTaskCall, report: string | undefined): Uint8Array {
  return encodeLengthDelimited(FIELD_INTERACTION_UPDATE, encodeLengthDelimited(field, concat([
    encodeStringField(FIELD_UPDATE_CALL_ID, call.callID),
    encodeLengthDelimited(FIELD_UPDATE_TOOL_CALL, encodeCursorTask(call, report)),
  ])))
}

/**
 * The update that OPENS a Task tool call.
 *
 * The agent turns this into an ACP `tool_call` notification whose `rawInput`
 * carries `_toolName: "task"` and whose title is `Task: <description>`, which is
 * what the subagent registry reads.
 */
export function cursorTaskStarted(call: CursorTaskCall): Uint8Array {
  return encodeToolCallUpdate(FIELD_TOOL_CALL_STARTED, call, undefined)
}

/**
 * The update that CLOSES a Task tool call, carrying the child's report.
 *
 * It repeats the arguments, because both updates carry a whole `ToolCall`. The
 * agent answers with a `tool_call_update` that moves the row to `completed`.
 */
export function cursorTaskCompleted(call: CursorTaskCall, report: string): Uint8Array {
  return encodeToolCallUpdate(FIELD_TOOL_CALL_COMPLETED, call, report)
}

/**
 * The KV channel, which is how the BACKEND writes the session transcript.
 *
 * `AgentServerMessage|1 interaction_update|...|4 kv_server_message`, and
 * `KvServerMessage|1 id 13|2 get_blob_args|3 set_blob_args`, whose payload is
 * `SetBlobArgs|1 blob_id 12|2 blob_data 12` -- both BYTES.
 *
 * This is the piece a mock cannot leave out. An interaction update draws the
 * turn on screen, but it writes NOTHING to disk: the CLI opens
 * `acp-sessions/<id>/store.db` and then only READS metadata from it. The server
 * pushes each conversation message over this channel, the client writes it into
 * `blobs`, and LeapMux reads the tool result back out of that table -- which is
 * the report ACP itself omits. A stream with no KV messages leaves the store
 * file absent, so the report never arrives.
 */
const FIELD_KV_SERVER_MESSAGE = 4
const FIELD_KV_ID = 1
const FIELD_KV_SET_BLOB_ARGS = 3
const FIELD_SET_BLOB_ID = 1
const FIELD_SET_BLOB_DATA = 2

/** One varint field: its tag, then its value. */
function encodeVarintField(fieldNumber: number, value: number): Uint8Array {
  return concat([encodeVarint(fieldNumber << 3), encodeVarint(value)])
}

/**
 * An `AgentServerMessage` that stores one blob in the session's database.
 *
 * `blobID` reaches the table as lowercase hex, which is why the real ids are 64
 * characters: the client hex-encodes these 32 bytes.
 */
export function cursorSetBlob(messageID: number, blobID: Uint8Array, data: Uint8Array): Uint8Array {
  return encodeLengthDelimited(FIELD_KV_SERVER_MESSAGE, concat([
    encodeVarintField(FIELD_KV_ID, messageID),
    encodeLengthDelimited(FIELD_KV_SET_BLOB_ARGS, concat([
      encodeLengthDelimited(FIELD_SET_BLOB_ID, blobID),
      encodeLengthDelimited(FIELD_SET_BLOB_DATA, data),
    ])),
  ]))
}

/** The end-of-stream flag on a Connect frame header. */
const CONNECT_END_OF_STREAM = 0x02

/** One Connect stream frame: a flag byte, a big-endian length, the payload. */
export function connectFrame(payload: Uint8Array, flags = 0): Uint8Array {
  const header = new Uint8Array(5)
  header[0] = flags
  new DataView(header.buffer).setUint32(1, payload.byteLength, false)
  return concat([header, payload])
}

/** The frame that closes a Connect stream, whose payload is the trailers as JSON. */
export function connectEndOfStream(trailers: Record<string, unknown> = {}): Uint8Array {
  return connectFrame(new TextEncoder().encode(JSON.stringify(trailers)), CONNECT_END_OF_STREAM)
}

/**
 * Split a Connect frame stream into payloads, keeping any partial tail.
 *
 * A reader calls this on every chunk and carries `rest` into the next one: one
 * frame can arrive in several TCP segments, and a length prefix read out of a
 * half-received header would take an arbitrary number as the frame's size.
 */
export function takeConnectFrames(buffer: Uint8Array): { frames: Uint8Array[], rest: Uint8Array } {
  const frames: Uint8Array[] = []
  const view = new DataView(buffer.buffer, buffer.byteOffset, buffer.byteLength)
  let at = 0
  while (buffer.byteLength - at >= 5) {
    const length = view.getUint32(at + 1, false)
    if (buffer.byteLength - at - 5 < length)
      break
    frames.push(buffer.subarray(at + 5, at + 5 + length))
    at += 5 + length
  }
  return { frames, rest: buffer.subarray(at) }
}
