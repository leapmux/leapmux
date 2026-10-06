import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it } from 'vitest'
import { grokWorkflowCompletion, grokWorkflowLaunch, grokWorkflowManifestPath, grokWorkflowName, grokWorkflowReportLabel, readGrokWorkflowManifest } from './codeExecution'

const directories: string[] = []
const scratchRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
function createDirectory(prefix: string): string {
  mkdirSync(scratchRoot, { recursive: true })
  const directory = mkdtempSync(join(scratchRoot, prefix))
  directories.push(directory)
  return directory
}
afterEach(() => {
  for (const path of directories.splice(0))
    rmSync(path, { recursive: true, force: true })
})
const launch = { runId: 'wf-1', name: 'code-output', scriptPath: '/private/sessions/cwd/session/workflows/wf-1/script.rhai' }
function manifest(status: string, text: string) {
  return {
    version: 4,
    script_revision: 0,
    state: {
      run_id: launch.runId,
      name: launch.name,
      status,
      ...(status === 'complete' ? { result_summary: text } : { pause_message: text }),
      history: [{ event: status === 'complete' ? 'workflow_completed' : 'workflow_failed', detail: status === 'failed' ? text : null, at: 'native-time' }],
    },
  }
}
function frames(value: unknown) {
  return [{ sessionUpdate: 'tool_call_update', toolCallId: 'launch', status: 'completed', rawOutput: value }]
}

describe('grokWorkflowLaunch', () => {
  it('reads the real launch result with its task alias and full tool output', () => {
    expect(grokWorkflowLaunch(frames({ type: 'Workflow', run_id: launch.runId, task_id: launch.runId, name: launch.name, script_path: launch.scriptPath }), 'launch')).toEqual(launch)
  })
  it.each([
    { value: null },
    { value: {} },
    { value: { type: 'Workflow', run_id: 'wf-1', task_id: 'other', name: 'code', script_path: launch.scriptPath } },
    { value: { type: 'Workflow', run_id: 'wf-1', task_id: 'wf-1', name: 'code', script_path: 'relative' } },
  ])('rejects an incomplete native launch', ({ value }) => {
    expect(() => grokWorkflowLaunch(frames(value), 'launch')).toThrow('The native Grok workflow launch has no exact run or script identity.')
  })
  it('rejects another call and repeated final launch records', () => {
    const native = frames({ type: 'Workflow', run_id: launch.runId, task_id: launch.runId, name: launch.name, script_path: launch.scriptPath })
    expect(() => grokWorkflowLaunch(native, 'other')).toThrow('one exact launch result')
    expect(() => grokWorkflowLaunch([...native, ...native], 'launch')).toThrow('one exact launch result')
  })
})

describe('grokWorkflowCompletion', () => {
  it.each(['answer42', '0', 'false', '', 'done'])('preserves the complete native summary %j', (text) => {
    expect(grokWorkflowCompletion(manifest('complete', text), launch)).toEqual({ runId: 'wf-1', status: 'completed', text })
  })
  it('preserves the exact failed history and error', () => {
    expect(grokWorkflowCompletion(manifest('failed', 'computed77'), launch)).toEqual({ runId: 'wf-1', status: 'failed', text: 'computed77' })
  })
  it('does not treat an active manifest as completion', () => {
    expect(grokWorkflowCompletion(manifest('active', 'earlier'), launch)).toBeNull()
  })
  it.each([
    [{ version: 3 }, 'The native Grok workflow manifest is invalid.'],
    [{ script_revision: -1 }, 'The native Grok workflow manifest is invalid.'],
    [{ state: { ...manifest('complete', 'answer').state, run_id: 'other' } }, 'The native Grok workflow manifest belongs to another run.'],
    [{ state: { ...manifest('complete', 'answer').state, name: 'other' } }, 'The native Grok workflow manifest belongs to another run.'],
    [{ state: { ...manifest('complete', 'answer').state, status: 'cancelled' } }, 'The native Grok workflow did not complete or fail.'],
    [{ state: { ...manifest('complete', 'answer').state, history: [] } }, 'The native Grok workflow requires one exact completion event.'],
    [{ state: { ...manifest('failed', 'answer').state, history: [{ event: 'workflow_failed', detail: 'other' }] } }, 'The native Grok workflow has no matching final output.'],
  ])('rejects another run or an invalid final record', (changed, error) => {
    expect(() => grokWorkflowCompletion({ ...manifest('complete', 'answer'), ...changed }, launch)).toThrow(error)
  })
})

describe('grokWorkflowManifestPath', () => {
  function fixture() {
    const home = createDirectory('grok-manifest-unit-')
    const scriptPath = join(home, 'sessions', 'cwd', 'session', 'workflows', 'wf-1', 'script.rhai')
    mkdirSync(dirname(scriptPath), { recursive: true })
    writeFileSync(scriptPath, 'actual script')
    return { home, nativeLaunch: { ...launch, scriptPath } }
  }
  it('selects the exact native run beside its unchanged script', () => {
    const { home, nativeLaunch } = fixture()
    expect(grokWorkflowManifestPath(nativeLaunch, home, 'session')).toBe(join(dirname(nativeLaunch.scriptPath), 'state.json'))
    expect(readFileSync(nativeLaunch.scriptPath, 'utf8')).toBe('actual script')
  })
  it('rejects another native session and run', () => {
    const { home, nativeLaunch } = fixture()
    expect(() => grokWorkflowManifestPath(nativeLaunch, home, 'other')).toThrow('another profile')
    expect(() => grokWorkflowManifestPath({ ...nativeLaunch, runId: 'other' }, home, 'session')).toThrow('another profile')
  })
  it('rejects a symbolic script inside the private profile', () => {
    const { home, nativeLaunch } = fixture()
    const original = join(home, 'original')
    writeFileSync(original, 'script')
    rmSync(nativeLaunch.scriptPath)
    symlinkSync(original, nativeLaunch.scriptPath)
    expect(() => grokWorkflowManifestPath(nativeLaunch, home, 'session')).toThrow('symbolic link')
  })
})

