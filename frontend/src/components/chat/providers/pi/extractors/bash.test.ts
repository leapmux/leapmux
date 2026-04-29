import { describe, expect, it } from 'vitest'
import { extractPiBash, piBashToCommandSource } from './bash'

describe('extractPiBash', () => {
  it('returns null for non-bash tool executions', () => {
    expect(extractPiBash({
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'read',
      args: { path: 'README.md' },
    })).toBeNull()
  })

  it('extracts command and output from a successful tool_execution_end (no marker, exitCode null)', () => {
    const out = extractPiBash({
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'bash',
      args: { command: 'ls -la' },
      result: {
        content: [{ type: 'text', text: 'total 48\nfile1\nfile2\n' }],
        details: {},
      },
      isError: false,
    })
    expect(out).toEqual({
      command: 'ls -la',
      output: 'total 48\nfile1\nfile2\n',
      // Pi only surfaces exit codes via the appended marker on non-zero
      // exits — a clean exit produces no marker, so `exitCode` stays null.
      exitCode: null,
      cancelled: false,
      truncated: false,
      fullOutputPath: null,
      isError: false,
    })
  })

  it('parses exitCode from the trailing "Command exited with code N" marker and strips it', () => {
    const out = extractPiBash({
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'bash',
      args: { command: 'false' },
      result: {
        content: [{ type: 'text', text: 'oops\n\nCommand exited with code 1' }],
        details: {},
      },
      isError: true,
    })
    expect(out).toMatchObject({
      output: 'oops',
      exitCode: 1,
      cancelled: false,
      isError: true,
    })
  })

  it('parses Command aborted as cancelled and strips it', () => {
    const out = extractPiBash({
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'bash',
      args: { command: 'sleep 1000' },
      result: {
        content: [{ type: 'text', text: 'partial output\n\nCommand aborted' }],
        details: {},
      },
      isError: true,
    })
    expect(out).toMatchObject({
      output: 'partial output',
      cancelled: true,
      exitCode: null,
    })
  })

  it('parses Command timed out as cancelled and strips it', () => {
    const out = extractPiBash({
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'bash',
      args: { command: 'sleep 1000' },
      result: {
        content: [{ type: 'text', text: 'Command timed out after 30 seconds' }],
        details: {},
      },
      isError: true,
    })
    expect(out).toMatchObject({
      output: '',
      cancelled: true,
      exitCode: null,
    })
  })

  it('does NOT parse the marker when isError is false (process printed a lookalike but succeeded)', () => {
    const out = extractPiBash({
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'bash',
      args: { command: 'echo "Command exited with code 99"' },
      result: {
        content: [{ type: 'text', text: 'Command exited with code 99' }],
        details: {},
      },
      isError: false,
    })
    expect(out).toMatchObject({
      output: 'Command exited with code 99',
      exitCode: null,
      cancelled: false,
      isError: false,
    })
  })

  it('matches Pi\'s actual trailing marker even when the process printed a lookalike earlier', () => {
    const out = extractPiBash({
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'bash',
      args: { command: 'mischief' },
      result: {
        content: [{
          type: 'text',
          // Process printed a fake marker mid-output, then Pi appended its
          // real one at the end on the actual exit-1 path.
          text: 'starting\n\nCommand exited with code 99\nmore output\n\nCommand exited with code 1',
        }],
        details: {},
      },
      isError: true,
    })
    expect(out).toMatchObject({
      // Only the trailing real marker is stripped; the earlier lookalike
      // stays in the output as legitimate process content.
      output: 'starting\n\nCommand exited with code 99\nmore output',
      exitCode: 1,
      cancelled: false,
    })
  })

  it('reads truncation from the nested details.truncation object and propagates fullOutputPath', () => {
    const out = extractPiBash({
      type: 'tool_execution_end',
      toolCallId: 'call-1',
      toolName: 'bash',
      args: { command: 'yes' },
      result: {
        content: [{ type: 'text', text: 'partial...' }],
        // Pi's actual wire: truncation flag lives in `details.truncation.truncated`,
        // not at the top level of `details`.
        details: {
          truncation: { truncated: true, outputLines: 1000, totalLines: 5000 },
          fullOutputPath: '/tmp/pi-bash-abc.log',
        },
      },
      isError: false,
    })
    expect(out).toMatchObject({
      truncated: true,
      fullOutputPath: '/tmp/pi-bash-abc.log',
    })
  })

  it('falls back to partialResult when result is absent (live update)', () => {
    const out = extractPiBash({
      type: 'tool_execution_update',
      toolCallId: 'call-1',
      toolName: 'bash',
      args: { command: 'ls' },
      partialResult: {
        content: [{ type: 'text', text: 'streaming...' }],
        details: {},
      },
    })
    expect(out?.output).toBe('streaming...')
    expect(out?.exitCode).toBeNull()
  })
})

describe('piBashToCommandSource', () => {
  it('propagates exit code and isError from a non-zero exit', () => {
    // The marker parser sets exitCode only when isError is true on the
    // wire; this is the realistic shape after extractPiBash runs.
    const source = piBashToCommandSource({
      command: 'false',
      output: '',
      exitCode: 1,
      cancelled: false,
      truncated: false,
      fullOutputPath: null,
      isError: true,
    })
    expect(source.isError).toBe(true)
    expect(source.exitCode).toBe(1)
  })

  it('marks interrupted commands', () => {
    const source = piBashToCommandSource({
      command: 'sleep 1000',
      output: '',
      exitCode: null,
      cancelled: true,
      truncated: false,
      fullOutputPath: null,
      isError: true,
    })
    expect(source.interrupted).toBe(true)
    expect(source.isError).toBe(true)
  })

  it('reports success when isError is false', () => {
    const source = piBashToCommandSource({
      command: 'true',
      output: 'ok',
      exitCode: null,
      cancelled: false,
      truncated: false,
      fullOutputPath: null,
      isError: false,
    })
    expect(source.isError).toBe(false)
  })
})
