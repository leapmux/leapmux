import type { NativeToolOutputFilePathsOperations } from './nativeToolOutputFilePaths'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nativeOutputPathsPrecedePreview, runNativeToolOutputFilePathsProof } from './nativeToolOutputFilePaths'

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
  ])('rejects an invalid proof before browser or Worker operations %j', async (invalid) => {
    const f = fixture()
    await expect(runNativeToolOutputFilePathsProof(invalid, f.operations)).rejects.toThrow('native output path proof')
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
