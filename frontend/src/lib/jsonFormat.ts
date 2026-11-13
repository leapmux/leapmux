import { Formatter, FracturedJsonOptions } from 'fracturedjsonjs'

export const DEFAULT_JSON_LINE_LENGTH = 80

function createFormatter(lineLength: number): Formatter {
  const formatter = new Formatter()
  const options = new FracturedJsonOptions()
  options.MaxTotalLineLength = lineLength
  options.MaxInlineComplexity = 1
  formatter.Options = options
  return formatter
}

const formatter = createFormatter(DEFAULT_JSON_LINE_LENGTH)

/**
 * The longest JSON text, in UTF-16 code units, that prettifyJson lays out with
 * FracturedJson.
 *
 * FracturedJson costs about 0.35 ms per KiB of structured JSON in V8, 15 to 20 times
 * what the native `JSON.stringify` costs, and it runs on the main thread, mostly
 * while a chat row extracts or renders. Up to this length a layout stays near 6 ms.
 * A longer text gets one value per line (see prettifyJson), which uses more lines.
 */
export const FRACTURED_JSON_MAX_LENGTH = 16 * 1024

/**
 * Pretty-print JSON text or a plain JS value: with FracturedJson up to
 * FRACTURED_JSON_MAX_LENGTH, and with one value per line above it. No layout
 * changes a token of a text, so a number keeps its digits and a repeated key
 * stays. Returns a text that is not JSON unchanged.
 */
export function prettifyJson(input: unknown, lineLength = DEFAULT_JSON_LINE_LENGTH): string {
  const indentWidth = formatter.Options.IndentSpaces
  if (typeof input === 'string') {
    return input.length > FRACTURED_JSON_MAX_LENGTH
      ? layOutJsonText(input, indentWidth) ?? input
      : reformat(input, lineLength)
  }
  // The compact text decides the layout, and FracturedJson reads it.
  const compact = JSON.stringify(input)
  if (compact === undefined)
    return String(input)
  // A value has no spelling to keep, so the native layout is exact for it. With
  // the compact pass, it costs about 40 percent of what layOutJsonText costs.
  return compact.length > FRACTURED_JSON_MAX_LENGTH
    ? JSON.stringify(input, null, indentWidth)
    : reformat(compact, lineLength)
}

/** Lay out `text` with FracturedJson, or return it unchanged when it is not JSON. */
function reformat(text: string, lineLength: number): string {
  try {
    const columns = Number.isFinite(lineLength) && lineLength >= 1
      ? Math.floor(lineLength)
      : DEFAULT_JSON_LINE_LENGTH
    const selected = columns === DEFAULT_JSON_LINE_LENGTH
      ? formatter
      : createFormatter(columns)
    return selected.Reformat(text)
  }
  catch {
    return text
  }
}

/**
 * The most that the plain layout may grow a text, as a multiple of its length.
 * Each line repeats the indentation of its depth, so the layout of a text that
 * nests deeply grows with the square of the depth: 100 KB of nested brackets
 * would take gigabytes. An ordinary document grows 2 to 4 times.
 */
const MAX_LAYOUT_GROWTH = 16

// The character codes that the plain layout reads.
const TAB = 0x09
const LINE_FEED = 0x0A
const CARRIAGE_RETURN = 0x0D
const SPACE = 0x20
const QUOTE = 0x22
const PLUS = 0x2B
const COMMA = 0x2C
const MINUS = 0x2D
const DOT = 0x2E
const SLASH = 0x2F
const DIGIT_0 = 0x30
const DIGIT_1 = 0x31
const DIGIT_9 = 0x39
const COLON = 0x3A
const UPPER_A = 0x41
const UPPER_E = 0x45
const UPPER_F = 0x46
const OPEN_BRACKET = 0x5B
const BACKSLASH = 0x5C
const CLOSE_BRACKET = 0x5D
const LOWER_A = 0x61
const LOWER_B = 0x62
const LOWER_E = 0x65
const LOWER_F = 0x66
const LOWER_N = 0x6E
const LOWER_R = 0x72
const LOWER_T = 0x74
const LOWER_U = 0x75
const OPEN_BRACE = 0x7B
const CLOSE_BRACE = 0x7D

/** What the plain layout reads next. */
enum Expect {
  /** Any value. */
  Value,
  /** The key of an object member. */
  Key,
  /** The colon after a key. */
  Colon,
  /** A comma, the bracket that closes the open object or array, or the end of the text. */
  Next,
}

