import { Buffer } from 'node:buffer'
import { fromBinary, toJson } from '@bufbuild/protobuf'
import { ValueSchema } from '@bufbuild/protobuf/wkt'
import { describe, expect, it } from 'vitest'
import {
  connectEndOfStream,
  connectFrame,
  cursorAttachmentPayloads,
  cursorAvailableModels,
  cursorConversationIdOf,
  cursorDefaultModel,
  cursorGenerateImageCompleted,
  cursorGenerateImageStarted,
  cursorInteractionQuery,
  cursorInteractionResponseOf,
  cursorMcpExec,
  cursorMcpResponseOf,
  cursorPromptOf,
  cursorSetBlob,
  cursorTextDelta,
  cursorThinkingDelta,
  cursorTodoCompleted,
  cursorTodoStarted,
  cursorTurnEnded,
  cursorUsableModels,
  descend,
  encodeLengthDelimited,
  encodeStringField,
  encodeVarint,
  readLengthDelimitedFields,
  takeConnectFrames,
} from './cursorWire'

/** Build the nesting a real `AgentClientMessage` uses to carry a prompt. */
function clientMessageWithPrompt(text: string): Uint8Array {
  return encodeLengthDelimited(1, // AgentClientMessage.run_request
    encodeLengthDelimited(2, // AgentRunRequest.action
      encodeLengthDelimited(1, // ConversationAction.user_message_action
        encodeLengthDelimited(1, // UserMessageAction.user_message
          encodeStringField(1, text), // UserMessage.text
        ))))
}

describe('encodeVarint', () => {
  it('writes one byte below 128 and two above it', () => {
    expect([...encodeVarint(0)]).toEqual([0])
    expect([...encodeVarint(127)]).toEqual([0x7F])
    expect([...encodeVarint(128)]).toEqual([0x80, 0x01])
    expect([...encodeVarint(300)]).toEqual([0xAC, 0x02])
  })

  it('refuses a negative or fractional value rather than writing a wrong length', () => {
    expect(() => encodeVarint(-1)).toThrow('non-negative integer')
    expect(() => encodeVarint(1.5)).toThrow('non-negative integer')
  })
})

describe('encodeLengthDelimited', () => {
  it('writes the tag, the length, and the payload', () => {
    // Field 1, wire type 2 -> tag 0x0A; then the length; then the bytes.
    expect([...encodeStringField(1, 'hi')]).toEqual([0x0A, 0x02, 0x68, 0x69])
  })

  it('writes a multi-byte length for a payload past 127 bytes', () => {
    const encoded = encodeLengthDelimited(1, new Uint8Array(200))
    expect([...encoded.subarray(0, 3)]).toEqual([0x0A, 0xC8, 0x01])
    expect(encoded.byteLength).toBe(203)
  })
})

describe('readLengthDelimitedFields', () => {
  it('reads a field back after writing it', () => {
    const fields = readLengthDelimitedFields(encodeStringField(7, 'value'))
    expect(new TextDecoder().decode(fields.get(7)![0]!)).toBe('value')
  })

  it('keeps every occurrence of a repeated field, in order', () => {
    const bytes = Buffer.concat([encodeStringField(3, 'a'), encodeStringField(3, 'b')])
    const fields = readLengthDelimitedFields(new Uint8Array(bytes))
    expect(fields.get(3)!.map(v => new TextDecoder().decode(v))).toEqual(['a', 'b'])
  })

  // The whole point of the reader: it walks a message whose other fields it does
  // not declare, so a varint or a fixed-width field beside the one it wants must
  // not shift every field after it.
  it('skips a varint, a 64-bit and a 32-bit field without losing its place', () => {
    const varintField = Uint8Array.from([(2 << 3) | 0, 0xAC, 0x02])
    const fixed64Field = Uint8Array.from([(4 << 3) | 1, 1, 2, 3, 4, 5, 6, 7, 8])
    const fixed32Field = Uint8Array.from([(5 << 3) | 5, 1, 2, 3, 4])
    const bytes = Buffer.concat([varintField, fixed64Field, encodeStringField(9, 'kept'), fixed32Field])
    const fields = readLengthDelimitedFields(new Uint8Array(bytes))
    expect(new TextDecoder().decode(fields.get(9)![0]!)).toBe('kept')
  })

  it('returns no field for an empty message', () => {
    expect(readLengthDelimitedFields(new Uint8Array(0)).size).toBe(0)
  })
})

