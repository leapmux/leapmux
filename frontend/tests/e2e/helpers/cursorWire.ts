import type { JsonValue } from '@bufbuild/protobuf'
import type { CursorProtobufFields } from './cursorProtobuf'
import { Buffer } from 'node:buffer'
import { fromJson, toBinary } from '@bufbuild/protobuf'
import { ValueSchema } from '@bufbuild/protobuf/wkt'
import {
  concatBytes,
  cursorProtobufBytes,
  cursorProtobufNumber,
  cursorProtobufRepeated,
  cursorProtobufString,
  descend,
  encodeBoolField,
  encodeLengthDelimited,
  encodeStringField,
  encodeVarintField,
  readCursorProtobufFields,
} from './cursorProtobuf'

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
 * below are stated once, with their source, rather than generated. The
 * schema-free encoders and the reader live in `./cursorProtobuf`.
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

/** `agent.v1.AgentClientMessage.run_request`, the client's whole turn request. */
const FIELD_RUN_REQUEST = 1
/** `agent.v1.AgentRunRequest.conversation_id`. */
const FIELD_CONVERSATION_ID = 5
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
/** `agent.v1.InteractionUpdate.thinking_delta`. */
const FIELD_THINKING_DELTA = 4
/** `agent.v1.InteractionUpdate.turn_ended`. */
const FIELD_TURN_ENDED = 14
/** `agent.v1.TurnEndedUpdate.input_tokens` and `.output_tokens`. */
const FIELD_INPUT_TOKENS = 1
const FIELD_OUTPUT_TOKENS = 2

/**
 * The path from one `AgentClientMessage` down to the prompt the user sent.
 *
 * Every step is a length-delimited message field, so the walk needs no schema
 * beyond these numbers: a field number and a wire type are enough to descend
 * through a message that declares 37 fields this module never names.
 */
const PROMPT_PATH = [FIELD_RUN_REQUEST, FIELD_ACTION, FIELD_USER_MESSAGE_ACTION, FIELD_USER_MESSAGE, FIELD_TEXT]
const SELECTED_CONTEXT_PATH = [FIELD_RUN_REQUEST, FIELD_ACTION, FIELD_USER_MESSAGE_ACTION, FIELD_USER_MESSAGE, 3]

export interface CursorAttachmentPayload {
  kind: 'image' | 'document' | 'file'
  data: string
  mimeType?: string
  filename?: string
}

/**
 * A read of an arbitrary client frame, which keeps the fields before a malformed
 * byte rather than failing. Each decoder here searches a frame of the shared Run
 * stream, where a heartbeat or a frame of another kind is the normal case.
 */