/**
 * The text that the plain layout writes for each punctuation mark, ending with
 * the indentation of one depth.
 */
interface Separators {
  comma: string
  openObject: string
  openArray: string
  closeObject: string
  closeArray: string
}

/**
 * Lay out JSON text with one value per line, indented `indentWidth` spaces for
 * each level of nesting, as `JSON.stringify(value, null, indentWidth)` lays out
 * a value.
 *
 * Each token keeps the exact characters of the text, and only the whitespace
 * between tokens changes:
 * - A number keeps its digits and its form: `9007199254740993`, `1e400`, `-0`
 *   and `1.50` stay as they are, where a round trip through `JSON.parse` would
 *   round, overflow or shorten them.
 * - A string keeps its escapes, such as `A\/` and `\u0041`.
 * - An object keeps every member in its order, a repeated key too.
 *
 * The layout reads the text in one pass, with no recursion, so its time is
 * linear in the length of the text. Returns undefined when the text is not one
 * JSON value, or when the layout would grow past MAX_LAYOUT_GROWTH times the text.
 */
function layOutJsonText(text: string, indentWidth: number): string | undefined {
  const end = text.length
  const maxLength = end * MAX_LAYOUT_GROWTH
  const parts: string[] = []
  let length = 0
  // The closing bracket of each open object or array, the innermost last.
  const closers: number[] = []
  // The separators that end at each depth, built on first use.
  const separatorsByDepth: Separators[] = []
  const separatorsAt = (depth: number): Separators => {
    let separators = separatorsByDepth[depth]
    if (separators === undefined) {
      const indent = ' '.repeat(depth * indentWidth)
      separators = {
        comma: `,\n${indent}`,
        openObject: `{\n${indent}`,
        openArray: `[\n${indent}`,
        closeObject: `\n${indent}}`,
        closeArray: `\n${indent}]`,
      }
      separatorsByDepth[depth] = separators
    }
    return separators
  }
  const write = (piece: string): void => {
    parts.push(piece)
    length += piece.length
  }

  let expect = Expect.Value
  let i = 0
  for (;;) {
    // Past the limit, the text nests too deeply for a layout to help the reader.
    if (length > maxLength)
      return undefined
    i = skipWhitespace(text, i)
    const code = text.charCodeAt(i)
    if (expect === Expect.Next) {
      const depth = closers.length
      if (depth === 0) {
        return i === end
          ? parts.join('')
          : undefined
      }
      const closer = closers[depth - 1]
      if (code === COMMA) {
        write(separatorsAt(depth).comma)
        expect = closer === CLOSE_BRACE
          ? Expect.Key
          : Expect.Value
      }
      else if (code === closer) {
        closers.pop()
        const separators = separatorsAt(depth - 1)
        write(code === CLOSE_BRACE
          ? separators.closeObject
          : separators.closeArray)
      }
      else {
        return undefined
      }
      i++
    }
    else if (expect === Expect.Colon) {
      if (code !== COLON)
        return undefined
      write(': ')
      expect = Expect.Value
      i++
    }
    else if (expect === Expect.Value && (code === OPEN_BRACE || code === OPEN_BRACKET)) {
      const isObject = code === OPEN_BRACE
      const closer = isObject ? CLOSE_BRACE : CLOSE_BRACKET
      const inside = skipWhitespace(text, i + 1)
      if (text.charCodeAt(inside) === closer) {
        write(isObject ? '{}' : '[]')
        expect = Expect.Next
        i = inside + 1
      }
      else {
        closers.push(closer)
        const separators = separatorsAt(closers.length)
        write(isObject ? separators.openObject : separators.openArray)
        expect = isObject ? Expect.Key : Expect.Value
        i = inside
      }
    }
    else {
      // A key is a string; a value is a string, a number or a literal.
      let tokenEnd = -1
      if (code === QUOTE)
        tokenEnd = scanString(text, i)
      else if (expect === Expect.Value)
        tokenEnd = scanLiteral(text, i)
      if (tokenEnd < 0)
        return undefined
      write(text.slice(i, tokenEnd))
      expect = expect === Expect.Key
        ? Expect.Colon
        : Expect.Next
      i = tokenEnd
    }
  }
}

