import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import '../plugin'

const path = '/native/dsh-subprocess-abcdef/dsh-subprocess-123-456-0123456789ab-stdout.log'
const frame = {
  type: 'tool/result',
  seq: 2,
  data: {
    message: {
      toolCallId: 'native-call',
      isError: false,
      content: [
        {
          type: 'text',
          text: 'native inline preview\n[output truncated; full output: /native/dsh-subprocess-abcdef/dsh-subprocess-123-456-0123456789ab-stdout.log]\n[exit code: 0]',
        },
      ],
    },
  },
}
const options = {
  spanId: 'native-call',
  spanType: 'bash',
  agentSessionId: 'native-session',
}

describe('registered output file paths', () => {
  it('attaches the native filesystem pointer without changing the native frame', () => {
    const before = JSON.stringify(frame)
    const call = providerToolCall(AgentProvider.DEEPSEEK_HARNESS, frame, options)

    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toEqual([path])
    expect(JSON.stringify(frame)).toBe(before)
  })
})

describe('native path ownership and preview preservation', () => {
  it.each([null, false, 0, -1, '', [], {}])('refuses a non-native payload without deriving a new call: %j', (payload) => {
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
    expect(pathsFor(payload)).toEqual([])
  })

  it('refuses another native call while the original call stays fixed', () => {
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
    const foreign: unknown = JSON.parse(JSON.stringify(frame).replaceAll('native-call', 'foreign-call'))
    expect(pathsFor(foreign)).toEqual([])
  })

  it.each(['request', 'none', 'other'] as const)('supplies no file path for the %s role', (role) => {
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
    expect(pathsFor(frame, { role })).toEqual([])
  })

  it.each(['file:///native/opaque', 'https://example.com/result', 'zcode-artifact://session/id', ' ', 'relative/output', '/native/zero\0byte'])('refuses a non-filesystem pointer: %j', (invalid) => {
    const payload: unknown = JSON.parse(JSON.stringify(frame).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(invalid).slice(1, -1)))
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
    expect(pathsFor(payload)).toEqual([])
  })

  it('keeps the native preview when an unrelated supplement supplies complete text', () => {
    const forged = 'FORGED_COMPLETE_BODY'
    const supplementalContent = {
      sessionId: options.agentSessionId,
      toolCallId: 'native-call',
      toolName: options.spanType,
      outputFile: { path, text: forged, output: forged },
      completeOutput: { path, text: forged, sessionId: options.agentSessionId, toolCallId: 'native-call' },
    }
    const before = JSON.stringify(frame)
    const meta = providerToolMeta(AgentProvider.DEEPSEEK_HARNESS, frame, { ...options, supplementalContent })
    if (!meta)
      throw new Error('The native Copy preview requires a valid tool metadata object.')
    const quote = meta.copyableContent()
    expect(quote).toContain('native inline preview')
    expect(quote).not.toContain(forged)
    expect(JSON.stringify(frame)).toBe(before)
  })
})

// The native spill store writes `<root>/session-<12 hex>/<12 hex>-<encoded name>`.
// Source: deepseek-harness packages/spill/spill-local/src/store.ts, sessionDir and saveTextFile.
describe('native formatted result layout', () => {
  function formatted(nativePath: string) {
    const text = `native preview\n\n(Omitted 672056 bytes. Full formatted result stored at: ${nativePath}. Use read with offset/limit, or grep this path to search within it.)`
    return { ...frame, data: { message: { ...frame.data.message, content: [{ type: 'text', text }] } } }
  }

  it.each([
    '/private/var/folders/ab/T/dsh-spill-nFARXy/session-c0dafcfbe537/e16aba412233-bash.txt',
    '/private/var/folders/ab/T/dsh-spill-mnf47T/session-e701b1becf4c/e4cb5425d310-mcp__results__inspect.txt',
    '/work/project/.spill/session-c22bc3f1d2af/8a7b6c5d4e3f-bash.txt',
    '/private/var/folders/ab/T/dsh-spill-q1W2e3/session-0123456789ab/0123456789ab-tool~0020name.txt',
    String.raw`C:\Users\native\AppData\Local\Temp\dsh-spill-q1W2e3\session-0123456789ab\0123456789ab-bash.txt`,
  ])('reads the native spill path: %j', (nativePath) => {
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
    expect(pathsFor(formatted(nativePath))).toEqual([nativePath])
  })

  it.each([
    '/native/never-open-this.txt',
    '/native/dsh-spill-q1W2e3/0123456789ab-bash.txt',
    '/native/dsh-spill-q1W2e3/session-0123456789AB/0123456789ab-bash.txt',
    '/native/dsh-spill-q1W2e3/session-0123456789a/0123456789ab-bash.txt',
    '/native/dsh-spill-q1W2e3/session-0123456789ab/0123456789a-bash.txt',
    '/native/dsh-spill-q1W2e3/session-0123456789ab/0123456789ab-tool name.txt',
    'dsh-spill-q1W2e3/session-0123456789ab/0123456789ab-bash.txt',
  ])('refuses a path outside the native spill layout: %j', (invalid) => {
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
    expect(pathsFor(formatted(invalid))).toEqual([])
  })

  it('reads both native pointers of one truncated command in their native order', () => {
    const spill = '/private/var/folders/ab/T/dsh-spill-nFARXy/session-c0dafcfbe537/e16aba412233-bash.txt'
    const text = `native preview\n[output truncated; full output: ${path}]\n\n(Omitted 49 bytes. Full formatted result stored at: ${spill}. Use read with offset/limit, or grep this path to search within it.)`
    const native = { ...frame, data: { message: { ...frame.data.message, content: [{ type: 'text', text }] } } }
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
    expect(pathsFor(native)).toEqual([path, spill])
  })
})

