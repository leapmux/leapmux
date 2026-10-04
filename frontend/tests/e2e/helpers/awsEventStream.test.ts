import { Buffer } from 'node:buffer'
import { crc32 } from 'node:zlib'
import { describe, expect, it } from 'vitest'
import {
  decodeEventStreamMessages,
  encodeEventStreamEvent,
  encodeEventStreamException,
  encodeEventStreamMessage,
} from './awsEventStream'

describe('encodeEventStreamEvent', () => {
  // The frame that the research probe captured from its own encoder, which both of
  // Kiro's engines parsed: `assistantResponseEvent {"content": "Hi"}`, 125 bytes.
  // The payload spells the JSON with a space after the colon, as Python writes it.
  it('writes the bytes that Kiro\'s engines parse', () => {
    const frame = encodeEventStreamMessage({
      ':event-type': 'assistantResponseEvent',
      ':content-type': 'application/json',
      ':message-type': 'event',
    }, Buffer.from('{"content": "Hi"}', 'utf8'))
    expect(frame.toString('hex')).toBe(
      '0000007d0000005c06bde6c80b3a6576656e742d747970650700166173736973'
      + '74616e74526573706f6e73654576656e740d3a636f6e74656e742d7479706507'
      + '00106170706c69636174696f6e2f6a736f6e0d3a6d6573736167652d74797065'
      + '0700056576656e747b22636f6e74656e74223a20224869227dba09105a',
    )
  })

  it('round-trips an event through the decoder', () => {
    const frame = encodeEventStreamEvent('toolUseEvent', { toolUseId: 't1', name: 'read_file', input: '{"path":"/w/a.txt"}' })
    const [message, ...rest] = decodeEventStreamMessages(frame)
    expect(rest).toEqual([])
    expect(message?.headers).toEqual({ ':event-type': 'toolUseEvent', ':content-type': 'application/json', ':message-type': 'event' })
    expect(JSON.parse(message!.payload.toString('utf8'))).toEqual({ toolUseId: 't1', name: 'read_file', input: '{"path":"/w/a.txt"}' })
  })

  it('writes a payload of multibyte text by its byte length', () => {
    const frame = encodeEventStreamEvent('assistantResponseEvent', { content: 'héllo 😀' })
    expect(frame.readUInt32BE(0)).toBe(frame.byteLength)
    expect(JSON.parse(decodeEventStreamMessages(frame)[0]!.payload.toString('utf8'))).toEqual({ content: 'héllo 😀' })
  })
})

describe('encodeEventStreamException', () => {
  it('marks the message as an exception of its type', () => {
    const [message] = decodeEventStreamMessages(encodeEventStreamException('ThrottlingException', { message: 'slow down' }))
    expect(message?.headers).toEqual({ ':exception-type': 'ThrottlingException', ':content-type': 'application/json', ':message-type': 'exception' })
  })
})

describe('encodeEventStreamMessage', () => {
  it('writes an empty payload and no header', () => {
    const frame = encodeEventStreamMessage({}, Buffer.alloc(0))
    expect(frame.byteLength).toBe(16)
    expect(decodeEventStreamMessages(frame)).toEqual([{ headers: {}, payload: Buffer.alloc(0) }])
  })

  it('refuses a header that the format cannot hold', () => {
    expect(() => encodeEventStreamMessage({ '': 'x' }, Buffer.alloc(0))).toThrow('header name')
    expect(() => encodeEventStreamMessage({ ['n'.repeat(256)]: 'x' }, Buffer.alloc(0))).toThrow('header name')
    expect(() => encodeEventStreamMessage({ name: 'v'.repeat(0x10000) }, Buffer.alloc(0))).toThrow('more than')
  })

  it('writes a header at each length limit that the format can hold', () => {
    const name = 'n'.repeat(255)
    const value = 'v'.repeat(0xFFFF)
    expect(decodeEventStreamMessages(encodeEventStreamMessage({ [name]: value }, Buffer.alloc(0))))
      .toEqual([{ headers: { [name]: value }, payload: Buffer.alloc(0) }])
  })

  // The length field counts bytes. A limit on characters would pass a value of
  // multibyte text whose byte length overflows the two-byte field.
  it('limits a header value by its UTF-8 bytes, not by its characters', () => {
    const value = 'é'.repeat(0x8000)
    expect(value.length).toBeLessThan(0xFFFF)
    expect(() => encodeEventStreamMessage({ name: value }, Buffer.alloc(0))).toThrow('more than')
  })

  it('refuses a header name that is not ASCII', () => {
    // The format states a name in ASCII. A latin1 byte would reach the client as a
    // different name, so the encoder refuses it rather than write it.
    expect(() => encodeEventStreamMessage({ é: 'x' }, Buffer.alloc(0))).toThrow('ASCII')
    expect(() => encodeEventStreamMessage({ ':event-type😀': 'x' }, Buffer.alloc(0))).toThrow('ASCII')
    expect(() => encodeEventStreamMessage({ 'a\nb': 'x' }, Buffer.alloc(0))).toThrow('ASCII')
  })
})