describe('descend', () => {
  it('follows a chain of nested fields', () => {
    const nested = encodeLengthDelimited(1, encodeLengthDelimited(2, encodeStringField(3, 'deep')))
    expect(new TextDecoder().decode(descend(nested, [1, 2, 3])!)).toBe('deep')
  })

  it('answers undefined for a path that leaves the message', () => {
    expect(descend(encodeStringField(1, 'x'), [1, 2])).toBeUndefined()
    expect(descend(encodeStringField(1, 'x'), [4])).toBeUndefined()
  })
})

describe('cursorPromptOf', () => {
  it('reads the prompt out of the five-field path', () => {
    expect(cursorPromptOf(clientMessageWithPrompt('Say the mock word.'))).toBe('Say the mock word.')
  })

  it('preserves a prompt that carries newlines, which every scenario marker does', () => {
    const prompt = 'Do the thing.\n\nLEAPMUXE2ESCENARIO:test-1'
    expect(cursorPromptOf(clientMessageWithPrompt(prompt))).toBe(prompt)
  })

  // A heartbeat, a tool result and an interaction response all travel on the
  // same stream. Answering undefined is what lets a reader tell the frame that
  // OPENS a turn from the rest.
  it('answers undefined for a client message that opens no turn', () => {
    expect(cursorPromptOf(encodeLengthDelimited(7, new Uint8Array(0)))).toBeUndefined()
  })
})

describe('cursorConversationIdOf', () => {
  it('reads the Run request conversation ID beside the action', () => {
    const prompt = readLengthDelimitedFields(clientMessageWithPrompt('Continue.')).get(1)![0]!
    const run = Buffer.concat([prompt, encodeStringField(5, 'cursor-conversation-1')])
    expect(cursorConversationIdOf(encodeLengthDelimited(1, run))).toBe('cursor-conversation-1')
  })

  it('keeps an absent or empty conversation ID distinct', () => {
    expect(cursorConversationIdOf(clientMessageWithPrompt('New.'))).toBeUndefined()
    expect(cursorConversationIdOf(encodeLengthDelimited(1, encodeStringField(5, '')))).toBe('')
  })

  it('ignores an ID on a message that does not open a Run request', () => {
    expect(cursorConversationIdOf(encodeLengthDelimited(7, encodeStringField(5, 'heartbeat')))).toBeUndefined()
  })
})

describe('cursorAttachmentPayloads', () => {
  const runWithContext = (context: Uint8Array) => {
    const userMessage = Buffer.concat([encodeStringField(1, 'Inspect.'), encodeLengthDelimited(3, context)])
    const userAction = encodeLengthDelimited(1, userMessage)
    const action = encodeLengthDelimited(1, userAction)
    const run = encodeLengthDelimited(2, action)
    return encodeLengthDelimited(1, run)
  }

  it('reads inline image, document, and file content from the selected context', () => {
    const image = Buffer.concat([encodeLengthDelimited(8, Uint8Array.from([0x89, 0x50])), encodeStringField(7, 'image/png')])
    const document = Buffer.concat([encodeLengthDelimited(8, Uint8Array.from([0x25, 0x50])), encodeStringField(3, 'report.pdf'), encodeStringField(4, 'application/pdf')])
    const file = Buffer.concat([encodeStringField(1, 'text fixture'), encodeStringField(2, 'notes.txt')])
    const context = Buffer.concat([encodeLengthDelimited(1, image), encodeLengthDelimited(25, document), encodeLengthDelimited(4, file)])

    expect(cursorAttachmentPayloads(runWithContext(context))).toEqual([
      { kind: 'image', data: 'iVA=', mimeType: 'image/png' },
      { kind: 'document', data: 'JVA=', filename: 'report.pdf', mimeType: 'application/pdf' },
      { kind: 'file', data: 'text fixture', filename: 'notes.txt' },
    ])
  })

  it('reads inline bytes from a blob-id-with-data attachment', () => {
    const blob = encodeLengthDelimited(9, encodeLengthDelimited(2, Uint8Array.from([0, 0xFF])))
    expect(cursorAttachmentPayloads(runWithContext(encodeLengthDelimited(1, blob))))
      .toEqual([{ kind: 'image', data: 'AP8=' }])
  })

  it('returns no inline payload for a blob reference or a message with no context', () => {
    expect(cursorAttachmentPayloads(clientMessageWithPrompt('No attachment.'))).toEqual([])
    const blobIDOnly = encodeLengthDelimited(1, encodeLengthDelimited(1, Uint8Array.from([1, 2])))
    expect(cursorAttachmentPayloads(runWithContext(blobIDOnly))).toEqual([])
  })
})

