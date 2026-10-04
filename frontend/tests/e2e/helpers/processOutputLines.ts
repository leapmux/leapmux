import { Buffer } from 'node:buffer'
import { StringDecoder } from 'node:string_decoder'

export interface ProcessOutputLineDecoder {
  write: (chunk: Uint8Array | string) => void
  end: () => void
  partial: () => string
}

/** Decode one process stream without losing UTF-8 characters or partial lines. */
export function createProcessOutputLineDecoder(onLine: (line: string) => void): ProcessOutputLineDecoder {
  const decoder = new StringDecoder('utf8')
  let carry = ''
  let ended = false
  const consume = (text: string) => {
    const complete = carry + text
    let start = 0
    let newline = complete.indexOf('\n')
    while (newline >= 0) {
      const line = complete.slice(start, newline)
      onLine(line.endsWith('\r') ? line.slice(0, -1) : line)
      start = newline + 1
      newline = complete.indexOf('\n', start)
    }
    carry = complete.slice(start)
  }
  return {
    write: (chunk) => {
      if (ended)
        throw new Error('The process output stream already ended.')
      consume(decoder.write(typeof chunk === 'string' ? Buffer.from(chunk) : chunk))
    },
    end: () => {
      if (ended)
        return
      ended = true
      consume(decoder.end())
      if (carry) {
        const line = carry
        carry = ''
        onLine(line)
      }
    },
    partial: () => carry,
  }
}
