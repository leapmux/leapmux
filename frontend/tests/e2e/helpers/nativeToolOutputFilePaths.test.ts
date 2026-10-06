import type { NativeMessageSnapshot } from './nativeMessages'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeToolOutputFilePathsOperations } from './nativeToolOutputFilePaths'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { assertPrivateNativePath } from './nativeCredentialIsolation'
import { checkNativeOutputReceipt, expectUnchangedNativeRecord, nativeOutputPathsPrecedePreview, presentPreviewMarkers, proveNativeOutputReceipt, runNativeToolOutputFilePathsProof } from './nativeToolOutputFilePaths'

/** The Worker state that the mocked agent and snapshot reads return. */
const worker = vi.hoisted(() => ({
  agent: { id: 'native-agent', agentSessionId: 'native-session' },
  snapshot: { agentId: 'native-agent', agentSessionId: 'native-session', messages: [] } as { agentId: string, agentSessionId: string, messages: never[] },
  snapshotReads: 0,
}))

/**
 * The result row of the receipt proof. Each locator states its selector chain, and the mocked `expect` records each
 * check of a fake locator as one event.
 */
const resultRow = vi.hoisted(() => {
  interface FakeLocator {
    fake: string
    locator: (selector: string) => FakeLocator
    filter: (options: { hasText: string }) => FakeLocator
    getByTestId: (testId: string) => FakeLocator
    textContent: () => Promise<string>
  }
  const state = {
    events: [] as string[],
    /** The text of the path block of the row. */
    pathText: '',
  }
  function fake(name: string): FakeLocator {
    return {
      fake: name,
      locator: selector => fake(`${name} ${selector}`),
      filter: ({ hasText }) => fake(`${name} with ${hasText}`),
      getByTestId: testId => fake(`${name} ${testId}`),
      textContent: async () => state.pathText,
    }
  }
  return Object.assign(state, { fake })
})

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown, message?: string) => {
    if (typeof value === 'object' && value !== null && 'fake' in value) {
      const name = String(value.fake)
      return {
        toHaveCount: async (count: number) => {
          resultRow.events.push(`${name} count:${count}`)
        },
        toHaveAttribute: async (attribute: string, expected: string) => {
          resultRow.events.push(`${name} ${attribute}:${expected}`)
        },
        not: {
          toHaveCount: async (count: number) => {
            resultRow.events.push(`${name} not count:${count}`)
          },
          toContainText: async (text: string) => {
            resultRow.events.push(`${name} omits:${text}`)
          },
        },
      }
    }
    return actual.expect(value, message)
  }
  return { ...actual, expect: Object.assign(check, actual.expect) }
})

vi.mock('./ui', () => ({
  toolCallRow: () => resultRow.fake('row'),
  openWorkspace: vi.fn(async () => {}),
  readAttachedWithArgument: vi.fn(async () => true),
}))

vi.mock('./nativeToolOutput', () => ({ copyNativeToolOutputPreview: vi.fn(async () => {}) }))

vi.mock('./nativeResultView', () => ({ expandNativeResultView: vi.fn(async () => {}) }))

vi.mock('./nativeCredentialIsolation', () => ({ assertPrivateNativePath: vi.fn() }))

vi.mock('./server', () => ({ getGlobalState: () => ({ tmpDir: '/run' }) }))

vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  currentNativeAgent: vi.fn(async () => ({ ...worker.agent })),
}))

vi.mock('./nativeMessages', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeMessages')>(),
  readNativeMessageSnapshot: vi.fn(async () => {
    worker.snapshotReads++
    return { ...worker.snapshot }
  }),
}))

const options = {
  callId: 'native-call',
  previewText: 'native first line\n[native omission notice]',
  previewMarkers: ['native first line', '[native omission notice]'],
  paths: ['/native/stdout.log', '/native/stderr.log'],
  status: 'completed',
}