describe('native formatted path punctuation', () => {
  it.each([
    '/native/project. notes/dsh-spill-q1W2e3/session-0123456789ab/0123456789ab-bash.txt',
    String.raw`C:\native\project. notes\dsh-spill-q1W2e3\session-0123456789ab\0123456789ab-bash.txt`,
  ])('keeps a period and space inside the declared native path: %j', (nativePath) => {
    const previewText = `(Output omitted. Full formatted result stored at: ${nativePath}. Use read_file.)`
    const native = { ...frame, data: { message: { ...frame.data.message, content: [{ type: 'text', text: previewText }] } } }
    const before = JSON.stringify(native)
    const call = providerToolCall(AgentProvider.DEEPSEEK_HARNESS, native, options)
    expect(call).not.toBeNull()
    expect(call?.outputFilePaths).toEqual([nativePath])
    const pathsFor = outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)
    expect(pathsFor(native)).toEqual([nativePath])
    const meta = providerToolMeta(AgentProvider.DEEPSEEK_HARNESS, native, options)
    expect(meta?.copyableContent()).toBe(previewText)
    expect(JSON.stringify(native)).toBe(before)
  })
})

// Native sources, `@deepseek-ai/dsh` 0.2.0-rc.2:
// - `dsh-tool-fs-search` formatGrepOutput and formatGlobPage write the stored-result notices.
// - `dsh-tool-bash` renderJobRead, `dsh-tool-pwsh` renderJobRead and `dsh-tool-jobs`
//   renderModelDelta write the dropped-output notice. The notice lists the job spill files
//   joined with ", ", or "(unavailable)" when the job has none.
// - `dsh-subprocess-local` names a stream file `dsh-subprocess-<pid>-<n>-<12 hex>-<label>.log`
//   in a `dsh-subprocess-XXXXXX` directory.
describe('native stored-result and dropped-output notices', () => {
  const stdout = '/private/var/folders/ab/T/dsh-subprocess-AbC123/dsh-subprocess-4242-1-0123456789ab-stdout.log'
  const stderr = '/private/var/folders/ab/T/dsh-subprocess-AbC123/dsh-subprocess-4242-2-0123456789ab-stderr.log'
  const grepSpill = '/private/var/folders/ab/T/dsh-spill-nFARXy/session-c0dafcfbe537/e16aba412233-grep-results.txt'
  const globSpill = '/private/var/folders/ab/T/dsh-spill-nFARXy/session-c0dafcfbe537/0a1b2c3d4e5f-glob-results.txt'
  const hint = 'Use read with offset/limit, or grep this path to search within it.'

  function native(text: string): unknown {
    return { ...frame, data: { message: { ...frame.data.message, content: [{ type: 'text', text }] } } }
  }

  function pathsOf(text: string): readonly string[] {
    return outputFilePathFixture(AgentProvider.DEEPSEEK_HARNESS, frame, options)(native(text))
  }

  const grepNotice = (nativePath: string) => `Found 200 of 6000 matches\n\n/w/big.txt\nLine 1: GGG-0\n\n(Full grep result stored at: ${nativePath}. ${hint})`
  const globNotice = (nativePath: string) => `/w/a.ts\n/w/b.ts\n\n(Showing 2 of 900 paths. Full sorted result stored at: ${nativePath}. ${hint})`
  const droppedNotice = (listed: string) => `tail\n[some output was dropped from memory; full output: ${listed}]`

  it('reads the stored result of a capped grep', () => {
    expect(pathsOf(grepNotice(grepSpill))).toEqual([grepSpill])
  })

  it('reads the stored result of a capped glob', () => {
    expect(pathsOf(globNotice(globSpill))).toEqual([globSpill])
  })

  it('reads the stored result of a glob that the native tool sampled', () => {
    const text = `/w/a.ts\n\n(Showing 1 of 900 paths, sampled across 1 of the 3 top-level entries this pattern matched instead of taken in modification-time order. Narrow path to inspect a specific subtree. Full sorted result stored at: ${globSpill}. ${hint})`
    expect(pathsOf(text)).toEqual([globSpill])
  })

  it('reads both stream files of a promoted command in their native order', () => {
    const text = `${droppedNotice(`${stdout}, ${stderr}`)}\n[still running after 30000ms; moved to background job job-1]\nThe command keeps running in the background. You will be notified when it finishes; read newer output with job_output, stop it with job_kill.`
    expect(pathsOf(text)).toEqual([stdout, stderr])
  })

  it('reads the stream file of a job output read', () => {
    expect(pathsOf(droppedNotice(stdout))).toEqual([stdout])
  })

  it('reads a dropped-output notice that opens the result', () => {
    expect(pathsOf(`[some output was dropped from memory; full output: ${stdout}]`)).toEqual([stdout])
  })

  it('reads the notices of one result in their native order and lists a file once', () => {
    const text = `native preview\n[output truncated; full output: ${stdout}]\n[stderr]\nerr\n[some output was dropped from memory; full output: ${stdout}, ${stderr}]`
    expect(pathsOf(text)).toEqual([stdout, stderr])
  })

  it('reads the stream notice before the stored-result notice of one result', () => {
    expect(pathsOf(`${droppedNotice(stdout)}\n${grepNotice(grepSpill)}`)).toEqual([stdout, grepSpill])
  })

  it('refuses a listed entry outside the native stream layout and keeps the valid one', () => {
    expect(pathsOf(droppedNotice(`/native/never-open-this.log, ${stdout}`))).toEqual([stdout])
  })

  it('refuses a listed path that holds the list separator', () => {
    // The native list joins the paths with ", ", so a path that holds one has no unique reading.
    expect(pathsOf(droppedNotice('/native/a, b/dsh-subprocess-AbC123/dsh-subprocess-4242-1-0123456789ab-stdout.log'))).toEqual([])
  })

  it.each([
    ['a stream notice', 'native inline preview\n[output truncated; full output: (unavailable)]\n[exit code: 0]'],
    ['a dropped-output notice', 'tail\n[some output was dropped from memory; full output: (unavailable)]'],
    ['a grep that could not be saved', 'Found 200 of 6000 matches\n\n/w/big.txt\nLine 1: GGG-0\n\n(The complete result could not be saved; narrow pattern, path, or include to see more.)'],
    ['a glob that could not be saved', '/w/a.ts\n/w/b.ts\n\n(Showing 2 of 900 paths. The complete result could not be saved; narrow pattern or path to see more.)'],
  ])('supplies no path for %s', (_label, text) => {
    expect(pathsOf(text)).toEqual([])
  })

  it('keeps the stderr file when the stdout file is unavailable', () => {
    const text = `out\n[output truncated; full output: (unavailable)]\n[stderr]\nerr\n[output truncated; full output: ${stderr}]`
    expect(pathsOf(text)).toEqual([stderr])
  })

  it.each([
    '/native/never-open-this.txt',
    '/native/dsh-spill-q1W2e3/0123456789ab-grep-results.txt',
    '/native/dsh-spill-q1W2e3/session-0123456789AB/0123456789ab-grep-results.txt',
    'file:///native/opaque',
    'opaque-id',
  ])('refuses a stored result outside the native spill layout: %j', (invalid) => {
    expect(pathsOf(grepNotice(invalid))).toEqual([])
    expect(pathsOf(globNotice(invalid))).toEqual([])
  })
})