describe('cursorTextDelta', () => {
  it('nests the text three fields deep, which reads back through the same path', () => {
    const message = cursorTextDelta('CURSOR_MOCK_OK')
    expect(new TextDecoder().decode(descend(message, [1, 1, 1])!)).toBe('CURSOR_MOCK_OK')
  })
})

describe('cursorThinkingDelta', () => {
  it('puts thinking text in the installed interaction update field', () => {
    expect(new TextDecoder().decode(descend(cursorThinkingDelta('I checked.'), [1, 4, 1])!)).toBe('I checked.')
  })
})

describe('cursorTurnEnded', () => {
  // Every token count on `TurnEndedUpdate` is optional, so the update that ends
  // a turn sets nothing at all and encodes as a tag and a zero length.
  it('encodes the empty update as four bytes', () => {
    // 0x0A: interaction_update, field 1, length-delimited, two bytes long.
    // 0x72: turn_ended, field 14, length-delimited -- (14 << 3) | 2 -- and empty.
    expect([...cursorTurnEnded()]).toEqual([0x0A, 0x02, 0x72, 0x00])
  })

  it('encodes scripted input and output token counts', () => {
    // The installed TurnEndedUpdate descriptor sets input_tokens=1 and output_tokens=2.
    expect([...cursorTurnEnded({ inputTokens: 12_000, outputTokens: 40 })])
      .toEqual([0x0A, 0x07, 0x72, 0x05, 0x08, 0xE0, 0x5D, 0x10, 0x28])
    expect([...cursorTurnEnded({ inputTokens: 0 })]).toEqual([0x0A, 0x04, 0x72, 0x02, 0x08, 0x00])
  })
})

describe('connectFrame', () => {
  it('writes a flag byte and a big-endian length before the payload', () => {
    const frame = connectFrame(new TextEncoder().encode('abc'))
    expect([...frame]).toEqual([0, 0, 0, 0, 3, 0x61, 0x62, 0x63])
  })

  it('marks the end of stream and carries the trailers as JSON', () => {
    const frame = connectEndOfStream()
    expect(frame[0]).toBe(0x02)
    expect(new TextDecoder().decode(frame.subarray(5))).toBe('{}')
  })
})

describe('takeConnectFrames', () => {
  it('splits several frames out of one buffer', () => {
    const buffer = Buffer.concat([connectFrame(encodeStringField(1, 'a')), connectFrame(encodeStringField(1, 'b'))])
    const { frames, rest } = takeConnectFrames(new Uint8Array(buffer))
    expect(frames).toHaveLength(2)
    expect(rest.byteLength).toBe(0)
  })

  // One frame can arrive in several TCP segments. A length prefix read out of a
  // half-received header would take an arbitrary number as the frame's size.
  it('keeps a partial header and a partial payload for the next chunk', () => {
    const whole = connectFrame(encodeStringField(1, 'hello'))
    for (const cut of [1, 3, 5, whole.byteLength - 1]) {
      const { frames, rest } = takeConnectFrames(whole.subarray(0, cut))
      expect(frames, `a frame cut at ${cut} bytes is not complete`).toHaveLength(0)
      expect(rest.byteLength).toBe(cut)
    }
  })

  it('reassembles a frame split across two chunks', () => {
    const whole = connectFrame(encodeStringField(1, 'hello'))
    const first = takeConnectFrames(whole.subarray(0, 6))
    const joined = Buffer.concat([first.rest, whole.subarray(6)])
    const { frames } = takeConnectFrames(new Uint8Array(joined))
    expect(frames).toHaveLength(1)
    expect(new TextDecoder().decode(descend(frames[0]!, [1])!)).toBe('hello')
  })
})