/**
 * One message around a header block that the test writes byte by byte, with both
 * CRCs correct, so the decoder sees a header block that the encoder never writes.
 */
function frameAround(headerBlock: Buffer, payload = Buffer.alloc(0)): Buffer {
  const prelude = Buffer.alloc(12)
  prelude.writeUInt32BE(12 + headerBlock.byteLength + payload.byteLength + 4, 0)
  prelude.writeUInt32BE(headerBlock.byteLength, 4)
  prelude.writeUInt32BE(crc32(prelude.subarray(0, 8)), 8)
  const body = Buffer.concat([prelude, headerBlock, payload])
  const messageCrc = Buffer.alloc(4)
  messageCrc.writeUInt32BE(crc32(body), 0)
  return Buffer.concat([body, messageCrc])
}

describe('decodeEventStreamMessages', () => {
  const frame = encodeEventStreamEvent('metadataEvent', { stopReason: 'END_TURN' })

  it('decodes several messages in order', () => {
    const second = encodeEventStreamEvent('assistantResponseEvent', { content: 'x' })
    expect(decodeEventStreamMessages(Buffer.concat([frame, second])).map(message => message.headers[':event-type']))
      .toEqual(['metadataEvent', 'assistantResponseEvent'])
  })

  it('decodes an empty buffer to no message', () => {
    expect(decodeEventStreamMessages(Buffer.alloc(0))).toEqual([])
  })

  it('refuses a buffer that ends inside a message', () => {
    expect(() => decodeEventStreamMessages(frame.subarray(0, frame.byteLength - 1))).toThrow('cannot hold')
    expect(() => decodeEventStreamMessages(frame.subarray(0, 5))).toThrow('inside a prelude')
  })

  it('refuses a header whose value runs past the header block', () => {
    // The name `a`, the string type, a value length of 16, and two bytes of value.
    const block = Buffer.from([1, 0x61, 7, 0x00, 0x10, 0x78, 0x79])
    expect(() => decodeEventStreamMessages(frameAround(block, Buffer.from('payload')))).toThrow('runs past')
  })

  it('refuses a header whose name or type runs past the header block', () => {
    expect(() => decodeEventStreamMessages(frameAround(Buffer.from([5, 0x61])))).toThrow('runs past')
    expect(() => decodeEventStreamMessages(frameAround(Buffer.from([1, 0x61])))).toThrow('runs past')
    expect(() => decodeEventStreamMessages(frameAround(Buffer.from([1, 0x61, 7, 0x00])))).toThrow('runs past')
  })

  it('refuses a header of a type other than string', () => {
    // The name `a`, the type 6 (a byte array), a length of 1, and one byte.
    const block = Buffer.from([1, 0x61, 6, 0x00, 0x01, 0x78])
    expect(() => decodeEventStreamMessages(frameAround(block))).toThrow('has the value type 6')
  })

  // A length below the fixed bytes would move the offset by less than one message,
  // or not at all, so the loop would read the same bytes again and again.
  it('refuses a message that states a length shorter than its fixed bytes', () => {
    const prelude = Buffer.alloc(12)
    prelude.writeUInt32BE(0, 0)
    prelude.writeUInt32BE(0, 4)
    prelude.writeUInt32BE(crc32(prelude.subarray(0, 8)), 8)
    expect(() => decodeEventStreamMessages(Buffer.concat([prelude, Buffer.alloc(4)]))).toThrow('states a length of 0')
  })

  it('refuses a message whose header block is longer than the message', () => {
    const prelude = Buffer.alloc(12)
    prelude.writeUInt32BE(16, 0)
    prelude.writeUInt32BE(8, 4)
    prelude.writeUInt32BE(crc32(prelude.subarray(0, 8)), 8)
    expect(() => decodeEventStreamMessages(Buffer.concat([prelude, Buffer.alloc(12)]))).toThrow('states a length of 16')
  })

  it('reads a header block that the test writes by hand', () => {
    const block = Buffer.from([1, 0x61, 7, 0x00, 0x02, 0x78, 0x79])
    expect(decodeEventStreamMessages(frameAround(block))).toEqual([{ headers: { a: 'xy' }, payload: Buffer.alloc(0) }])
  })

  it('refuses a message whose CRCs do not match', () => {
    const badPrelude = Buffer.from(frame)
    badPrelude[3] = badPrelude[3]! ^ 0xFF
    expect(() => decodeEventStreamMessages(badPrelude)).toThrow('prelude CRC')
    const badBody = Buffer.from(frame)
    badBody[badBody.byteLength - 6] = badBody[badBody.byteLength - 6]! ^ 0xFF
    expect(() => decodeEventStreamMessages(badBody)).toThrow('message CRC')
  })
})