function lenientFields(bytes: Uint8Array): CursorProtobufFields {
  return readCursorProtobufFields(bytes, { lenient: true })
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

/** Read the native conversation ID that Cursor sends with a Run request. */
export function cursorConversationIdOf(clientMessage: Uint8Array): string | undefined {
  const id = descend(clientMessage, [FIELD_RUN_REQUEST, FIELD_CONVERSATION_ID])
  return id === undefined ? undefined : new TextDecoder().decode(id)
}

/** Read the inline attachment bytes that Cursor puts in a Run request. */
export function cursorAttachmentPayloads(clientMessage: Uint8Array): CursorAttachmentPayload[] {
  const context = descend(clientMessage, SELECTED_CONTEXT_PATH)
  if (!context)
    return []
  const fields = lenientFields(context)
  const payloads: CursorAttachmentPayload[] = []
  for (const image of cursorProtobufRepeated(fields, 1)) {
    const parts = lenientFields(image)
    const data = cursorProtobufBytes(parts, 8) ?? descend(image, [9, 2])
    const mimeType = cursorProtobufString(parts, 7)
    if (data) {
      payloads.push({
        kind: 'image',
        data: Buffer.from(data).toString('base64'),
        ...(mimeType === undefined ? {} : { mimeType }),
      })
    }
  }
  for (const document of cursorProtobufRepeated(fields, 25)) {
    const parts = lenientFields(document)
    const data = cursorProtobufBytes(parts, 8) ?? descend(document, [9, 2])
    const mimeType = cursorProtobufString(parts, 4)
    const filename = cursorProtobufString(parts, 3)
    if (data) {
      payloads.push({
        kind: 'document',
        data: Buffer.from(data).toString('base64'),
        ...(mimeType === undefined ? {} : { mimeType }),
        ...(filename === undefined ? {} : { filename }),
      })
    }
  }
  for (const file of cursorProtobufRepeated(fields, 4)) {
    const parts = lenientFields(file)
    const data = cursorProtobufString(parts, 1)
    const filename = cursorProtobufString(parts, 2)
    if (data !== undefined)
      payloads.push({ kind: 'file', data, ...(filename === undefined ? {} : { filename }) })
  }
  return payloads
}

/** An `AgentServerMessage` carrying one piece of assistant text. */
export function cursorTextDelta(text: string): Uint8Array {
  return encodeLengthDelimited(
    FIELD_INTERACTION_UPDATE,
    encodeLengthDelimited(FIELD_TEXT_DELTA, encodeStringField(FIELD_TEXT, text)),
  )
}

/** An `AgentServerMessage` carrying one piece of native thinking text. */
export function cursorThinkingDelta(text: string): Uint8Array {
  return encodeLengthDelimited(
    FIELD_INTERACTION_UPDATE,
    encodeLengthDelimited(FIELD_THINKING_DELTA, encodeStringField(FIELD_TEXT, text)),
  )
}

/**
 * The `AgentServerMessage` that ends a turn.
 *
 * `TurnEndedUpdate` declares five optional token counts. State the two counts
 * that a scripted model reports, and leave the others absent.
 */
export function cursorTurnEnded(usage?: { inputTokens?: number, outputTokens?: number }): Uint8Array {
  const counts: Uint8Array[] = []
  if (usage?.inputTokens !== undefined)
    counts.push(encodeVarintField(FIELD_INPUT_TOKENS, usage.inputTokens))
  if (usage?.outputTokens !== undefined)
    counts.push(encodeVarintField(FIELD_OUTPUT_TOKENS, usage.outputTokens))
  return encodeLengthDelimited(FIELD_INTERACTION_UPDATE, encodeLengthDelimited(FIELD_TURN_ENDED, concatBytes(counts)))
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
  /** The picker ID, such as `grok-4.7[context=256k,reasoning_effort=low]`. */
  id: string
  displayName: string
  /** Native model parameters that the CLI sends on its next Run request. */
  parameters?: readonly { id: string, value: string }[]
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

function encodeCursorVariant(variant: CursorModelVariant): Uint8Array {
  return concatBytes([
    ...(variant.parameters ?? []).map(parameter => encodeLengthDelimited(1, concatBytes([
      encodeStringField(1, parameter.id),
      encodeStringField(2, parameter.value),
    ]))),
    encodeStringField(FIELD_VARIANT_DISPLAY_NAME, variant.displayName),
    encodeBoolField(FIELD_VARIANT_IS_DEFAULT_MAX_CONFIG, variant.isDefault === true),
    encodeBoolField(FIELD_VARIANT_IS_DEFAULT_NON_MAX_CONFIG, variant.isDefault === true),
    encodeStringField(FIELD_VARIANT_STRING_REPRESENTATION, variant.id),
  ])
}

function encodeCursorModel(model: CursorModel): Uint8Array {
  return concatBytes([
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
  return concatBytes([
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
  return concatBytes(models.map(model => encodeLengthDelimited(FIELD_MODEL_DETAILS, encodeCursorModelDetails(model))))
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
  return concatBytes(models.map(model => encodeLengthDelimited(FIELD_MODELS, encodeCursorModel(model))))
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
 * `agent.v1.ToolCall`, a union of 69 tools.
 *
 * Resolving `#14` took the class whose own `typeName` says `ToolCall` and whose
 * schema lists `19 task_tool_call`, NOT the nearest class of that minified name
 * -- two other classes share it, and one of them is an unrelated `ToolCall`.
 */
const FIELD_TOOLCALL_TASK = 19
const FIELD_TOOLCALL_UPDATE_TODOS = 9
const FIELD_TOOLCALL_GENERATE_IMAGE = 28
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

export type CursorTodoStatus = 'pending' | 'in_progress' | 'completed' | 'cancelled'

/** One native Todo row. Cursor requires an id in each row it reports. */
export interface CursorTodoItem {
  id: string
  content: string
  status: CursorTodoStatus
}

/** One UpdateTodos call in Cursor's Run stream. */
export interface CursorTodoCall {
  callID: string
  todos: readonly CursorTodoItem[]
  merge: boolean
}

/** One native GenerateImage call and the image returned by Cursor's server. */
export interface CursorGenerateImageCall {
  callID: string
  description: string
  filePath: string
  imageData: string
}

export interface CursorQuestionOption {
  id: string
  label: string
}

export interface CursorQuestion {
  id: string
  prompt: string
  options: readonly CursorQuestionOption[]
  allowMultiple: boolean
}

export type CursorInteractionCall
  = | { kind: 'question', callID: string, title: string, questions: readonly CursorQuestion[] }
    | { kind: 'plan', callID: string, name: string, overview: string, plan: string }
    | { kind: 'webFetch', callID: string, url: string }

export type CursorInteractionReply
  = | { kind: 'question', id: number, answers: Array<{ questionID: string, selectedOptionIDs: string[], freeformText?: string }>, rejectedReason?: string }
    | { kind: 'plan', id: number, accepted: boolean, planURI?: string, error?: string }
    | { kind: 'webFetch', id: number, approved: boolean, reason?: string }

export interface CursorMcpCall {
  callID: string
  server: string
  tool: string
  input: Record<string, unknown>
}

export interface CursorMcpReply {
  id: number
  success: boolean
  text: string
}

/** Operations that the Cursor service delegates to the actual local client. */
export type CursorExecutionCall
  = | { kind: 'shell', callID: string, command: string, workingDirectory?: string }
    | { kind: 'read', callID: string, path: string }
    | { kind: 'write', callID: string, path: string, content: string }
    | { kind: 'edit', callID: string, path: string, before: string, after: string }

/** The real client reply, including the native result bytes used by the tool update. */
export interface CursorExecutionReply {
  id: number
  execID?: string
  kind: 'shell' | 'read' | 'write'
  success: boolean
  text: string
  content?: string
  data?: Uint8Array
  path?: string
  exitCode?: number
  lines?: number
  size?: number
  rawResult: Uint8Array
  failureKind?: 'error' | 'file-not-found' | 'permission-denied' | 'rejected' | 'timeout' | 'spawn-error' | 'invalid-edit'
}

function encodeCursorTask(call: CursorTaskCall, report: string | undefined): Uint8Array {
  const args = concatBytes([
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
      encodeLengthDelimited(FIELD_RESULT_SUCCESS, concatBytes([
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
  return concatBytes([
    encodeLengthDelimited(FIELD_TOOLCALL_TASK, concatBytes(task)),
    encodeStringField(FIELD_TOOLCALL_TOOL_CALL_ID, call.callID),
  ])
}

function encodeToolCallUpdate(field: number, callID: string, toolCall: Uint8Array): Uint8Array {
  return encodeLengthDelimited(FIELD_INTERACTION_UPDATE, encodeLengthDelimited(field, concatBytes([
    encodeStringField(FIELD_UPDATE_CALL_ID, callID),
    encodeLengthDelimited(FIELD_UPDATE_TOOL_CALL, toolCall),
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
  return encodeToolCallUpdate(FIELD_TOOL_CALL_STARTED, call.callID, encodeCursorTask(call, undefined))
}

/**
 * The update that CLOSES a Task tool call, carrying the child's report.
 *
 * It repeats the arguments, because both updates carry a whole `ToolCall`. The
 * agent answers with a `tool_call_update` that moves the row to `completed`.
 */
export function cursorTaskCompleted(call: CursorTaskCall, report: string): Uint8Array {
  return encodeToolCallUpdate(FIELD_TOOL_CALL_COMPLETED, call.callID, encodeCursorTask(call, report))
}

/** Encode an actual nested child text delta while its parent Task remains open. */
export function cursorTaskProgress(callID: string, text: string): Uint8Array {
  if (!callID)
    throw new Error('The native Cursor Task delta requires its parent call ID.')
  const child = encodeLengthDelimited(FIELD_TEXT_DELTA, encodeStringField(FIELD_TEXT, text))
  const taskDelta = encodeLengthDelimited(1, child)
  const delta = encodeLengthDelimited(2, taskDelta)
  return encodeLengthDelimited(FIELD_INTERACTION_UPDATE, encodeLengthDelimited(15, concatBytes([
    encodeStringField(1, callID),
    encodeLengthDelimited(2, delta),
  ])))
}

/** Cursor's TodoStatus enum uses 1 pending, 2 in progress, 3 completed, 4 cancelled. */
const CURSOR_TODO_STATUS: Record<CursorTodoStatus, number> = {
  pending: 1,
  in_progress: 2,
  completed: 3,
  cancelled: 4,
}

function encodeCursorTodoItem(item: CursorTodoItem): Uint8Array {
  return concatBytes([
    encodeStringField(1, item.id),
    encodeStringField(2, item.content),
    encodeVarintField(3, CURSOR_TODO_STATUS[item.status]),
  ])
}

function encodeCursorTodo(call: CursorTodoCall, completed: boolean): Uint8Array {
  const todos = call.todos.map(item => encodeLengthDelimited(1, encodeCursorTodoItem(item)))
  const args = encodeLengthDelimited(1, concatBytes([...todos, encodeVarintField(2, Number(call.merge))]))
  const result = completed
    ? encodeLengthDelimited(2, encodeLengthDelimited(1, concatBytes([
        ...todos,
        encodeVarintField(2, call.todos.length),
        encodeVarintField(3, Number(call.merge)),
      ])))
    : new Uint8Array(0)
  return concatBytes([
    encodeLengthDelimited(FIELD_TOOLCALL_UPDATE_TODOS, concatBytes([args, result])),
    encodeStringField(FIELD_TOOLCALL_TOOL_CALL_ID, call.callID),
  ])
}

/** The native UpdateTodos call starts with its list. */
export function cursorTodoStarted(call: CursorTodoCall): Uint8Array {
  return encodeToolCallUpdate(FIELD_TOOL_CALL_STARTED, call.callID, encodeCursorTodo(call, false))
}

/** The completed UpdateTodos call repeats its list and the final count. */
export function cursorTodoCompleted(call: CursorTodoCall): Uint8Array {
  return encodeToolCallUpdate(FIELD_TOOL_CALL_COMPLETED, call.callID, encodeCursorTodo(call, true))
}

function encodeCursorGenerateImage(call: CursorGenerateImageCall, completed: boolean): Uint8Array {
  const args = concatBytes([
    encodeStringField(1, call.description),
    encodeStringField(2, call.filePath),
  ])
  const tool = [encodeLengthDelimited(1, args)]
  if (completed) {
    const success = concatBytes([
      encodeStringField(1, call.filePath),
      encodeStringField(2, call.imageData),
    ])
    tool.push(encodeLengthDelimited(2, encodeLengthDelimited(1, success)))
  }
  return concatBytes([
    encodeLengthDelimited(FIELD_TOOLCALL_GENERATE_IMAGE, concatBytes(tool)),
    encodeStringField(FIELD_TOOLCALL_TOOL_CALL_ID, call.callID),
  ])
}

/** Open the native GenerateImage row before the result arrives. */
export function cursorGenerateImageStarted(call: CursorGenerateImageCall): Uint8Array {
  return encodeToolCallUpdate(FIELD_TOOL_CALL_STARTED, call.callID, encodeCursorGenerateImage(call, false))
}

/** Close the native GenerateImage row with the generated file and image data. */
export function cursorGenerateImageCompleted(call: CursorGenerateImageCall): Uint8Array {
  return encodeToolCallUpdate(FIELD_TOOL_CALL_COMPLETED, call.callID, encodeCursorGenerateImage(call, true))
}

function cursorQuestionArgs(call: Extract<CursorInteractionCall, { kind: 'question' }>): Uint8Array {
  return concatBytes([
    encodeStringField(1, call.title),
    ...call.questions.map(question => encodeLengthDelimited(2, concatBytes([
      encodeStringField(1, question.id),
      encodeStringField(2, question.prompt),
      ...question.options.map(option => encodeLengthDelimited(3, concatBytes([
        encodeStringField(1, option.id),
        encodeStringField(2, option.label),
      ]))),
      encodeVarintField(4, Number(question.allowMultiple)),
    ]))),
  ])
}

/** Send one native Cursor interaction query on the Run stream. */
export function cursorInteractionQuery(id: number, call: CursorInteractionCall): Uint8Array {
  let field: number
  let query: Uint8Array
  switch (call.kind) {
    case 'question':
      field = 3
      query = concatBytes([
        encodeLengthDelimited(1, cursorQuestionArgs(call)),
        encodeStringField(2, call.callID),
      ])
      break
    case 'plan':
      field = 7
      query = concatBytes([
        encodeLengthDelimited(1, concatBytes([
          encodeStringField(1, call.plan),
          encodeStringField(3, call.overview),
          encodeStringField(4, call.name),
        ])),
        encodeStringField(2, call.callID),
      ])
      break
    case 'webFetch':
      field = 9
      query = encodeLengthDelimited(1, concatBytes([
        encodeStringField(1, call.url),
        encodeStringField(2, call.callID),
      ]))
      break
  }
  return encodeLengthDelimited(7, concatBytes([
    encodeVarintField(1, id),
    encodeLengthDelimited(field, query),
  ]))
}

/** The text at field 1 of the failure message at `field` of an interaction result, if it states one. */
function failureText(result: Uint8Array, field: number): string | undefined {
  const failure = descend(result, [field])
  return failure === undefined ? undefined : cursorProtobufString(lenientFields(failure), 1)
}

/** Read the native response that answers an interaction query. */
export function cursorInteractionResponseOf(clientMessage: Uint8Array): CursorInteractionReply | undefined {
  const response = descend(clientMessage, [6])
  if (!response)
    return undefined
  const fields = lenientFields(response)
  const id = cursorProtobufNumber(fields, 1)
  if (id === undefined)
    throw new Error('Cursor interaction response has no valid query id')
  if (fields.strings.has(3)) {
    const result = descend(response, [3, 1])
    if (!result)
      throw new Error('Cursor question response has no result')
    const success = descend(result, [1])
    if (success) {
      const answers = cursorProtobufRepeated(lenientFields(success), 1).map((answer) => {
        const parts = lenientFields(answer)
        const freeformText = cursorProtobufString(parts, 3)
        return {
          questionID: cursorProtobufString(parts, 1) ?? '',
          selectedOptionIDs: cursorProtobufRepeated(parts, 2).map(bytes => new TextDecoder().decode(bytes)),
          ...(freeformText === undefined ? {} : { freeformText }),
        }
      })
      return { kind: 'question', id, answers }
    }
    return { kind: 'question', id, answers: [], rejectedReason: failureText(result, 3) ?? 'rejected' }
  }
  if (fields.strings.has(7)) {
    const result = descend(response, [7, 1])
    if (!result)
      throw new Error('Cursor plan response has no result')
    if (descend(result, [1])) {
      const planURI = cursorProtobufString(lenientFields(result), 3)
      return { kind: 'plan', id, accepted: true, ...(planURI === undefined ? {} : { planURI }) }
    }
    const error = failureText(result, 2)
    return { kind: 'plan', id, accepted: false, ...(error === undefined ? {} : { error }) }
  }
  if (fields.strings.has(9)) {
    const result = descend(response, [9])!
    if (descend(result, [1]))
      return { kind: 'webFetch', id, approved: true }
    const reason = failureText(result, 2)
    return { kind: 'webFetch', id, approved: false, ...(reason === undefined ? {} : { reason }) }
  }
  throw new Error('Cursor sent an unsupported interaction response')
}

function cursorJsonValue(value: unknown, depth = 0, seen = new Set<object>()): JsonValue {
  if (depth > 32)
    throw new Error('Cursor MCP input exceeds the JSON nesting limit')
  if (value === null || typeof value === 'string' || typeof value === 'boolean')
    return value
  if (typeof value === 'number') {
    if (!Number.isFinite(value))
      throw new Error('Cursor MCP input contains a non-finite number')
    return value
  }
  if (typeof value !== 'object')
    throw new Error('Cursor MCP input contains a non-JSON value')
  if (seen.has(value))
    throw new Error('Cursor MCP input contains a cycle')
  seen.add(value)
  try {
    if (Array.isArray(value))
      return value.map(item => cursorJsonValue(item, depth + 1, seen))
    const prototype = Object.getPrototypeOf(value)
    if (prototype !== Object.prototype && prototype !== null)
      throw new Error('Cursor MCP input contains a non-JSON object')
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, cursorJsonValue(item, depth + 1, seen)]))
  }
  finally {
    seen.delete(value)
  }
}

/** Ask the installed Cursor client to run a local MCP tool. */
export function cursorMcpExec(id: number, call: CursorMcpCall): Uint8Array {
  const nativeToolName = `${call.server}-${call.tool}`
  const input = Object.entries(call.input).map(([key, value]) => encodeLengthDelimited(2, concatBytes([
    encodeStringField(1, key),
    encodeLengthDelimited(2, toBinary(ValueSchema, fromJson(ValueSchema, cursorJsonValue(value)))),
  ])))
  const args = concatBytes([
    encodeStringField(1, nativeToolName),
    ...input,
    encodeStringField(3, call.callID),
    encodeStringField(4, call.server),
    encodeStringField(5, nativeToolName),
    encodeVarintField(8, 1),
    encodeStringField(9, call.server),
  ])
  return encodeLengthDelimited(2, concatBytes([
    encodeVarintField(1, id),
    encodeStringField(15, call.callID),
    encodeLengthDelimited(11, args),
  ]))
}

/** Read the native result of one local MCP execution. */
export function cursorMcpResponseOf(clientMessage: Uint8Array): CursorMcpReply | undefined {
  const exec = descend(clientMessage, [2])
  if (!exec)
    return undefined
  const result = descend(exec, [11])
  if (!result)
    return undefined
  const id = cursorProtobufNumber(lenientFields(exec), 1)
  if (id === undefined)
    throw new Error('Cursor MCP result has no valid execution id')
  const success = descend(result, [1])
  if (success) {
    const successFields = lenientFields(success)
    const parts = cursorProtobufRepeated(successFields, 1)
      .map(item => descend(item, [1, 1]))
      .filter((part): part is Uint8Array => part !== undefined)
    const text = parts.map(part => new TextDecoder().decode(part)).join('\n')
    return { id, success: cursorProtobufNumber(successFields, 2) !== 1, text }
  }
  for (const field of [2, 3, 4, 5, 6]) {
    const failure = descend(result, [field])
    if (failure) {
      const parts = lenientFields(failure)
      const name = cursorProtobufString(parts, 1) ?? ''
      const available = cursorProtobufRepeated(parts, 2).map(bytes => new TextDecoder().decode(bytes))
      const details = available.length > 0 ? `; available: ${available.join(', ')}` : ''
      switch (field) {
        case 5: return { id, success: false, text: `tool ${name} not found${details}` }
        case 6: return { id, success: false, text: `server ${name} not found${details}` }
        default: return { id, success: false, text: name || `MCP result ${field}` }
      }
    }
  }
  if (descend(result, [7]))
    return { id, success: true, text: 'MCP tool approved' }
  throw new Error('Cursor MCP result has no outcome')
}

/**
 * `agent.v1.ExecServerMessage.request_context_args` and
 * `agent.v1.ExecClientMessage.request_context_result`. Both sit at field 10 of
 * their message, beside `mcp_args` and `mcp_result` at field 11.
 */
const FIELD_REQUEST_CONTEXT = 10
/** `agent.v1.RequestContextResult.success`, `.error` and `.rejected`. */
const FIELD_REQUEST_CONTEXT_SUCCESS = 1
const FIELD_REQUEST_CONTEXT_ERROR = 2
const FIELD_REQUEST_CONTEXT_REJECTED = 3
/** `agent.v1.RequestContextSuccess.request_context`. */
const FIELD_REQUEST_CONTEXT_VALUE = 1
/** `agent.v1.RequestContext.rules`, each one an `agent.v1.CursorRule`. */
const FIELD_CONTEXT_RULES = 2
/** `agent.v1.CursorRule.full_path` and `.content`. */
const FIELD_RULE_PATH = 1
const FIELD_RULE_CONTENT = 2

/** One rule that the CLI loaded for the project and states in its request context. */
export interface CursorContextRule {
  path: string
  content: string
}

export interface CursorRequestContextReply {
  id: number
  rules: CursorContextRule[]
}

/**
 * Encode the `ExecServerMessage` that asks the CLI for its request context.
 *
 * The backend sends this query before it builds the model's prompt. The CLI
 * answers with the rules, skills, and environment it loaded from the project.
 * The Run request carries none of them: its selected context holds only the
 * rules that a user attached to the message.
 */
export function cursorRequestContextExec(id: number, callID: string): Uint8Array {
  if (!Number.isInteger(id) || id <= 0 || id > 0xFFFF_FFFF)
    throw new Error('The native Cursor request context ID must be a positive uint32.')
  return encodeLengthDelimited(2, concatBytes([
    encodeVarintField(1, id),
    encodeStringField(15, callID),
    encodeLengthDelimited(FIELD_REQUEST_CONTEXT, new Uint8Array(0)),
  ]))
}

/**
 * Read the rules from the CLI's answer to a request context query.
 *
 * An error or a rejection throws: the proof that reads these rules must never
 * take a refused query for a project with no rules.
 */
export function cursorRequestContextResponseOf(clientMessage: Uint8Array): CursorRequestContextReply | undefined {
  const exec = descend(clientMessage, [2])
  if (!exec)
    return undefined
  const result = descend(exec, [FIELD_REQUEST_CONTEXT])
  if (!result)
    return undefined
  const id = cursorProtobufNumber(lenientFields(exec), 1)
  if (id === undefined)
    throw new Error('Cursor request context result has no valid execution id')
  const failure = descend(result, [FIELD_REQUEST_CONTEXT_ERROR]) ?? descend(result, [FIELD_REQUEST_CONTEXT_REJECTED])
  if (failure)
    throw new Error(`The native Cursor request context was refused: ${cursorProtobufString(lenientFields(failure), 1) ?? 'no reason'}`)
  const success = descend(result, [FIELD_REQUEST_CONTEXT_SUCCESS])
  if (!success)
    throw new Error('Cursor request context result has no outcome')
  const context = descend(success, [FIELD_REQUEST_CONTEXT_VALUE])
  const rules = context === undefined
    ? []
    : cursorProtobufRepeated(lenientFields(context), FIELD_CONTEXT_RULES).map((rule) => {
        const fields = lenientFields(rule)
        return { path: cursorProtobufString(fields, FIELD_RULE_PATH) ?? '', content: cursorProtobufString(fields, FIELD_RULE_CONTENT) ?? '' }
      })
  return { id, rules }
}

/** Encode a source-backed ExecServerMessage on the native Run stream. */
export function cursorExecutionRequest(id: number, call: CursorExecutionCall): Uint8Array {
  if (!Number.isInteger(id) || id <= 0 || id > 0xFFFF_FFFF)
    throw new Error('The native Cursor execution ID must be a positive uint32.')
  let field: number
  let args: Uint8Array
  if (call.kind === 'shell') {
    field = 2
    args = concatBytes([
      encodeStringField(1, call.command),
      ...(call.workingDirectory !== undefined ? [encodeStringField(2, call.workingDirectory)] : []),
      encodeVarintField(3, 120_000),
      encodeStringField(4, call.callID),
      // The service reports parse failure instead of inventing an allowlist result.
      encodeLengthDelimited(8, encodeVarintField(1, 1)),
      encodeStringField(15, 'Run the scripted command'),
    ])
  }
  else if (call.kind === 'read') {
    field = 7
    args = concatBytes([encodeStringField(1, call.path), encodeStringField(2, call.callID)])
  }
  else if (call.kind === 'write') {
    field = 3
    args = concatBytes([
      encodeStringField(1, call.path),
      encodeStringField(2, call.content),
      encodeStringField(3, call.callID),
      encodeVarintField(4, 1),
    ])
  }
  else {
    throw new Error('A native Cursor targeted edit must read the file before writing.')
  }
  return encodeLengthDelimited(2, concatBytes([
    encodeVarintField(1, id),
    encodeStringField(15, call.callID),
    encodeLengthDelimited(field, args),
  ]))
}

/** Decode the actual local client's shell, read, or write result. */
export function cursorExecutionResponseOf(clientMessage: Uint8Array): CursorExecutionReply | undefined {
  const outer = readCursorProtobufFields(clientMessage)
  const rawExec = cursorProtobufBytes(outer, 2)
  if (!rawExec)
    return undefined
  const exec = readCursorProtobufFields(rawExec)
  const id = cursorProtobufNumber(exec, 1)
  if (id === undefined || id <= 0 || id > 0xFFFF_FFFF)
    throw new Error('The native Cursor execution reply has no valid ID.')
  const entry = ([2, 7, 3] as const).find(field => exec.strings.has(field))
  if (entry === undefined)
    return undefined
  const rawResult = cursorProtobufBytes(exec, entry)!
  const result = readCursorProtobufFields(rawResult)
  const kind = entry === 2 ? 'shell' : entry === 7 ? 'read' : 'write'
  const execID = cursorProtobufString(exec, 15)
  const base = { id, ...(execID !== undefined ? { execID } : {}), kind, rawResult } satisfies Pick<CursorExecutionReply, 'id' | 'execID' | 'kind' | 'rawResult'>
  const outcome = [...result.strings.keys()].find(field => field >= 1 && field <= 7)
  if (outcome === undefined)
    throw new Error('The native Cursor execution reply has no outcome.')
  const fields = readCursorProtobufFields(cursorProtobufBytes(result, outcome)!)
  if (kind === 'shell' && (outcome === 1 || outcome === 2)) {
    const exitCode = cursorProtobufNumber(fields, 3, true) ?? 0
    const stdout = cursorProtobufString(fields, 5) ?? ''
    const stderr = cursorProtobufString(fields, 6) ?? ''
    return { ...base, success: outcome === 1 && exitCode === 0, exitCode, text: `Exit code: ${exitCode}\n${stdout}${stderr ? `\n${stderr}` : ''}` }
  }
  if (kind === 'read' && outcome === 1) {
    const content = cursorProtobufString(fields, 2)
    const data = cursorProtobufBytes(fields, 5)
    const path = cursorProtobufString(fields, 1)
    const lines = cursorProtobufNumber(fields, 3)
    const size = cursorProtobufNumber(fields, 4)
    return {
      ...base,
      success: true,
      text: content ?? `Read ${data?.length ?? 0} binary bytes.`,
      ...(content !== undefined ? { content } : {}),
      ...(data !== undefined ? { data } : {}),
      ...(path !== undefined ? { path } : {}),
      ...(lines !== undefined ? { lines } : {}),
      ...(size !== undefined ? { size } : {}),
    }
  }
  if (kind === 'write' && outcome === 1) {
    const content = cursorProtobufString(fields, 4)
    const path = cursorProtobufString(fields, 1)
    return { ...base, success: true, text: `Wrote ${path ?? 'the native file'}${content !== undefined ? `\n${content}` : ''}`, ...(content !== undefined ? { content } : {}), ...(path !== undefined ? { path } : {}) }
  }
  const failureKind: CursorExecutionReply['failureKind'] = kind === 'read' && outcome === 4
    ? 'file-not-found'
    : kind === 'shell' && outcome === 3
      ? 'timeout'
      : kind === 'shell' && outcome === 5
        ? 'spawn-error'
        : (kind === 'read' && outcome === 5) || (kind === 'write' && outcome === 3) || (kind === 'shell' && outcome === 7)
            ? 'permission-denied'
            : (kind === 'read' && outcome === 3) || (kind === 'write' && outcome === 6) || (kind === 'shell' && outcome === 4) ? 'rejected' : 'error'
  const detail = cursorProtobufString(fields, kind === 'shell' ? 3 : 2)
    ?? [...fields.strings.values()].flat().map(value => new TextDecoder().decode(value)).join('\n')
  return { ...base, success: false, failureKind, text: detail || `Native ${kind} ${failureKind}.` }
}

function executionTool(call: CursorExecutionCall, result?: Uint8Array): Uint8Array {
  let field: number
  let args: Uint8Array
  if (call.kind === 'shell') {
    field = 1
    args = concatBytes([encodeStringField(1, call.command), ...(call.workingDirectory !== undefined ? [encodeStringField(2, call.workingDirectory)] : []), encodeStringField(4, call.callID)])
  }
  else if (call.kind === 'read') {
    field = 8
    args = encodeStringField(1, call.path)
  }
  else {
    field = 12
    args = concatBytes([encodeStringField(1, call.path), ...(call.kind === 'write' ? [encodeStringField(6, call.content)] : [])])
  }
  return concatBytes([
    encodeLengthDelimited(field, concatBytes([encodeLengthDelimited(1, args), ...(result ? [encodeLengthDelimited(2, result)] : [])])),
    encodeStringField(57, call.callID),
  ])
}

/** Start a native tool row before the client executes its operation. */
export function cursorExecutionToolStarted(call: CursorExecutionCall): Uint8Array {
  return encodeToolCallUpdate(FIELD_TOOL_CALL_STARTED, call.callID, executionTool(call))
}

/** Bind a completed tool update to the actual matching client reply. */
export function cursorExecutionToolCompleted(call: CursorExecutionCall, reply: CursorExecutionReply, contents: { before: string, after: string }): Uint8Array {
  let result: Uint8Array
  if (call.kind === 'shell') {
    result = reply.rawResult
  }
  else if (call.kind === 'read') {
    result = reply.success
      ? encodeLengthDelimited(1, concatBytes([
          ...(reply.content !== undefined ? [encodeStringField(1, reply.content)] : []),
          ...(reply.data !== undefined ? [encodeLengthDelimited(6, reply.data)] : []),
          encodeStringField(7, reply.path ?? call.path),
          ...(reply.lines !== undefined ? [encodeVarintField(4, reply.lines)] : []),
          ...(reply.size !== undefined ? [encodeVarintField(5, reply.size)] : []),
        ]))
      : encodeLengthDelimited(2, encodeStringField(1, reply.text))
  }
  else {
    result = reply.success
      ? encodeLengthDelimited(1, concatBytes([encodeStringField(1, reply.path ?? call.path), encodeStringField(6, contents.before), encodeStringField(7, contents.after), encodeStringField(8, reply.text)]))
      : encodeLengthDelimited(7, concatBytes([encodeStringField(1, call.path), encodeStringField(2, reply.text), encodeStringField(5, reply.text)]))
  }
  return encodeToolCallUpdate(FIELD_TOOL_CALL_COMPLETED, call.callID, executionTool(call, result))
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

/**
 * An `AgentServerMessage` that stores one blob in the session's database.
 *
 * `blobID` reaches the table as lowercase hex, which is why the real ids are 64
 * characters: the client hex-encodes these 32 bytes.
 */
export function cursorSetBlob(messageID: number, blobID: Uint8Array, data: Uint8Array): Uint8Array {
  return encodeLengthDelimited(FIELD_KV_SERVER_MESSAGE, concatBytes([
    encodeVarintField(FIELD_KV_ID, messageID),
    encodeLengthDelimited(FIELD_KV_SET_BLOB_ARGS, concatBytes([
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
  return concatBytes([header, payload])
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
export interface CursorConnectFrame {
  flags: number
  payload: Uint8Array
}

export function takeConnectFrames(buffer: Uint8Array): { frames: CursorConnectFrame[], rest: Uint8Array } {
  const frames: CursorConnectFrame[] = []
  const view = new DataView(buffer.buffer, buffer.byteOffset, buffer.byteLength)
  let at = 0
  while (buffer.byteLength - at >= 5) {
    const length = view.getUint32(at + 1, false)
    if (buffer.byteLength - at - 5 < length)
      break
    frames.push({ flags: buffer[at]!, payload: buffer.subarray(at + 5, at + 5 + length) })
    at += 5 + length
  }
  return { frames, rest: buffer.subarray(at) }
}
