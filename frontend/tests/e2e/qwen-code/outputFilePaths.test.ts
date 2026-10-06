import { spawnSync } from 'node:child_process'
import { describe, expect, it } from 'vitest'
import { qwenModelOutputPath, qwenOutputPathCommand, qwenOutputPathReceipt } from './outputFilePaths'

const PATH = '/native/run_shell_command_123456abcdef.output'
const NOTICE = `Tool output was too large and has been truncated.\nThe full output has been saved to: ${PATH}\nNative preview.`

/** The errors of the receipt reader, one for each check. */
const RECEIPT_ERROR = {
  NotFinal: 'The native Qwen receipt requires a completed or failed tool call.',
  NoContent: 'The native Qwen result requires a content array.',
  NoNotice: 'The native Qwen background result requires one output path notice.',
  NoShellResult: 'The native Qwen shell result requires its original paths and preview.',
} as const

/** The errors of the model result reader, one for each check. */
const MODEL_ERROR = {
  InvalidArray: 'The native Qwen model result contains an invalid content array.',
  NoBlocks: 'The native Qwen model result requires text blocks.',
  NonText: 'The native Qwen model result contains a non-text block.',
  NoNotice: 'The native Qwen model result requires its exact output path notice.',
} as const

function frame(patch: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    sessionUpdate: 'tool_call_update',
    toolCallId: 'native-path',
    status: 'completed',
    content: [{ type: 'content', content: { type: 'text', text: NOTICE } }],
    rawOutput: { type: 'shell_result', version: 1, outputFiles: [PATH], output: 'Native inline preview.', error: null, exitCode: 0 },
    ...patch,
  }
}

describe('qwenOutputPathReceipt', () => {
  it('keeps the original successful preview and zero exit code', () => {
    const native = frame()
    const before = structuredClone(native)
    expect(qwenOutputPathReceipt(native)).toEqual({ callId: 'native-path', status: 'completed', paths: [PATH], preview: 'Native inline preview.', exitCode: 0 })
    expect(native).toEqual(before)
  })

  it('keeps the original failure preview', () => {
    expect(qwenOutputPathReceipt(frame({ status: 'failed' })).preview).toBe(NOTICE)
  })

  it.each(['', '0', 'false'])('preserves the native preview %j', (preview) => {
    expect(qwenOutputPathReceipt(frame({ rawOutput: { type: 'shell_result', version: 1, outputFiles: [PATH], output: preview, error: null } })).preview).toBe(preview)
  })

  it('reads background notices without a structured result', () => {
    const native = frame()
    delete native.rawOutput
    expect(qwenOutputPathReceipt(native)).toEqual({ callId: 'native-path', status: 'completed', paths: [PATH], preview: NOTICE })
  })

  it.each(([
    [{ sessionUpdate: 'tool_call' }, RECEIPT_ERROR.NotFinal],
    [{ toolCallId: '' }, RECEIPT_ERROR.NotFinal],
    [{ status: 'pending' }, RECEIPT_ERROR.NotFinal],
    [{ content: null }, RECEIPT_ERROR.NoContent],
    [{ rawOutput: { type: 'foreign_result' } }, RECEIPT_ERROR.NoShellResult],
    [{ rawOutput: { type: 'shell_result', version: 2 } }, RECEIPT_ERROR.NoShellResult],
    [{ rawOutput: { type: 'shell_result', version: 1, outputFiles: [PATH, 0], output: '', error: null } }, RECEIPT_ERROR.NoShellResult],
    [{ rawOutput: { type: 'shell_result', version: 1, outputFiles: ['/native/invalid\0.output'], output: '', error: null } }, RECEIPT_ERROR.NoShellResult],
  ] as const).map(([patch, error], index) => ({ patch, error, index })))('refuses invalid native receipt $index', ({ patch, error }) => {
    expect(() => qwenOutputPathReceipt(frame(patch))).toThrow(error)
  })
})