describe('cursorAvailableModels', () => {
  const FIELD_MODELS = 2
  const FIELD_MODEL_NAME = 1
  const FIELD_MODEL_VARIANTS = 30
  const FIELD_MODEL_ID_ALIASES = 37
  const FIELD_VARIANT_DISPLAY_NAME = 2
  const FIELD_VARIANT_STRING = 9

  const text = (bytes: Uint8Array | undefined): string | undefined =>
    bytes === undefined ? undefined : new TextDecoder().decode(bytes)

  it('writes one repeated models entry per model', () => {
    const encoded = cursorAvailableModels([
      { name: 'default', displayName: 'Auto', variants: [{ id: 'default[]', displayName: 'Auto' }] },
      { name: 'mock-grok', displayName: 'Mock Grok', variants: [{ id: 'mock-grok[]', displayName: 'Mock Grok' }] },
    ])
    const models = readLengthDelimitedFields(encoded).get(FIELD_MODELS)
    expect(models).toHaveLength(2)
    expect(text(readLengthDelimitedFields(models![0]!).get(FIELD_MODEL_NAME)?.[0])).toBe('default')
    expect(text(readLengthDelimitedFields(models![1]!).get(FIELD_MODEL_NAME)?.[0])).toBe('mock-grok')
  })

  // A variant reaches the picker only when it carries BOTH a display name and a
  // variant string, so both must survive the round trip on every variant.
  it('keeps every variant display name and bracketed id', () => {
    const encoded = cursorAvailableModels([{
      name: 'mock-grok',
      displayName: 'Mock Grok',
      variants: [
        { id: 'mock-grok[context=256k,reasoning_effort=low]', displayName: 'Mock Grok Low' },
        { id: 'mock-grok[context=256k,reasoning_effort=xhigh]', displayName: 'Mock Grok Extra High' },
      ],
    }])
    const model = readLengthDelimitedFields(encoded).get(FIELD_MODELS)![0]!
    const variants = readLengthDelimitedFields(model).get(FIELD_MODEL_VARIANTS)
    expect(variants).toHaveLength(2)
    const read = variants!.map((variant) => {
      const fields = readLengthDelimitedFields(variant)
      return {
        id: text(fields.get(FIELD_VARIANT_STRING)?.[0]),
        displayName: text(fields.get(FIELD_VARIANT_DISPLAY_NAME)?.[0]),
      }
    })
    expect(read).toEqual([
      { id: 'mock-grok[context=256k,reasoning_effort=low]', displayName: 'Mock Grok Low' },
      { id: 'mock-grok[context=256k,reasoning_effort=xhigh]', displayName: 'Mock Grok Extra High' },
    ])
  })

  it('writes an alias for each one given, and none when there are none', () => {
    const withAlias = cursorAvailableModels([
      { name: 'default', displayName: 'Auto', aliases: ['auto'], variants: [{ id: 'default[]', displayName: 'Auto' }] },
    ])
    const aliased = readLengthDelimitedFields(readLengthDelimitedFields(withAlias).get(FIELD_MODELS)![0]!)
    expect(aliased.get(FIELD_MODEL_ID_ALIASES)?.map(a => text(a))).toEqual(['auto'])

    const without = cursorAvailableModels([
      { name: 'mock-grok', displayName: 'Mock Grok', variants: [{ id: 'mock-grok[]', displayName: 'Mock Grok' }] },
    ])
    const bare = readLengthDelimitedFields(readLengthDelimitedFields(without).get(FIELD_MODELS)![0]!)
    expect(bare.get(FIELD_MODEL_ID_ALIASES)).toBeUndefined()
  })

  // An empty catalogue is what an all-defaults answer produces, and it is the
  // state the mock exists to avoid. It must still encode to a valid, empty body
  // rather than to something the client refuses.
  it('encodes an empty catalogue as a zero-byte body', () => {
    expect(cursorAvailableModels([]).byteLength).toBe(0)
  })

  // A model with no variants is legal on the wire and contributes no picker
  // entry, so the encoder must not invent one.
  it('writes a model that has no variants', () => {
    const encoded = cursorAvailableModels([{ name: 'bare', displayName: 'Bare', variants: [] }])
    const model = readLengthDelimitedFields(encoded).get(FIELD_MODELS)![0]!
    expect(readLengthDelimitedFields(model).get(FIELD_MODEL_VARIANTS)).toBeUndefined()
    expect(text(readLengthDelimitedFields(model).get(FIELD_MODEL_NAME)?.[0])).toBe('bare')
  })
})

