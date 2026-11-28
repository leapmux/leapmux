import { describe, expect, it } from 'vitest'
import {
  AsciicastWriter,
  colorizeJsonAnsi,
  normalizeCrlf,
  sanitizeCastText,
} from './cliCastWriter'

describe('normalizeCrlf', () => {
  it('converts bare LF to CRLF without doubling existing CRLF', () => {
    expect(normalizeCrlf('a\nb\r\nc\n')).toBe('a\r\nb\r\nc\r\n')
  })
})

describe('sanitizeCastText', () => {
  it('replaces ephemeral paths while leaving non-matching text intact', () => {
    const out = sanitizeCastText(
      'worktree=/private/tmp/lm-cast-123/repos/payments-api branch=feat/api',
      [
        ['/private/tmp/lm-cast-123/repos/payments-api', '~/src/payments-api'],
      ],
    )
    expect(out).toBe('worktree=~/src/payments-api branch=feat/api')
  })
})

describe('colorizeJsonAnsi', () => {
  it('applies ANSI colors to keys, strings, booleans, numbers, and null', () => {
    const colored = colorizeJsonAnsi({
      agent_id: 'a1',
      ready: true,
      elapsed_ms: 820,
      error: null,
    })
    expect(colored).toContain('\x1B[36m"agent_id"\x1B[0m:')
    expect(colored).toContain('\x1B[32m"a1"\x1B[0m')
    expect(colored).toContain('\x1B[33mtrue\x1B[0m')
    expect(colored).toContain('\x1B[33m820\x1B[0m')
    expect(colored).toContain('\x1B[90mnull\x1B[0m')
  })
})

describe('AsciicastWriter', () => {
  it('emits valid asciicast v2 NDJSON with monotonic timestamps and CRLF', () => {
    const writer = new AsciicastWriter({
      width: 86,
      height: 22,
      title: 'CLI demo',
      timestamp: 1_764_320_000,
    })
    writer.writePrompt('payments-api', 'main')
    writer.typeLine('leapmux agent wait "$A1" --any --json', { baseCharMs: 20 })
    writer.pause(300)
    writer.writeOutputLines('line 1\nline 2\n', 40)

    const lines = writer.toString().trim().split('\n')
    const header = JSON.parse(lines[0]!) as Record<string, unknown>
    expect(header.version).toBe(2)
    expect(header.width).toBe(86)
    expect(header.height).toBe(22)

    let prevTime = -1
    let combined = ''
    for (const raw of lines.slice(1)) {
      const [t, kind, data] = JSON.parse(raw) as [number, string, string]
      expect(kind).toBe('o')
      expect(t).toBeGreaterThanOrEqual(prevTime)
      prevTime = t
      combined += data
    }
    expect(combined).toContain('payments-api')
    expect(combined).toContain('leapmux agent wait "$A1" --any --json\r\n')
    expect(combined).toContain('line 1\r\nline 2\r\n')
  })

  it('writes the LeapMux Unicode banner and preserves trailing holdEnd duration', () => {
    const writer = new AsciicastWriter({
      width: 100,
      height: 28,
      title: 'Banner and hold test',
      timestamp: 1_764_320_000,
    })
    writer.writeBanner()
    writer.writeLine('')
    writer.writePrompt()
    writer.typeLine('leapmux agent list', { baseCharMs: 20 })
    const beforeHoldLines = writer.toString().trim().split('\n')
    const [beforeT] = JSON.parse(beforeHoldLines[beforeHoldLines.length - 1]!) as [number, string, string]

    writer.holdEnd(5000)
    const afterHoldLines = writer.toString().trim().split('\n')
    const [afterT] = JSON.parse(afterHoldLines[afterHoldLines.length - 1]!) as [number, string, string]

    expect(afterT - beforeT).toBeCloseTo(5.0, 2)
    expect(writer.toString()).toContain('█   █▀▀ █▀█ █▀█ █▄ ▄█ █ █ █ █')
  })
})