describe('qwenModelOutputPath', () => {
  it('reads the native pointer without opening the file', () => {
    expect(qwenModelOutputPath(NOTICE)).toBe(PATH)
    expect(qwenModelOutputPath(JSON.stringify([{ type: 'text', text: NOTICE }]))).toBe(PATH)
  })

  it.each([
    ['ordinary output', MODEL_ERROR.NoNotice],
    [`prefix\n${NOTICE}`, MODEL_ERROR.NoNotice],
    ['[]', MODEL_ERROR.NoBlocks],
    ['[broken', MODEL_ERROR.InvalidArray],
    [JSON.stringify([{ type: 'image', data: '' }]), MODEL_ERROR.NonText],
  ])('refuses unrelated model output %j', (value, error) => {
    expect(() => qwenModelOutputPath(value)).toThrow(error)
  })

  it.each([
    '/native/ leading.output',
    '/native/trailing.output ',
    '/native/internal space.output',
    '/native/한글😀.output',
    'C:\\native\\file.output',
  ])('keeps the exact native pointer spelling %j', (path) => {
    const notice = `Tool output was too large and has been truncated.\nThe full output has been saved to: ${path}\nNative preview.`
    expect(qwenModelOutputPath(notice)).toBe(path)
    const native = frame({ content: [{ type: 'content', content: { type: 'text', text: notice } }] })
    delete native.rawOutput
    expect(qwenOutputPathReceipt(native).paths).toEqual([path])
  })

  it.each([
    { name: 'missing marker period', text: NOTICE.replace('truncated.', 'truncated') },
    { name: 'wrong marker', text: NOTICE.replace('has been truncated', 'was retained') },
    { name: 'wrong pointer label', text: NOTICE.replace('has been saved to:', 'was saved at:') },
    { name: 'pointer before marker', text: `The full output has been saved to: ${PATH}\nTool output was too large and has been truncated.\nPreview.` },
    { name: 'empty pointer', text: 'Tool output was too large and has been truncated.\nThe full output has been saved to: \nPreview.' },
    { name: 'NUL pointer', text: 'Tool output was too large and has been truncated.\nThe full output has been saved to: /native/invalid\0.output\nPreview.' },
    { name: 'missing following line', text: `Tool output was too large and has been truncated.\nThe full output has been saved to: ${PATH}` },
  ])('refuses a native $name in both packet shapes', ({ text }) => {
    expect(() => qwenModelOutputPath(text)).toThrow(MODEL_ERROR.NoNotice)
    const native = frame({ content: [{ type: 'content', content: { type: 'text', text } }] })
    delete native.rawOutput
    expect(() => qwenOutputPathReceipt(native)).toThrow(RECEIPT_ERROR.NoNotice)
  })

  it('refuses two native notice blocks in the original model packet', () => {
    expect(() => qwenModelOutputPath(JSON.stringify([{ type: 'text', text: NOTICE }, { type: 'text', text: NOTICE }]))).toThrow(MODEL_ERROR.NoNotice)
    const native = frame({ content: [NOTICE, NOTICE].map(text => ({ type: 'content', content: { type: 'text', text } })) })
    delete native.rawOutput
    expect(() => qwenOutputPathReceipt(native)).toThrow(RECEIPT_ERROR.NoNotice)
  })

  it('refuses a native notice after an unrelated model block', () => {
    expect(() => qwenModelOutputPath(JSON.stringify([{ type: 'text', text: 'Ordinary output.' }, { type: 'text', text: NOTICE }]))).toThrow(MODEL_ERROR.NoNotice)
  })

  it('keeps a later pointer-shaped preview line outside the native header', () => {
    const text = `${NOTICE}\nThe full output has been saved to: /native/echoed.output`
    expect(qwenModelOutputPath(text)).toBe(PATH)
  })
})

describe('qwenOutputPathCommand', () => {
  it('keeps the original line count and padding for the native path trigger', () => {
    const output = qwenOutputPathCommand('NATIVEOUTPUTPATH', 7)
    expect(output.command).toMatch(/^node -e '[^']*'$/u)
    const program = output.command.slice('node -e \''.length, -1)
    const actual = spawnSync(process.execPath, ['-e', program], { encoding: 'utf8', maxBuffer: 2 * 1024 * 1024 })
    expect(actual.error).toBeUndefined()
    expect(actual.status).toBe(7)
    const lines = actual.stdout.split('\n')
    expect(lines).toHaveLength(8001)
    expect(lines[0]).toBe(`NATIVEOUTPUTPATH-line-0:${'x'.repeat(30)}`)
    expect(lines[4000]).toBe(output.omittedMarker)
    expect(lines[8000]).toBe('NATIVEOUTPUTPATH-complete-42')
    expect(lines[8000]).toBe(output.lastMarker)
    expect(output.command).not.toContain(output.omittedMarker)
    expect(output.command).not.toContain(output.lastMarker)
    expect(output.omittedMarker).toBe(`NATIVEOUTPUTPATH-line-4000:${'x'.repeat(30)}-middle-77`)
  })

  it.each(['', 'invalid prefix'])('refuses the invalid prefix %j', (prefix) => {
    expect(() => qwenOutputPathCommand(prefix)).toThrow('The native tool output prefix requires at most eighty ASCII letters and digits.')
  })

  it.each([-1, 256, 1.5])('refuses the invalid exit code %j', (code) => {
    expect(() => qwenOutputPathCommand('PREFIX', code)).toThrow('A native output command requires an exit code from 0 through 255.')
  })
})

describe('native filesystem boundaries', () => {
  it.each(['https://example.com/output', 'file:///native/output', 'zcode-artifact://session/opaque', ' ', '\t', 'relative/output', ' /native/leading-relative.output'])('refuses a non-filesystem pointer independently in both receipts: %j', (path) => {
    const notice = `Tool output was too large and has been truncated.\nThe full output has been saved to: ${path}\nNative preview.`
    const structured = frame({ rawOutput: { type: 'shell_result', version: 1, outputFiles: [path], output: 'Native inline preview.', error: null } })
    const background = frame({ content: [{ type: 'content', content: { type: 'text', text: notice } }] })
    delete background.rawOutput
    expect(() => qwenOutputPathReceipt(structured)).toThrow(RECEIPT_ERROR.NoShellResult)
    expect(() => qwenOutputPathReceipt(background)).toThrow(RECEIPT_ERROR.NoNotice)
    expect(() => qwenModelOutputPath(notice)).toThrow(MODEL_ERROR.NoNotice)
  })

  it.each(['pending', 'in_progress', 'cancelled', 'timed_out', '', 'foreign', undefined, null])('refuses a non-final native status independently: %j', (status) => {
    expect(() => qwenOutputPathReceipt(frame({ status }))).toThrow(RECEIPT_ERROR.NotFinal)
  })

  it.each(['cancelled', 'timed_out'])('keeps the failure preview for a native %s shell outcome', (outcome) => {
    const native = frame({ status: 'failed', rawOutput: { type: 'shell_result', version: 1, outcome, outputFiles: [PATH], output: 'Native inline preview.', error: null } })
    expect(qwenOutputPathReceipt(native)).toMatchObject({ status: 'failed', paths: [PATH], preview: NOTICE })
  })
})