describe('cursorDefaultModel', () => {
  const FIELD_MODEL_DETAILS = 1
  const FIELD_DETAILS_MODEL_ID = 1
  const FIELD_DETAILS_DISPLAY_MODEL_ID = 3
  const FIELD_DETAILS_DISPLAY_NAME = 4
  const FIELD_DETAILS_DISPLAY_NAME_SHORT = 5
  const FIELD_DETAILS_ALIASES = 6

  const auto = {
    name: 'default',
    displayName: 'Auto',
    aliases: ['auto'],
    variants: [{ id: 'default[]', displayName: 'Auto', isDefault: true }],
  }

  const details = (encoded: Uint8Array): Map<number, Uint8Array[]> =>
    readLengthDelimitedFields(readLengthDelimitedFields(encoded).get(FIELD_MODEL_DETAILS)![0]!)

  const text = (bytes: Uint8Array | undefined): string | undefined =>
    bytes === undefined ? undefined : new TextDecoder().decode(bytes)

  it('names the model and both of its display forms', () => {
    const fields = details(cursorDefaultModel(auto))
    expect(text(fields.get(FIELD_DETAILS_MODEL_ID)?.[0])).toBe('default')
    expect(text(fields.get(FIELD_DETAILS_DISPLAY_NAME)?.[0])).toBe('Auto')
    expect(text(fields.get(FIELD_DETAILS_DISPLAY_NAME_SHORT)?.[0])).toBe('Auto')
    expect(fields.get(FIELD_DETAILS_ALIASES)?.map(a => text(a))).toEqual(['auto'])
  })

  // The real response carries the ALIAS as the display model id ("auto" for the
  // model named "default"), so a model with no alias falls back to its own name
  // rather than leaving the field empty.
  it('uses the first alias as the display model id, else the name', () => {
    expect(text(details(cursorDefaultModel(auto)).get(FIELD_DETAILS_DISPLAY_MODEL_ID)?.[0])).toBe('auto')

    const unaliased = { name: 'mock-grok', displayName: 'Mock Grok', variants: [] }
    const fields = details(cursorDefaultModel(unaliased))
    expect(text(fields.get(FIELD_DETAILS_DISPLAY_MODEL_ID)?.[0])).toBe('mock-grok')
    expect(fields.get(FIELD_DETAILS_ALIASES)).toBeUndefined()
  })
})

describe('cursorUsableModels', () => {
  const FIELD_MODEL_DETAILS = 1
  const FIELD_DETAILS_MODEL_ID = 1

  it('repeats one models entry per model, in order', () => {
    const encoded = cursorUsableModels([
      { name: 'default', displayName: 'Auto', aliases: ['auto'], variants: [] },
      { name: 'mock-grok', displayName: 'Mock Grok', variants: [] },
    ])
    const entries = readLengthDelimitedFields(encoded).get(FIELD_MODEL_DETAILS)
    expect(entries).toHaveLength(2)
    const names = entries!.map(entry =>
      new TextDecoder().decode(readLengthDelimitedFields(entry).get(FIELD_DETAILS_MODEL_ID)![0]!))
    expect(names).toEqual(['default', 'mock-grok'])
  })

  it('encodes an empty list as a zero-byte body', () => {
    expect(cursorUsableModels([]).byteLength).toBe(0)
  })
})

