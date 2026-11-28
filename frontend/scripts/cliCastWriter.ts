export interface AsciicastHeader {
  version: 2
  width: number
  height: number
  timestamp: number
  title: string
  env: {
    SHELL: string
    TERM: string
  }
}

export type AsciicastEvent = [number, 'o', string]

/** Normalize bare LF newlines into terminal CRLF sequences. */
export function normalizeCrlf(text: string): string {
  return text.replace(/\r?\n/g, '\r\n')
}

/**
 * Replace ephemeral run-directory paths with clean display paths while leaving
 * real IDs, branch names, and command outputs intact.
 */
export function sanitizeCastText(
  text: string,
  replacements: ReadonlyArray<readonly [string, string]>,
): string {
  let out = text
  for (const [raw, display] of replacements) {
    if (raw)
      out = out.split(raw).join(display)
  }
  return out
}

/**
 * Pretty-print a JSON value with standard 16-color ANSI SGR codes so it adapts
 * cleanly to both light and dark Dimidium terminal palettes.
 */
export function colorizeJsonAnsi(value: unknown): string {
  const pretty = JSON.stringify(value, null, 2)
  const tokenPattern = /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g
  return pretty.replace(tokenPattern, (match, str: string | undefined, colon: string | undefined, keyword: string | undefined) => {
    if (str !== undefined) {
      if (colon !== undefined)
        return `\x1B[36m${str}\x1B[0m${colon}`
      return `\x1B[32m${str}\x1B[0m`
    }
    if (keyword === 'null')
      return `\x1B[90m${match}\x1B[0m`
    if (keyword !== undefined)
      return `\x1B[33m${match}\x1B[0m`
    return `\x1B[33m${match}\x1B[0m`
  })
}

/** Compact LeapMux block Unicode logo matching backend/internal/logging/banner.go. */
export const LEAPMUX_BANNER_LINES = [
  '  █   █▀▀ █▀█ █▀█ █▄ ▄█ █ █ █ █',
  '  █   █▀  █▀█ █▀▀ █ ▀ █ █ █ ▄▀▄',
  '  ▀▀▀ ▀▀▀ ▀ ▀ ▀   ▀   ▀ ▀▀▀ ▀ ▀',
] as const

const BANNER_TEAL_BOLD = '\x1B[1;38;2;13;148;136m'
const ANSI_RESET = '\x1B[0m'

/**
 * Deterministic asciicast v2 builder with natural typing cadence and CRLF
 * normalization.
 */
export class AsciicastWriter {
  private readonly header: AsciicastHeader
  private readonly events: AsciicastEvent[] = []
  private currentSec = 0
  private prngState = 0x5EED_CAFE

  constructor(options: { width: number, height: number, title: string, timestamp?: number }) {
    this.header = {
      version: 2,
      width: options.width,
      height: options.height,
      timestamp: options.timestamp ?? 1_764_320_000,
      title: options.title,
      env: {
        SHELL: '/bin/zsh',
        TERM: 'xterm-256color',
      },
    }
  }

  /** Advance playback clock without emitting output. */
  pause(ms: number): void {
    if (ms > 0)
      this.currentSec += ms / 1000
  }

  /** Emit raw terminal output immediately at the current timestamp. */
  write(text: string): void {
    if (!text)
      return
    const t = Number(this.currentSec.toFixed(3))
    this.events.push([
      t,
      'o',
      normalizeCrlf(text),
    ])
  }

  /** Emit a full line followed by CRLF. */
  writeLine(line: string): void {
    this.write(`${line}\n`)
  }

  /** Emit the bold teal LeapMux block Unicode banner at the top of the session. */
  writeBanner(): void {
    for (const line of LEAPMUX_BANNER_LINES)
      this.writeLine(`${BANNER_TEAL_BOLD}${line}${ANSI_RESET}`)
  }

  /**
   * Hold the final screen for `ms` milliseconds before the player loops.
   *
   * Asciicast v2 duration equals the timestamp of the last event, so advancing
   * the clock alone is dropped unless an event is emitted at the end timestamp.
   */
  holdEnd(ms: number): void {
    if (ms <= 0)
      return
    this.pause(ms)
    const t = Number(this.currentSec.toFixed(3))
    this.events.push([t, 'o', ANSI_RESET])
  }

  /** Emit a shell prompt using Dimidium ANSI colors (cyan dir, magenta branch, green glyph). */
  writePrompt(repo = 'payments-api', branch = 'main'): void {
    this.write(`\x1B[36m${repo}\x1B[0m \x1B[35m(${branch})\x1B[0m \x1B[32m❯\x1B[0m `)
  }

  /**
   * Simulate human typing of a shell line with slight deterministic cadence
   * variation, optionally wrapped in an ANSI SGR prefix (e.g. muted comments).
   */
  typeLine(text: string, options: { baseCharMs?: number, ansiPrefix?: string } = {}): void {
    const baseMs = options.baseCharMs ?? 28
    const prefix = options.ansiPrefix ?? ''
    if (prefix)
      this.write(prefix)
    for (const ch of text) {
      const jitter = (this.nextUnit() - 0.5) * 14
      const delay = ch === ' '
        ? baseMs + 10
        : Math.max(12, baseMs + jitter)
      this.pause(delay)
      this.write(ch)
    }
    if (prefix)
      this.write('\x1B[0m')
    this.pause(140)
    this.write('\n')
  }

  /** Stream multiline command output line-by-line with brisk terminal pacing. */
  writeOutputLines(text: string, perLineMs = 35): void {
    const cleaned = text.replace(/\r\n/g, '\n').replace(/\n+$/, '')
    if (!cleaned)
      return
    const lines = cleaned.split('\n')
    for (let i = 0; i < lines.length; i++) {
      if (i > 0)
        this.pause(perLineMs)
      this.writeLine(lines[i]!)
    }
  }

  /** Render the complete asciicast v2 NDJSON string. */
  toString(): string {
    const lines = [
      JSON.stringify(this.header),
      ...this.events.map(ev => JSON.stringify(ev)),
    ]
    return `${lines.join('\n')}\n`
  }

  private nextUnit(): number {
    // Deterministic xorshift32 in [0, 1) so regenerated casts never drift.
    let x = this.prngState
    x ^= x << 13
    x ^= x >>> 17
    x ^= x << 5
    this.prngState = x >>> 0
    return this.prngState / 0x1_0000_0000
  }
}