describe('nativeOutputPathsPrecedePreview', () => {
  const paths = '<div data-testid="tool-output-file-paths">Output file: /native/result.log</div>'

  function element(markup: string): HTMLElement {
    const result = document.createElement('div')
    result.innerHTML = markup
    document.body.append(result)
    return result
  }

  afterEach(() => document.body.replaceChildren())

  function order(markup: string, markers: readonly string[]): boolean | null {
    return nativeOutputPathsPrecedePreview([element(markup)], markers)
  }

  it('requires paths before every original preview marker', () => {
    expect(order(`${paths}<pre data-tool-output-preview>native first\nnative last</pre>`, ['native first', 'native last'])).toBe(true)
    expect(order(`<pre data-tool-output-preview>native first\nnative last</pre>${paths}`, ['native first', 'native last'])).toBe(false)
  })

  it('uses the preview after earlier command text', () => {
    expect(order(`<code>printf native first</code>${paths}<pre data-tool-output-preview>native first</pre>`, ['native first'])).toBe(true)
    expect(order(`<code>printf native first</code>${paths}`, ['native first'])).toBe(false)
  })

  function piCodemodeArgumentResult(outputBeforePaths: boolean): HTMLElement {
    const output = `${Array.from({ length: 3000 }, (_, index) => `full-output-line-${index}`).join('\n')}\nPI_OUTPUT_FILE_RECOVERED`
    const code = `// @options: {"max_output_tokens": 100}\nconst result = await tools.mcp__result_probe__inspect({count: 0, enabled: false, text: ${JSON.stringify(output)}}); text(result.structuredContent.text);`
    const result = element(paths)
    if (outputBeforePaths) {
      const actualPreview = document.createElement('pre')
      actualPreview.setAttribute('data-tool-output-preview', '')
      actualPreview.textContent = output
      result.prepend(actualPreview)
    }
    const argumentsBlock = document.createElement('div')
    argumentsBlock.append('Arguments')
    const argumentsText = document.createElement('pre')
    argumentsText.textContent = JSON.stringify({ code })
    argumentsBlock.append(argumentsText)
    result.append(argumentsBlock)
    return result
  }

  it('does not certify Pi codemode arguments as native output', () => {
    const result = piCodemodeArgumentResult(false)
    expect(nativeOutputPathsPrecedePreview([result], ['full-output-line-0', 'PI_OUTPUT_FILE_RECOVERED'])).toBe(false)
  })

  it('refuses output before paths when later Pi arguments repeat every output marker', () => {
    const result = piCodemodeArgumentResult(true)
    expect(nativeOutputPathsPrecedePreview([result], ['full-output-line-0', 'PI_OUTPUT_FILE_RECOVERED'])).toBe(false)
  })

  it('reads preview text across styled spans without changing it', () => {
    const result = element(`${paths}<div data-tool-output-preview><span>native </span><span>first</span></div>`)
    const before = result.innerHTML
    expect(nativeOutputPathsPrecedePreview([result], ['native first'])).toBe(true)
    expect(result.innerHTML).toBe(before)
  })

  it('keeps collapsed preview text after the path block', () => {
    expect(order(`${paths}<pre data-tool-output-preview class="collapsed">native first</pre>`, ['native first'])).toBe(true)
  })

  it('does not accept a preview marker from the path block itself', () => {
    const result = element('<div data-testid="tool-output-file-paths">Output file: /native/native first.log</div>')
    expect(nativeOutputPathsPrecedePreview([result], ['native first'])).toBe(false)
  })

  it.each(['', ' ', 'missing preview'])('refuses an empty or missing marker: %j', (marker) => {
    expect(order(`${paths}<pre data-tool-output-preview>native first</pre>`, [marker])).toBe(false)
  })

  it('refuses absent or duplicate path blocks and an empty marker list', () => {
    expect(order('<pre data-tool-output-preview>native first</pre>', ['native first'])).toBe(false)
    expect(order(`${paths}${paths}<pre data-tool-output-preview>native first</pre>`, ['native first'])).toBe(false)
    expect(order(`${paths}<pre data-tool-output-preview>native first</pre>`, [])).toBe(false)
  })

  it('requires every actual output block after paths even when only the later block contains a marker', () => {
    expect(order(`<div data-tool-output-preview>earlier returned output</div>${paths}<pre data-tool-output-preview>native first</pre>`, ['native first'])).toBe(false)
  })

  it('refuses output ownership that wraps the path block or sits inside it', () => {
    expect(order(`<div data-tool-output-preview>earlier output${paths}</div><pre data-tool-output-preview>native first</pre>`, ['native first'])).toBe(false)
    expect(order(`<div data-testid="tool-output-file-paths">Output file: /native/result.log<span data-tool-output-preview>x</span></div><pre data-tool-output-preview>native first</pre>`, ['native first'])).toBe(false)
  })

  it('does not read a marker from unmarked text beside marked output', () => {
    expect(order(`${paths}<pre data-tool-output-preview>native first</pre><div>Agent ID: native last</div>`, ['native first', 'native last'])).toBe(false)
    expect(order(`${paths}<pre data-tool-output-preview>native first</pre><div>Agent ID: native last</div>`, ['native first'])).toBe(true)
  })

  it('ignores an empty marked output element before the path block', () => {
    expect(order(`<pre data-tool-output-preview></pre>${paths}<pre data-tool-output-preview>native first</pre>`, ['native first'])).toBe(true)
  })

  it('owns genuine structured output without accepting unmarked metadata', () => {
    expect(order(`${paths}<div data-tool-output-preview>{"count":0,"enabled":false}</div>`, ['"count":0'])).toBe(true)
    expect(order(`${paths}<div>Structured</div><pre>{"count":0,"enabled":false}</pre>`, ['"count":0'])).toBe(false)
  })

  it('requests another attached read when every match was replaced', () => {
    const detached = document.createElement('div')
    detached.innerHTML = `${paths}<pre data-tool-output-preview>native first</pre>`
    expect(nativeOutputPathsPrecedePreview([detached], ['native first'])).toBeNull()
    expect(nativeOutputPathsPrecedePreview([], ['native first'])).toBeNull()
  })

  it('ignores a replaced match and reads the attached replacement', () => {
    const detached = document.createElement('div')
    detached.innerHTML = `<pre data-tool-output-preview>native first</pre>${paths}`
    const attached = element(`${paths}<pre data-tool-output-preview>native first</pre>`)
    expect(nativeOutputPathsPrecedePreview([detached, attached], ['native first'])).toBe(true)
  })
})