describe('cursorSetBlob', () => {
  const FIELD_KV_SERVER_MESSAGE = 4
  const FIELD_KV_SET_BLOB_ARGS = 3
  const FIELD_SET_BLOB_ID = 1
  const FIELD_SET_BLOB_DATA = 2

  const setBlobArgs = (encoded: Uint8Array): Map<number, Uint8Array[]> => {
    const kv = readLengthDelimitedFields(encoded).get(FIELD_KV_SERVER_MESSAGE)![0]!
    return readLengthDelimitedFields(readLengthDelimitedFields(kv).get(FIELD_KV_SET_BLOB_ARGS)![0]!)
  }

  it('carries the blob id and its data as bytes', () => {
    const id = Uint8Array.from([0xDE, 0xAD, 0xBE, 0xEF])
    const data = new TextEncoder().encode('{"role":"tool"}')
    const args = setBlobArgs(cursorSetBlob(7, id, data))
    expect([...args.get(FIELD_SET_BLOB_ID)![0]!]).toEqual([0xDE, 0xAD, 0xBE, 0xEF])
    expect(new TextDecoder().decode(args.get(FIELD_SET_BLOB_DATA)![0]!)).toBe('{"role":"tool"}')
  })

  // The message id is a VARINT beside the args, so a value past 127 must not
  // shift the fields that follow it.
  it('keeps the args readable for a multi-byte message id', () => {
    const args = setBlobArgs(cursorSetBlob(300, Uint8Array.from([1]), Uint8Array.from([2])))
    expect([...args.get(FIELD_SET_BLOB_ID)![0]!]).toEqual([1])
    expect([...args.get(FIELD_SET_BLOB_DATA)![0]!]).toEqual([2])
  })

  it('writes an empty blob without losing either field', () => {
    const args = setBlobArgs(cursorSetBlob(1, new Uint8Array(0), new Uint8Array(0)))
    expect(args.get(FIELD_SET_BLOB_ID)![0]!.byteLength).toBe(0)
    expect(args.get(FIELD_SET_BLOB_DATA)![0]!.byteLength).toBe(0)
  })
})

describe('cursorTodoStarted and cursorTodoCompleted', () => {
  const call = {
    callID: 'todo-1',
    todos: [
      { id: '1', content: 'Inspect', status: 'completed' as const },
      { id: '2', content: 'Report', status: 'in_progress' as const },
    ],
    merge: false,
  }

  it('writes native Todo ids, content, and enum statuses on the started update', () => {
    // InteractionUpdate.tool_call_started -> ToolCall.update_todos_tool_call
    // -> UpdateTodosToolCall.args -> UpdateTodosArgs.todos.
    const first = descend(cursorTodoStarted(call), [1, 2, 2, 9, 1, 1])!
    const second = readLengthDelimitedFields(descend(cursorTodoStarted(call), [1, 2, 2, 9, 1])!).get(1)![1]!
    expect(new TextDecoder().decode(readLengthDelimitedFields(first).get(1)![0]!)).toBe('1')
    expect(new TextDecoder().decode(readLengthDelimitedFields(first).get(2)![0]!)).toBe('Inspect')
    expect([...first.slice(-2)]).toEqual([0x18, 3])
    expect([...second.slice(-2)]).toEqual([0x18, 2])
  })

  it('writes the completed Todo list and native success count', () => {
    // InteractionUpdate.tool_call_completed -> ToolCall.update_todos_tool_call
    // -> UpdateTodosToolCall.result -> UpdateTodosResult.success.
    const success = descend(cursorTodoCompleted(call), [1, 3, 2, 9, 2, 1])!
    const items = readLengthDelimitedFields(success).get(1)
    expect(items).toHaveLength(2)
    expect(new TextDecoder().decode(readLengthDelimitedFields(items![1]!).get(2)![0]!)).toBe('Report')
    expect([...success.slice(-4)]).toEqual([0x10, 2, 0x18, 0])
  })

  it('keeps an empty replacement list as a valid snapshot', () => {
    const empty = { callID: 'clear', todos: [], merge: false }
    expect(descend(cursorTodoStarted(empty), [1, 2, 2, 9, 1, 1])).toBeUndefined()
    expect(descend(cursorTodoCompleted(empty), [1, 3, 2, 9, 2, 1])).toBeDefined()
  })
})

