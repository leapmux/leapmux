import { Buffer } from 'node:buffer'
import { gzipSync } from 'node:zlib'
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
  cursorExecutionRequest,
  cursorExecutionResponseOf,
  cursorExecutionToolCompleted,
  cursorExecutionToolStarted,
  cursorGenerateImageCompleted,
  cursorGenerateImageStarted,
  cursorInteractionQuery,
  cursorInteractionResponseOf,
  cursorMcpExec,
  cursorMcpResponseOf,
  cursorPromptOf,
  cursorSetBlob,
  cursorTaskProgress,
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
  it('rejects a number that cannot preserve its integer value', () => {
    expect(() => encodeVarint(Number.MAX_SAFE_INTEGER + 1)).toThrow('safe integer')
  })
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
  it('encodes the largest valid field number without a signed bitwise overflow', () => {
    const field = 0x1FFF_FFFF
    const fields = readLengthDelimitedFields(encodeLengthDelimited(field, Uint8Array.from([42])))
    expect(fields.get(field)).toEqual([Uint8Array.from([42])])
  })

  it.each([0, -1, 1.5, 0x2000_0000, Number.POSITIVE_INFINITY])('rejects the invalid field number %s before encoding it', (field) => {
    expect(() => encodeLengthDelimited(field, new Uint8Array())).toThrow('field number')
  })
  it('writes the tag, the length, and the payload', () => {
    // Write tag 0x0A for field 1 with wire type 2.
    // Then write the length and payload bytes.
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

  // The reader must skip fields that it does not decode.
  // A neighboring varint or fixed-width field must not shift later fields.
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

  it('does not treat a truncated payload as a complete length-delimited field', () => {
    const truncated = Uint8Array.from([0x0A, 0x04, 0x61])
    expect(readLengthDelimitedFields(truncated).has(1)).toBe(false)
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

  // These messages share the stream:
  // - Heartbeats.
  // - Tool results.
  // - Interaction responses.
  // Return undefined to distinguish them from a frame that opens a turn.
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
  // Every TurnEndedUpdate token count is optional.
  // An update without counts encodes as a tag with a zero payload length.
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
  it('preserves the compression flag and the actual gzip payload', () => {
    const prompt = clientMessageWithPrompt('CHILD_NATIVE_EXECUTION: reply once.\n\nLEAPMUXE2ESCENARIO:native-child')
    const compressed = new Uint8Array(gzipSync(prompt))
    const { frames, rest } = takeConnectFrames(connectFrame(compressed, 0x01))
    expect(frames).toEqual([{ flags: 0x01, payload: compressed }])
    expect(rest.byteLength).toBe(0)
  })

  it('keeps ordinary message and end flags distinct in a mixed stream', () => {
    const message = clientMessageWithPrompt('The native parent prompt.')
    const trailers = new TextEncoder().encode('{"error":{"code":"internal"}}')
    const { frames } = takeConnectFrames(new Uint8Array(Buffer.concat([
      connectFrame(message),
      connectFrame(trailers, 0x02),
    ])))
    expect(frames).toEqual([{ flags: 0, payload: message }, { flags: 0x02, payload: trailers }])
  })

  it('retains a compressed frame flag after a partial header or payload', () => {
    const payload = new Uint8Array(gzipSync(clientMessageWithPrompt('The actual compressed child prompt.')))
    const whole = connectFrame(payload, 0x01)
    for (const cut of [1, 4, 5, whole.byteLength - 1]) {
      const first = takeConnectFrames(whole.subarray(0, cut))
      expect(first.frames).toHaveLength(0)
      const next = takeConnectFrames(new Uint8Array(Buffer.concat([first.rest, whole.subarray(cut)])))
      expect(next.frames).toEqual([{ flags: 0x01, payload }])
      expect(next.rest.byteLength).toBe(0)
    }
  })

  it('keeps an empty message payload distinct from the end of the stream', () => {
    const empty = new Uint8Array()
    const { frames } = takeConnectFrames(new Uint8Array(Buffer.concat([connectFrame(empty), connectFrame(empty, 0x02)])))
    expect(frames).toEqual([{ flags: 0, payload: empty }, { flags: 0x02, payload: empty }])
  })

  it('splits several frames out of one buffer', () => {
    const buffer = Buffer.concat([connectFrame(encodeStringField(1, 'a')), connectFrame(encodeStringField(1, 'b'))])
    const { frames, rest } = takeConnectFrames(new Uint8Array(buffer))
    expect(frames).toHaveLength(2)
    expect(rest.byteLength).toBe(0)
  })

  // One frame can arrive in several TCP segments.
  // An incomplete header must not supply an incorrect payload length.
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
    expect(new TextDecoder().decode(descend(frames[0]!.payload, [1])!)).toBe('hello')
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

  // The picker requires both the variant's display name and variant string.
  // Encoding and decoding must preserve both values for every variant.
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

  it('preserves each explicit native variant parameter instead of deriving it from the display ID', () => {
    const encoded = cursorAvailableModels([{
      name: 'mock-grok',
      displayName: 'Mock Grok',
      variants: [{
        id: 'display-only[]',
        displayName: 'Mock Grok Low',
        parameters: [{ id: 'context', value: '256k' }, { id: 'reasoning_effort', value: 'low' }],
      }],
    }])
    const model = readLengthDelimitedFields(encoded).get(FIELD_MODELS)![0]!
    const variant = readLengthDelimitedFields(model).get(FIELD_MODEL_VARIANTS)![0]!
    const parameters = readLengthDelimitedFields(variant).get(1) ?? []
    expect(parameters.map((parameter) => {
      const fields = readLengthDelimitedFields(parameter)
      return { id: text(fields.get(1)?.[0]), value: text(fields.get(2)?.[0]) }
    })).toEqual([{ id: 'context', value: '256k' }, { id: 'reasoning_effort', value: 'low' }])
  })

  it('preserves an empty native parameter value', () => {
    const encoded = cursorAvailableModels([{
      name: 'mock-model',
      displayName: 'Mock Model',
      variants: [{ id: 'mock-model[]', displayName: 'Mock Model', parameters: [{ id: 'native-option', value: '' }] }],
    }])
    const parameter = descend(encoded, [FIELD_MODELS, FIELD_MODEL_VARIANTS, 1])
    expect(parameter).toBeDefined()
    expect(text(readLengthDelimitedFields(parameter!).get(1)?.[0])).toBe('native-option')
    expect(text(readLengthDelimitedFields(parameter!).get(2)?.[0])).toBe('')
  })

  // An all-defaults answer produces an empty catalog.
  // The mock normally supplies models, but an empty catalog must still encode a valid empty body.
  it('encodes an empty catalogue as a zero-byte body', () => {
    expect(cursorAvailableModels([]).byteLength).toBe(0)
  })

  // The native protocol permits a model with no variants.
  // It contributes no picker entry, so the encoder must not invent one.
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

  // The native response uses the alias as the display model ID.
  // The model "default" uses alias "auto".
  // A model without an alias uses its own name and keeps the field nonempty.
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

  // A varint encodes the message ID beside its arguments.
  // A value above 127 must not shift later fields.
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

function integer(field: number, value: number): Uint8Array {
  return Buffer.concat([encodeVarint(field * 8), encodeVarint(value)])
}

function clientReply(id: number, field: number, outcome: number, bytes: Uint8Array): Uint8Array {
  return encodeLengthDelimited(2, Buffer.concat([integer(1, id), encodeStringField(15, 'tool'), encodeLengthDelimited(field, encodeLengthDelimited(outcome, bytes))]))
}

describe('cursorExecutionRequest', () => {
  it('uses the native shell envelope and argument field numbers', () => {
    const frame = cursorExecutionRequest(3, { kind: 'shell', callID: 'tool', command: 'printf native', workingDirectory: '/project' })
    const args = readLengthDelimitedFields(descend(frame, [2, 2])!)
    expect(new TextDecoder().decode(args.get(1)?.[0])).toBe('printf native')
    expect(new TextDecoder().decode(args.get(2)?.[0])).toBe('/project')
    expect(new TextDecoder().decode(args.get(4)?.[0])).toBe('tool')
  })

  it('supplies the required native shell parsing result without claiming an allowlist match', () => {
    const frame = cursorExecutionRequest(3, { kind: 'shell', callID: 'native-shell', command: 'printf actual; exit 7' })
    const parsing = descend(frame, [2, 2, 8])
    expect(parsing, 'the installed shell executor refuses an absent parsing result').toBeDefined()
    expect([...parsing!]).toEqual([0x08, 1])
  })

  it('keeps empty write text and the actual read path', () => {
    const write = readLengthDelimitedFields(descend(cursorExecutionRequest(4, { kind: 'write', callID: 'tool', path: '/project/empty', content: '' }), [2, 3])!)
    expect(write.has(2)).toBe(true)
    expect(write.get(2)?.[0]?.length).toBe(0)
    const read = readLengthDelimitedFields(descend(cursorExecutionRequest(5, { kind: 'read', callID: 'tool', path: '/project/empty' }), [2, 7])!)
    expect(new TextDecoder().decode(read.get(1)?.[0])).toBe('/project/empty')
  })

  it.each([0, -1, 1.5, 0x1_0000_0000])('refuses an invalid native request ID %s', (id) => {
    expect(() => cursorExecutionRequest(id, { kind: 'read', callID: 'tool', path: '/project/a' })).toThrow('positive uint32')
  })
})

describe('cursorExecutionResponseOf', () => {
  it('preserves actual stdout and stderr from a failed native shell', () => {
    const frame = clientReply(3, 2, 2, Buffer.concat([integer(3, 7), encodeStringField(5, 'actual out'), encodeStringField(6, 'actual err')]))
    const reply = cursorExecutionResponseOf(frame)
    expect(reply).toMatchObject({ id: 3, execID: 'tool', kind: 'shell', success: false, exitCode: 7 })
    expect(reply?.text).toContain('actual out')
    expect(reply?.text).toContain('actual err')
  })

  it('reads a native signed int32 exit code', () => {
    const negativeOne = Uint8Array.from([0x18, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x01])
    expect(cursorExecutionResponseOf(clientReply(3, 2, 2, negativeOne))).toMatchObject({ exitCode: -1, success: false })
  })

  it('keeps empty text and binary read output distinct from absent output', () => {
    expect(cursorExecutionResponseOf(clientReply(3, 7, 1, encodeStringField(2, '')))).toMatchObject({ content: '', text: '' })
    const binary = cursorExecutionResponseOf(clientReply(3, 7, 1, encodeLengthDelimited(5, Uint8Array.from([0, 255]))))
    expect(binary?.data).toEqual(Uint8Array.from([0, 255]))
    expect(binary?.content).toBeUndefined()
  })

  it('distinguishes a missing file and a denied write from successful empty results', () => {
    expect(cursorExecutionResponseOf(clientReply(3, 7, 4, encodeStringField(1, '/project/missing')))).toMatchObject({ success: false, failureKind: 'file-not-found' })
    expect(cursorExecutionResponseOf(clientReply(3, 3, 3, encodeStringField(2, 'denied')))).toMatchObject({ success: false, failureKind: 'permission-denied' })
  })

  it('ignores a nonexecution client message', () => {
    expect(cursorExecutionResponseOf(encodeLengthDelimited(7, new Uint8Array(0)))).toBeUndefined()
  })

  it('rejects truncated payloads, fixed fields, and varints', () => {
    expect(() => cursorExecutionResponseOf(Uint8Array.from([0x12, 0x04, 0x08]))).toThrow('truncated payload')
    expect(() => cursorExecutionResponseOf(Uint8Array.from([0x09, 0x01]))).toThrow('truncated fixed-width')
    expect(() => cursorExecutionResponseOf(Uint8Array.from([0x80]))).toThrow('truncated or invalid varint')
  })
})

describe('cursorExecutionToolCompleted', () => {
  it('uses the source-backed start and completion update fields', () => {
    const call = { kind: 'read' as const, callID: 'tool', path: '/project/a' }
    expect(descend(cursorExecutionToolStarted(call), [1, 2, 2, 8, 1])).toBeDefined()
    const reply = cursorExecutionResponseOf(clientReply(3, 7, 1, Buffer.concat([encodeStringField(1, '/project/a'), encodeStringField(2, 'actual data')])))
    if (!reply)
      throw new Error('The native read has no reply.')
    const frame = cursorExecutionToolCompleted(call, reply, { before: '', after: '' })
    const success = readLengthDelimitedFields(descend(frame, [1, 3, 2, 8, 2, 1])!)
    expect(new TextDecoder().decode(success.get(1)?.[0])).toBe('actual data')
    expect(new TextDecoder().decode(success.get(7)?.[0])).toBe('/project/a')
  })
})

describe('cursorTaskProgress', () => {
  it.each(['ACTUAL_CHILD_DELTA', '', 'Child UTF-8 零 🔒'])('uses the actual nested Task and child interaction fields for %s', (text) => {
    const frame = cursorTaskProgress('native-parent-task', text)
    expect(new TextDecoder().decode(descend(frame, [1, 15, 1]))).toBe('native-parent-task')
    expect(new TextDecoder().decode(descend(frame, [1, 15, 2, 2, 1, 1, 1]))).toBe(text)
    expect(descend(frame, [1, 3])).toBeUndefined()
  })

  it('refuses a nested child delta without its actual parent call ID', () => {
    expect(() => cursorTaskProgress('', 'Native child progress.')).toThrow('parent call ID')
  })
})