function fixture() {
  const events: string[] = []
  const operations: NativeToolOutputFilePathsOperations = {
    workerProof: async (reloaded) => { events.push(`worker:${reloaded}`) },
    viewProof: async () => { events.push('view') },
    copyProof: async (text) => { events.push(`copy:${text}`) },
    reload: async () => { events.push('reload') },
  }
  return { events, operations }
}

describe('runNativeToolOutputFilePathsProof', () => {
  it('checks the original preview and native owner before and after reload', async () => {
    const f = fixture()
    await runNativeToolOutputFilePathsProof(options, f.operations)
    expect(f.events).toEqual([
      'worker:false',
      'view',
      `copy:${options.previewText}`,
      'reload',
      'worker:true',
      'view',
      `copy:${options.previewText}`,
    ])
  })

  it.each(['failed', 'incomplete'])('preserves native %s status with a reported path', async (status) => {
    const f = fixture()
    await runNativeToolOutputFilePathsProof({ ...options, status }, f.operations)
    expect(f.events).toHaveLength(7)
    expect(f.events.at(-1)).toBe(`copy:${options.previewText}`)
  })

  it('accepts the absence of a native output file pointer', async () => {
    const f = fixture()
    await runNativeToolOutputFilePathsProof({ ...options, paths: [] }, f.operations)
    expect(f.events).toContain('worker:true')
  })

  it('preserves path spelling and the original preview independently', async () => {
    const f = fixture()
    const copyProof = vi.fn(f.operations.copyProof)
    const paths = Object.freeze(['C:\\native output\\one.log', '\\\\host\\share\\two.log'])
    await runNativeToolOutputFilePathsProof({ ...options, paths }, { ...f.operations, copyProof })
    expect(copyProof).toHaveBeenNthCalledWith(1, options.previewText)
    expect(copyProof).toHaveBeenNthCalledWith(2, options.previewText)
    expect(paths).toEqual(['C:\\native output\\one.log', '\\\\host\\share\\two.log'])
  })

  it.each([
    { ...options, callId: '' },
    { ...options, callId: ' ' },
    { ...options, previewText: '' },
    { ...options, status: '' },
    { ...options, previewMarkers: [] },
    { ...options, previewMarkers: ['absent marker'] },
    { ...options, previewMarkers: ['', options.previewText] },
    { ...options, previewMarkers: [' '] },
    { ...options, previewMarkers: ['native first line', 'native first line'] },
    { ...options, paths: [''] },
    { ...options, paths: [' '] },
    { ...options, paths: ['/native/one.log\0'] },
    { ...options, paths: ['/native/one.log', '/native/one.log'] },
    { ...options, absentMarkers: [''] },
    { ...options, absentMarkers: [' '] },
    { ...options, absentMarkers: ['native first line'] },
    { ...options, absentMarkers: ['omission notice'] },
    { ...options, absentMarkers: ['native middle line', 'native middle line'] },
  ])('rejects an invalid proof before browser or Worker operations %j', async (invalid) => {
    const f = fixture()
    await expect(runNativeToolOutputFilePathsProof(invalid, f.operations)).rejects.toThrow('native output path proof')
    expect(f.events).toEqual([])
  })

  it('accepts absent markers that the original preview does not hold', async () => {
    const f = fixture()
    await runNativeToolOutputFilePathsProof({ ...options, absentMarkers: ['native middle line', 'native last line'] }, f.operations)
    expect(f.events).toHaveLength(7)
  })

  it('refuses an absent marker that equals a preview marker, with the absent marker message', async () => {
    const f = fixture()
    await expect(runNativeToolOutputFilePathsProof({ ...options, absentMarkers: [options.previewMarkers[1]!] }, f.operations))
      .rejects
      .toThrow('absent markers that the original preview does not hold')
    expect(f.events).toEqual([])
  })

  it.each(['workerProof', 'viewProof', 'copyProof', 'reload'] as const)('propagates a %s failure and stops later operations', async (step) => {
    const f = fixture()
    const failure = new Error(`The ${step} operation failed.`)
    const failing = vi.fn(async () => {
      throw failure
    })
    await expect(runNativeToolOutputFilePathsProof(options, { ...f.operations, [step]: failing })).rejects.toBe(failure)
    expect(failing).toHaveBeenCalledTimes(1)
    expect(f.events).not.toContain('worker:true')
  })
})

