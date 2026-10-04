export interface CursorProtobufFields {
  strings: Map<number, Uint8Array[]>
  integers: Map<number, bigint>
}

/** Read native protobuf fields and reject malformed bytes before decoding a result. */
export function readCursorProtobufFields(bytes: Uint8Array): CursorProtobufFields {
  let at = 0
  const strings = new Map<number, Uint8Array[]>()
  const integers = new Map<number, bigint>()
  const take = (): bigint => {
    let value = 0n
    for (let index = 0; index < 10; index++) {
      const byte = bytes[at++]
      if (byte === undefined || (index === 9 && byte > 1))
        throw new Error('The native Cursor protobuf message has a truncated or invalid varint.')
      value |= BigInt(byte & 0x7F) << BigInt(index * 7)
      if ((byte & 0x80) === 0)
        return value
    }
    throw new Error('The native Cursor protobuf message has an invalid varint.')
  }
  while (at < bytes.length) {
    const tag = take()
    const field = Number(tag >> 3n)
    if (!Number.isSafeInteger(field) || field <= 0 || field > 0x1FFF_FFFF)
      throw new Error('The native Cursor protobuf message has an invalid field number.')
    switch (Number(tag & 7n)) {
      case 0:
        integers.set(field, take())
        break
      case 1:
        at += 8
        break
      case 2: {
        const length = take()
        if (length > BigInt(bytes.length - at))
          throw new Error('The native Cursor protobuf message has a truncated payload.')
        const size = Number(length)
        const values = strings.get(field) ?? []
        values.push(bytes.subarray(at, at + size))
        strings.set(field, values)
        at += size
        break
      }
      case 5:
        at += 4
        break
      default:
        throw new Error('The native Cursor protobuf message has an unsupported wire type.')
    }
    if (at > bytes.length)
      throw new Error('The native Cursor protobuf message has a truncated fixed-width field.')
  }
  return { strings, integers }
}

export function cursorProtobufString(fields: CursorProtobufFields, field: number): string | undefined {
  const value = fields.strings.get(field)?.at(-1)
  return value !== undefined ? new TextDecoder().decode(value) : undefined
}

export function cursorProtobufNumber(fields: CursorProtobufFields, field: number, signed = false): number | undefined {
  const value = fields.integers.get(field)
  if (value === undefined)
    return undefined
  const normalized = signed ? BigInt.asIntN(32, value) : value
  return normalized <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(normalized) : undefined
}