describe('cursorGenerateImageStarted and cursorGenerateImageCompleted', () => {
  const call = { callID: 'image-1', description: 'A teal square', filePath: '/work/teal.png', imageData: 'cG5n' }

  it('encodes the native tool id and image request', () => {
    const started = cursorGenerateImageStarted(call)
    const tool = descend(started, [1, 2, 2])!
    const fields = readLengthDelimitedFields(tool)
    expect(new TextDecoder().decode(fields.get(57)?.[0])).toBe(call.callID)
    const args = descend(tool, [28, 1])!
    expect(new TextDecoder().decode(readLengthDelimitedFields(args).get(1)?.[0])).toBe(call.description)
    expect(new TextDecoder().decode(readLengthDelimitedFields(args).get(2)?.[0])).toBe(call.filePath)
    expect(descend(tool, [28, 2])).toBeUndefined()
  })

  it('encodes the native success with a file path and image data', () => {
    const completed = cursorGenerateImageCompleted(call)
    const success = descend(completed, [1, 3, 2, 28, 2, 1])!
    const fields = readLengthDelimitedFields(success)
    expect(new TextDecoder().decode(fields.get(1)?.[0])).toBe(call.filePath)
    expect(new TextDecoder().decode(fields.get(2)?.[0])).toBe(call.imageData)
  })
})

describe('cursorInteractionQuery and cursorInteractionResponseOf', () => {
  function reply(id: number, field: number, payload: Uint8Array): Uint8Array {
    return encodeLengthDelimited(6, Buffer.concat([
      Uint8Array.from([0x08, ...encodeVarint(id)]),
      encodeLengthDelimited(field, payload),
    ]))
  }

  it('encodes a native question query with stable ids and choices', () => {
    const query = cursorInteractionQuery(300, {
      kind: 'question',
      callID: 'cursor-q',
      title: 'Color',
      questions: [{ id: 'question-1', prompt: 'Which color?', allowMultiple: false, options: [{ id: 'option-1-1', label: 'Blue' }] }],
    })
    const request = descend(query, [7, 3])!
    expect(new TextDecoder().decode(readLengthDelimitedFields(request).get(2)?.[0])).toBe('cursor-q')
    const args = descend(request, [1])!
    expect(new TextDecoder().decode(readLengthDelimitedFields(args).get(1)?.[0])).toBe('Color')
    const question = descend(args, [2])!
    expect(new TextDecoder().decode(readLengthDelimitedFields(question).get(2)?.[0])).toBe('Which color?')
    const option = descend(question, [3])!
    expect(new TextDecoder().decode(readLengthDelimitedFields(option).get(1)?.[0])).toBe('option-1-1')
    expect([...query]).toContain(0xAC)
  })

  it('encodes plan and web-fetch queries in their own union fields', () => {
    const plan = cursorInteractionQuery(301, { kind: 'plan', callID: 'plan-1', name: 'Review', overview: 'No edits', plan: '# Plan' })
    expect(new TextDecoder().decode(descend(plan, [7, 7, 1, 1]))).toBe('# Plan')
    expect(new TextDecoder().decode(descend(plan, [7, 7, 1, 4]))).toBe('Review')
    const fetch = cursorInteractionQuery(302, { kind: 'webFetch', callID: 'fetch-1', url: 'https://example.invalid/probe' })
    expect(new TextDecoder().decode(descend(fetch, [7, 9, 1, 1]))).toBe('https://example.invalid/probe')
    expect(new TextDecoder().decode(descend(fetch, [7, 9, 1, 2]))).toBe('fetch-1')
  })

  it('decodes the selected native question option and multi-byte query id', () => {
    const answer = Buffer.concat([encodeStringField(1, 'question-1'), encodeStringField(2, 'option-1-1')])
    const question = encodeLengthDelimited(1, encodeLengthDelimited(1, encodeLengthDelimited(1, answer)))
    expect(cursorInteractionResponseOf(reply(300, 3, question))).toEqual({
      kind: 'question',
      id: 300,
      answers: [{ questionID: 'question-1', selectedOptionIDs: ['option-1-1'] }],
    })
  })

  it('decodes plan approval and a web-fetch refusal', () => {
    const plan = encodeLengthDelimited(1, encodeLengthDelimited(1, new Uint8Array(0)))
    expect(cursorInteractionResponseOf(reply(301, 7, plan))).toEqual({ kind: 'plan', id: 301, accepted: true })
    const refused = encodeLengthDelimited(2, encodeStringField(1, 'Denied'))
    expect(cursorInteractionResponseOf(reply(302, 9, refused))).toEqual({ kind: 'webFetch', id: 302, approved: false, reason: 'Denied' })
  })

  it('ignores other client messages and refuses a response without an id', () => {
    expect(cursorInteractionResponseOf(encodeLengthDelimited(1, new Uint8Array(0)))).toBeUndefined()
    expect(() => cursorInteractionResponseOf(encodeLengthDelimited(6, encodeLengthDelimited(9, new Uint8Array(0)))))
      .toThrow('no valid query id')
  })
})