const output = { firstMarker: 'OUT-line-0:', omittedMarker: 'OUT-line-1:-middle-77', lastMarker: 'OUT-complete-42' }

describe('presentPreviewMarkers', () => {
  it.each([
    ['no marker', 'native head', []],
    ['the first marker', 'OUT-line-0:\n[truncated]', ['OUT-line-0:']],
    ['the last marker', '[truncated]\nOUT-complete-42', ['OUT-complete-42']],
    ['both markers', 'OUT-line-0:\n[truncated]\nOUT-complete-42', ['OUT-line-0:', 'OUT-complete-42']],
  ])('returns %s that the preview holds, first before last', (_case, previewText, expected) => {
    expect(presentPreviewMarkers(previewText, output)).toEqual(expected)
  })
})

describe('checkNativeOutputReceipt', () => {
  const receipt = { paths: ['/native/output.txt'], previewText: 'OUT-line-0:\n[truncated]\nOUT-complete-42' }

  it('returns the sole declared path, the computed markers that the preview holds, and the omitted line as absent', () => {
    expect(checkNativeOutputReceipt(receipt, output)).toEqual({
      path: '/native/output.txt',
      previewMarkers: ['OUT-line-0:', 'OUT-complete-42'],
      absentMarker: 'OUT-line-1:-middle-77',
      rowAbsentMarkers: ['OUT-line-1:-middle-77'],
    })
  })

  it.each([undefined, false])('requires the row to omit the absent line when the arguments do not hold the output: %j', (argumentsHoldOutput) => {
    const markers = argumentsHoldOutput === undefined ? {} : { argumentsHoldOutput }
    expect(checkNativeOutputReceipt(receipt, output, markers).rowAbsentMarkers).toEqual(['OUT-line-1:-middle-77'])
  })

  it('requires no absent line in the row when the arguments hold the output, but still requires it in the preview', () => {
    const checked = checkNativeOutputReceipt(receipt, output, { argumentsHoldOutput: true })
    expect(checked.rowAbsentMarkers).toEqual([])
    expect(checked.absentMarker).toBe('OUT-line-1:-middle-77')
    expect(() => checkNativeOutputReceipt({ ...receipt, previewText: `${receipt.previewText}\nOUT-line-1:-middle-77` }, output, { argumentsHoldOutput: true }))
      .toThrow('omitted middle line')
  })

  it('returns the markers that the caller states, unchanged', () => {
    expect(checkNativeOutputReceipt(receipt, output, { previewMarkers: ['[truncated]'] }).previewMarkers).toEqual(['[truncated]'])
  })

  it('selects the markers through a function of the receipt', () => {
    const select = vi.fn((current: typeof receipt) => current.previewText.split('\n').filter(line => line.startsWith('OUT-line-')))
    expect(checkNativeOutputReceipt(receipt, output, { previewMarkers: select }).previewMarkers).toEqual(['OUT-line-0:'])
    expect(select).toHaveBeenCalledWith(receipt)
  })

  // A tail window of the output, as OpenCode keeps one, can hold the middle line and never holds the first line.
  const tailWindow = { paths: ['/native/output.txt'], previewText: '...output truncated...\nOUT-line-1:-middle-77\nOUT-complete-42' }

  it('accepts a preview that holds the omitted line when the caller states another absent line', () => {
    expect(checkNativeOutputReceipt(tailWindow, output, { previewMarkers: ['OUT-line-'], absentMarker: 'OUT-line-0:' }))
      .toEqual({ path: '/native/output.txt', previewMarkers: ['OUT-line-'], absentMarker: 'OUT-line-0:', rowAbsentMarkers: ['OUT-line-0:'] })
  })

  it('refuses a preview that holds the absent line that the caller states', () => {
    expect(() => checkNativeOutputReceipt(receipt, output, { absentMarker: 'OUT-line-0:' })).toThrow('holds the absent line')
  })

  it.each(['', ' '])('refuses an empty absent line: %j', (absentMarker) => {
    expect(() => checkNativeOutputReceipt(receipt, output, { absentMarker })).toThrow('nonempty absent line')
  })

  it.each([[[]], [['/native/one.txt', '/native/two.txt']]])('refuses a receipt that does not declare exactly one path: %j', (paths) => {
    expect(() => checkNativeOutputReceipt({ ...receipt, paths }, output)).toThrow(`exactly one declared path, not ${paths.length}`)
  })

  it('refuses a preview that holds the omitted middle line', () => {
    expect(() => checkNativeOutputReceipt({ ...receipt, previewText: `${receipt.previewText}\nOUT-line-1:-middle-77` }, output)).toThrow('omitted middle line')
  })

  it('refuses a preview that holds neither the first line nor the last line', () => {
    expect(() => checkNativeOutputReceipt({ ...receipt, previewText: '[truncated]' }, output)).toThrow('neither the first line nor the last line')
  })

  it('refuses an empty list of stated markers', () => {
    expect(() => checkNativeOutputReceipt(receipt, output, { previewMarkers: [] })).toThrow('at least one preview marker')
    expect(() => checkNativeOutputReceipt(receipt, output, { previewMarkers: () => [] })).toThrow('at least one preview marker')
  })
})

