import { Buffer } from 'node:buffer'
import { describe, expect, it } from 'vitest'
import {
  connectEndOfStream,
  connectFrame,
  cursorAvailableModels,
  cursorDefaultModel,
  cursorPromptOf,
  cursorSetBlob,
  cursorTextDelta,
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

describe('cursorTextDelta', () => {
  it('nests the text three fields deep, which reads back through the same path', () => {
    const message = cursorTextDelta('CURSOR_MOCK_OK')
    expect(new TextDecoder().decode(descend(message, [1, 1, 1])!)).toBe('CURSOR_MOCK_OK')
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