describe('cursorMcpExec and cursorMcpResponseOf', () => {
  function reply(id: number | undefined, result: Uint8Array): Uint8Array {
    return encodeLengthDelimited(2, Buffer.concat([
      ...(id === undefined ? [] : [Uint8Array.from([0x08, ...encodeVarint(id)])]),
      encodeLengthDelimited(11, result),
    ]))
  }

  it('encodes the native local tool and every JSON input value', () => {
    const encoded = cursorMcpExec(301, {
      callID: 'mcp-1',
      server: 'form_probe',
      tool: 'echo',
      input: { count: 0, enabled: false, nested: { values: [null, -2, 'blue'] } },
    })
    const exec = descend(encoded, [2])!
    const args = descend(exec, [11])!
    const fields = readLengthDelimitedFields(args)
    expect(new TextDecoder().decode(fields.get(1)?.[0])).toBe('form_probe-echo')
    expect(new TextDecoder().decode(fields.get(3)?.[0])).toBe('mcp-1')
    expect(new TextDecoder().decode(fields.get(5)?.[0])).toBe('form_probe-echo')
    expect(new TextDecoder().decode(fields.get(9)?.[0])).toBe('form_probe')
    const input = Object.fromEntries((fields.get(2) ?? []).map((entry) => {
      const key = new TextDecoder().decode(descend(entry, [1]))
      const value = fromBinary(ValueSchema, descend(entry, [2])!)
      return [key, toJson(ValueSchema, value)]
    }))
    expect(input).toEqual({ count: 0, enabled: false, nested: { values: [null, -2, 'blue'] } })
    expect([...args]).toContain(0x40)
  })

  it('keeps an empty input and refuses non-JSON values', () => {
    const call = { callID: 'mcp-1', server: 'form_probe', tool: 'ask', input: {} }
    expect(readLengthDelimitedFields(descend(cursorMcpExec(301, call), [2, 11])!).get(2)).toBeUndefined()
    expect(() => cursorMcpExec(301, { ...call, input: { count: Number.NaN } })).toThrow('non-finite number')
    expect(() => cursorMcpExec(301, { ...call, input: { missing: undefined } })).toThrow('non-JSON value')
    const cycle: Record<string, unknown> = {}
    cycle.self = cycle
    expect(() => cursorMcpExec(301, { ...call, input: cycle })).toThrow('cycle')
  })

  it('reads native success text and an error with the correlated id', () => {
    const text = encodeLengthDelimited(1, encodeStringField(1, 'FORM_ROUND_TRIP_OK'))
    const success = encodeLengthDelimited(1, text)
    expect(cursorMcpResponseOf(reply(301, encodeLengthDelimited(1, success)))).toEqual({
      id: 301,
      success: true,
      text: 'FORM_ROUND_TRIP_OK',
    })
    expect(cursorMcpResponseOf(reply(302, encodeLengthDelimited(2, encodeStringField(1, 'server unavailable'))))).toEqual({
      id: 302,
      success: false,
      text: 'server unavailable',
    })
    const missingTool = Buffer.concat([encodeStringField(1, 'form_probe_ask'), encodeStringField(2, 'ask')])
    expect(cursorMcpResponseOf(reply(303, encodeLengthDelimited(5, missingTool)))).toEqual({
      id: 303,
      success: false,
      text: 'tool form_probe_ask not found; available: ask',
    })
  })

  it('ignores unrelated frames and refuses a result without an execution id', () => {
    expect(cursorMcpResponseOf(encodeLengthDelimited(1, new Uint8Array(0)))).toBeUndefined()
    expect(() => cursorMcpResponseOf(reply(undefined, encodeLengthDelimited(1, new Uint8Array(0)))))
      .toThrow('no valid execution id')
  })
})