describe('expectUnchangedNativeRecord', () => {
  const context = {} as Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>
  const agent = { id: 'native-agent', agentSessionId: 'native-session' }
  const record = { frame: { status: 'completed' }, paths: ['/native/output.txt'] }

  beforeEach(() => {
    worker.agent = { ...agent }
    worker.snapshot = { agentId: agent.id, agentSessionId: agent.agentSessionId, messages: [] }
    worker.snapshotReads = 0
  })

  it('reads the record from a fresh Worker snapshot on each call and accepts an unchanged record', async () => {
    const read = vi.fn((snapshot: NativeMessageSnapshot) => ({ ...record, agentId: snapshot.agentId }))
    const expected = { ...record, agentId: agent.id }
    await expectUnchangedNativeRecord(context, agent, read, expected)
    await expectUnchangedNativeRecord(context, agent, read, expected)
    expect(read).toHaveBeenCalledTimes(2)
    expect(worker.snapshotReads).toBe(2)
  })

  it.each([
    ['a changed frame', { ...record, frame: { status: 'failed' } }],
    ['a changed path', { ...record, paths: ['/native/other.txt'] }],
  ])('refuses %s', async (_case, current) => {
    await expect(expectUnchangedNativeRecord(context, agent, () => current, record)).rejects.toThrow('the Worker record of the native result stays the same')
  })

  it('refuses another selected agent before it reads the record', async () => {
    worker.agent = { ...agent, id: 'other-agent' }
    const read = vi.fn(() => record)
    await expect(expectUnchangedNativeRecord(context, agent, read, record)).rejects.toThrow('the selected agent is the agent that ran the tool')
    expect(read).not.toHaveBeenCalled()
  })

  it('refuses a changed native session of the agent', async () => {
    worker.agent = { ...agent, agentSessionId: 'other-session' }
    await expect(expectUnchangedNativeRecord(context, agent, () => record, record)).rejects.toThrow('the agent keeps its native session')
  })

  it('refuses a Worker snapshot of another native session', async () => {
    worker.snapshot = { ...worker.snapshot, agentSessionId: 'other-session' }
    await expect(expectUnchangedNativeRecord(context, agent, () => record, record)).rejects.toThrow('the Worker snapshot belongs to the native session')
  })
})