describe('readGrokWorkflowManifest', () => {
  /** A manifest path in a new private directory of the run. */
  function manifestPath(): string {
    return join(createDirectory('grok-full-output-unit-'), 'state.json')
  }

  /** The refusal of `readNativeToolOutputFile` for an entry that is not a regular file within the limit. */
  const REFUSED = 'The native full tool output requires a complete regular file within the read limit.'

  it('reads a complete native file', () => {
    const path = manifestPath()
    writeFileSync(path, JSON.stringify(manifest('complete', 'answer42')))
    expect(readGrokWorkflowManifest(path)).toEqual(manifest('complete', 'answer42'))
  })
  it('fails with a SyntaxError on a manifest that Grok has not finished writing', () => {
    const path = manifestPath()
    writeFileSync(path, '{')
    expect(() => readGrokWorkflowManifest(path)).toThrow(SyntaxError)
  })
  it('fails with ENOENT on a manifest that Grok has not written yet', () => {
    expect(() => readGrokWorkflowManifest(manifestPath())).toThrow(expect.objectContaining({ code: 'ENOENT' }))
  })
  it('reads a manifest of exactly the byte limit', () => {
    const path = manifestPath()
    // A JSON string adds its two quotes to the text.
    const text = 'x'.repeat(512 * 1024 - 2)
    writeFileSync(path, JSON.stringify(text))
    expect(readGrokWorkflowManifest(path)).toBe(text)
  })
  it('rejects a manifest one byte above the limit', () => {
    const path = manifestPath()
    writeFileSync(path, JSON.stringify('x'.repeat(512 * 1024 - 1)))
    expect(() => readGrokWorkflowManifest(path)).toThrow(REFUSED)
  })
  it('rejects a directory in place of the manifest', () => {
    const path = manifestPath()
    mkdirSync(path)
    expect(() => readGrokWorkflowManifest(path)).toThrow(REFUSED)
  })
  // The target is a valid manifest, so only the refusal of the link can fail the read. Windows has no O_NOFOLLOW, and
  // an open there follows the link, so the refusal must not come from the open.
  it('rejects a symbolic link to a valid manifest on every platform', () => {
    const path = manifestPath()
    writeFileSync(path, JSON.stringify(manifest('complete', 'answer42')))
    const link = join(dirname(path), 'link.json')
    symlinkSync(path, link)
    expect(() => readGrokWorkflowManifest(link)).toThrow(REFUSED)
  })
})

describe('grokWorkflowName', () => {
  it('uses the native lowercase metadata rule without changing the computed marker', () => {
    const marker = 'NATIVEGROK0123456789ABCDEF'
    expect(grokWorkflowName('output', marker)).toBe('native-code-output-nativegrok0123456789abcdef')
    expect(grokWorkflowName('error', marker)).toBe('native-code-error-nativegrok0123456789abcdef')
  })

  it('keeps separate native names for the output and error cases', () => {
    const marker = 'NATIVEGROK0123456789abcdef'
    const output = grokWorkflowName('output', marker)
    const error = grokWorkflowName('error', marker)
    expect(output).not.toBe(error)
    expect(output).toMatch(/^[a-z0-9]+(?:-[a-z0-9]+)*$/u)
    expect(error).toMatch(/^[a-z0-9]+(?:-[a-z0-9]+)*$/u)
    expect(output.length).toBeLessThanOrEqual(64)
    expect(error.length).toBeLessThanOrEqual(64)
  })
  it('accepts the exact native length limit and rejects one extra byte', () => {
    const prefix = 'native-code-output-'
    expect(grokWorkflowName('output', 'a'.repeat(64 - prefix.length)).length).toBe(64)
    expect(() => grokWorkflowName('output', 'a'.repeat(65 - prefix.length))).toThrow('64 bytes')
  })
  it.each(['', '-invalid', 'invalid-', 'invalid--marker', 'invalid_marker', '文'])('rejects invalid native metadata marker %j', (marker) => {
    expect(() => grokWorkflowName('output', marker)).toThrow('lowercase words')
  })
})

describe('grokWorkflowReportLabel', () => {
  it('uses the exact native name and objective instead of the group label alone', () => {
    expect(grokWorkflowReportLabel('native-code-output', 'Compute one native value.')).toBe('native-code-output: Compute one native value.')
  })

  it('keeps a name when the objective is empty and trims native label spacing', () => {
    expect(grokWorkflowReportLabel(' native-code-output ', '  ')).toBe('native-code-output')
  })

  it('rejects an absent workflow name', () => {
    expect(() => grokWorkflowReportLabel(' ', 'objective')).toThrow('name')
  })
})