/** The index of the first character at or after `from` that is not JSON whitespace. */
function skipWhitespace(text: string, from: number): number {
  const end = text.length
  let i = from
  while (i < end) {
    const code = text.charCodeAt(i)
    if (code !== SPACE && code !== LINE_FEED && code !== CARRIAGE_RETURN && code !== TAB)
      return i
    i++
  }
  return end
}

/**
 * The index after the JSON string that starts at `start` (a quote), or -1 when
 * no valid string starts there: it has a bad escape, an unescaped control
 * character, or no closing quote.
 */
function scanString(text: string, start: number): number {
  const end = text.length
  let i = start + 1
  while (i < end) {
    const code = text.charCodeAt(i)
    if (code === QUOTE)
      return i + 1
    if (code === BACKSLASH) {
      const escapeLength = escapeLengthAt(text, i)
      if (escapeLength === 0)
        return -1
      i += escapeLength
    }
    else if (code < SPACE) {
      return -1
    }
    else {
      i++
    }
  }
  return -1
}

/** The length of the escape that starts at `start` (a backslash), or 0 when it is not valid JSON. */
function escapeLengthAt(text: string, start: number): number {
  switch (text.charCodeAt(start + 1)) {
    case QUOTE:
    case BACKSLASH:
    case SLASH:
    case LOWER_B:
    case LOWER_F:
    case LOWER_N:
    case LOWER_R:
    case LOWER_T:
      return 2
    case LOWER_U:
      for (let i = start + 2; i < start + 6; i++) {
        if (!isHexDigit(text.charCodeAt(i)))
          return 0
      }
      return 6
    default:
      return 0
  }
}

function isHexDigit(code: number): boolean {
  return (code >= DIGIT_0 && code <= DIGIT_9)
    || (code >= UPPER_A && code <= UPPER_F)
    || (code >= LOWER_A && code <= LOWER_F)
}

/** The index after the number, `true`, `false` or `null` at `start`, or -1 when none starts there. */
function scanLiteral(text: string, start: number): number {
  for (const word of ['true', 'false', 'null']) {
    if (text.startsWith(word, start))
      return start + word.length
  }
  return scanNumber(text, start)
}

/** The index after the JSON number at `start`, or -1 when no valid number starts there. */
function scanNumber(text: string, start: number): number {
  let i = start
  if (text.charCodeAt(i) === MINUS)
    i++
  const first = text.charCodeAt(i)
  if (first === DIGIT_0)
    i++
  else if (first >= DIGIT_1 && first <= DIGIT_9)
    i = skipDigits(text, i + 1)
  else
    return -1
  if (text.charCodeAt(i) === DOT) {
    const fractionEnd = skipDigits(text, i + 1)
    if (fractionEnd === i + 1)
      return -1
    i = fractionEnd
  }
  const exponent = text.charCodeAt(i)
  if (exponent === LOWER_E || exponent === UPPER_E) {
    let digitsStart = i + 1
    const sign = text.charCodeAt(digitsStart)
    if (sign === PLUS || sign === MINUS)
      digitsStart++
    const exponentEnd = skipDigits(text, digitsStart)
    if (exponentEnd === digitsStart)
      return -1
    i = exponentEnd
  }
  return i
}

/** The index of the first character at or after `from` that is not a decimal digit. */
function skipDigits(text: string, from: number): number {
  const end = text.length
  let i = from
  while (i < end) {
    const code = text.charCodeAt(i)
    if (code < DIGIT_0 || code > DIGIT_9)
      return i
    i++
  }
  return end
}

/**
 * Pretty-print a tool-args object, returning `''` for null/undefined and for
 * empty objects. Used by MCP extractors so that an absent or `{}` `arguments`
 * field renders as no body rather than as a literal empty `{}`.
 */
export function prettifyArgsJson(args: unknown, lineLength?: number): string {
  if (args === undefined || args === null)
    return ''
  if (typeof args === 'object' && !Array.isArray(args)
    && Object.keys(args as Record<string, unknown>).length === 0) {
    return ''
  }
  return prettifyJson(args, lineLength)
}

/**
 * Pretty-print MCP `structuredContent`, returning `undefined` when the value
 * is absent or empty. Per MCP spec the field is a JSON value; in practice
 * it's always an object/array, but we accept any non-null/non-empty value
 * so neither Claude nor Codex silently drops a payload.
 */
export function prettifyStructuredJson(input: unknown): string | undefined {
  if (input === undefined || input === null || input === '')
    return undefined
  return prettifyJson(input)
}