describe('proveNativeOutputReceipt', () => {
  const computed = { ...output, source: 'process.stdout.write(computed)', text: 'OUT-line-0:\nOUT-line-1:-middle-77\nOUT-complete-42' }
  const receipt = { paths: ['/run/native/output.txt'], previewText: 'OUT-line-0:\n[truncated]\nOUT-complete-42', frame: { status: 'completed' }, content: new Uint8Array([1, 2, 3]) }
  const capture = {
    context: { page: { reload: async () => null }, workspaceId: 'native-workspace' },
    agent: { id: 'native-agent', agentSessionId: 'native-session' },
    snapshot: { agentId: 'native-agent', agentSessionId: 'native-session', messages: [] },
    nativeCallId: 'native-call',
    output: computed,
  } as unknown as Parameters<typeof proveNativeOutputReceipt>[0]
  const testInfo = { attach: vi.fn(async () => {}) }
  /** A reader that returns a fresh copy of the receipt for each snapshot, as a provider reader does. */
  const read = vi.fn(() => ({ ...receipt, paths: [...receipt.paths], content: receipt.content.slice() }))

  /** The lines that the view proof required out of the result row, one event for each pass. */
  const omittedFromRow = () => resultRow.events.filter(event => event.startsWith('row omits:'))

  beforeEach(() => {
    worker.agent = { id: 'native-agent', agentSessionId: 'native-session' }
    worker.snapshot = { agentId: 'native-agent', agentSessionId: 'native-session', messages: [] }
    resultRow.events.length = 0
    resultRow.pathText = 'Output file:/run/native/output.txt'
    testInfo.attach.mockClear()
    read.mockClear()
    vi.mocked(assertPrivateNativePath).mockClear()
  })

  it('requires the omitted line out of the result row on both passes, and the path in the run directory', async () => {
    await proveNativeOutputReceipt(capture, testInfo, read)
    expect(omittedFromRow()).toEqual(['row omits:OUT-line-1:-middle-77', 'row omits:OUT-line-1:-middle-77'])
    expect(resultRow.events).toContain('row data-tool-status:completed')
    expect(assertPrivateNativePath).toHaveBeenCalledWith('/run/native/output.txt', '/run')
    // One read of the captured snapshot, then one read of a fresh snapshot on each pass.
    expect(read).toHaveBeenCalledTimes(3)
  })

  it('requires no line out of the result row when the arguments of the call hold the output', async () => {
    await proveNativeOutputReceipt(capture, testInfo, read, { argumentsHoldOutput: true })
    expect(omittedFromRow()).toEqual([])
    expect(resultRow.events).toContain('row data-tool-status:completed')
  })

  it('checks the path against the private root that the caller states', async () => {
    await proveNativeOutputReceipt(capture, testInfo, read, { privateRoot: '/home/native' })
    expect(assertPrivateNativePath).toHaveBeenCalledWith('/run/native/output.txt', '/home/native')
  })

  it('attaches the receipt before a failed receipt check, and checks no row', async () => {
    const leaked = vi.fn(() => ({ ...receipt, previewText: `${receipt.previewText}\nOUT-line-1:-middle-77` }))
    await expect(proveNativeOutputReceipt(capture, testInfo, leaked)).rejects.toThrow('omitted middle line')
    expect(testInfo.attach).toHaveBeenCalledTimes(1)
    expect(resultRow.events).toEqual([])
    expect(assertPrivateNativePath).not.toHaveBeenCalled()
  })

  it('fails when the Worker record changes after the reload', async () => {
    let reads = 0
    const changing = vi.fn(() => ++reads < 3 ? { ...receipt } : { ...receipt, frame: { status: 'failed' } })
    await expect(proveNativeOutputReceipt(capture, testInfo, changing)).rejects.toThrow('the Worker record of the native result stays the same')
  })
})
