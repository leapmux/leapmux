import type { Page } from '@playwright/test'
import type { MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeScenarioContext } from './nativeScenario'
import { Buffer } from 'node:buffer'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectPngInRequest, PNG_BASE64_PREFIX, runToolImageTurn, writeToolImage } from './toolImages'

/** The browser operations of the mocked UI helpers, in call order. */
const browser = vi.hoisted(() => ({ events: [] as string[] }))

/** The turn that the mocked `runNativeToolTurn` received. */
const toolTurn = vi.hoisted(() => ({ received: [] as unknown[] }))

vi.mock('./nativeToolExecution', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeToolExecution')>(),
  runNativeToolTurn: vi.fn(async (_context: unknown, turn: unknown) => {
    toolTurn.received.push(turn)
    return { start: 6, toolRequest: { stepIndex: 6 }, resultRequest: { stepIndex: 7, protocol: 'openai-chat-completions', body: {} } }
  }),
}))

vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  sendMessage: vi.fn(async (_page: unknown, text: string) => {
    browser.events.push(`send:${text}`)
  }),
  waitForControlBanner: vi.fn(async () => {
    browser.events.push('banner')
    return { fake: 'banner' }
  }),
  answerControl: vi.fn(async (_page: unknown, decision: string) => {
    browser.events.push(`answer:${decision}`)
  }),
  waitForAgentIdle: vi.fn(async () => {
    browser.events.push('idle')
  }),
}))

// A fake banner records the text check. Every other value keeps the real assertion.
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown, message?: string) => {
    if (typeof value === 'object' && value !== null && 'fake' in value) {
      return {
        toContainText: async (text: string) => {
          browser.events.push(`${String(value.fake)} holds:${text}`)
        },
      }
    }
    return actual.expect(value, message)
  }
  return { ...actual, expect: Object.assign(check, actual.expect) }
})

let workingDir: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  workingDir = mkdtempSync(join(scratch, 'tool-images-'))
  browser.events.length = 0
  toolTurn.received.length = 0
})

afterEach(() => rmSync(workingDir, { recursive: true, force: true }))

describe('writeToolImage', () => {
  it('writes a PNG under the working directory, named from the marker', () => {
    const name = writeToolImage(workingDir, 'prov-42')
    expect(name).toBe('tool-image-prov-42.png')
    expect(existsSync(join(workingDir, name))).toBe(true)
  })

  it('writes a decodable PNG with the standard signature', () => {
    const bytes = readFileSync(join(workingDir, writeToolImage(workingDir, 'sig')))
    expect([...bytes.subarray(0, 8)]).toEqual([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A])
    expect(bytes.length).toBeGreaterThan(8)
  })

  // The marker is what a tool row shows. A name that carries it can only come
  // from a real read of this file, not from the prompt or the scripted reply.
  it('keeps the marker in the file name so a tool row can name it', () => {
    expect(writeToolImage(workingDir, 'kimi-77')).toBe('tool-image-kimi-77.png')
    expect(writeToolImage(workingDir, 'codex-77')).toBe('tool-image-codex-77.png')
  })
})

describe('PNG_BASE64_PREFIX', () => {
  it('starts the base64 of the written PNG and of any PNG with the same signature and IHDR length', () => {
    const bytes = readFileSync(join(workingDir, writeToolImage(workingDir, 'prefix')))
    expect(bytes.toString('base64').startsWith(PNG_BASE64_PREFIX)).toBe(true)
    const otherPng = Buffer.concat([bytes.subarray(0, 12), Buffer.from('IHDR'), Buffer.alloc(13, 0xFF)])
    expect(otherPng.toString('base64').startsWith(PNG_BASE64_PREFIX)).toBe(true)
  })

  it('does not start the base64 of a JPEG', () => {
    expect(Buffer.from([0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 0x4A, 0x46, 0x49, 0x46]).toString('base64').startsWith(PNG_BASE64_PREFIX)).toBe(false)
  })
})

describe('expectPngInRequest', () => {
  /** The start of a PNG in base64: the signature, then the IHDR length and type. */
  const pngBase64 = `${PNG_BASE64_PREFIX}AAAANSUhEUg`

  it('accepts the PNG base64 anywhere in the request body', () => {
    expect(() => expectPngInRequest({ protocol: 'openai-chat-completions', body: { messages: [{ role: 'tool', content: pngBase64 }] } })).not.toThrow()
  })

  it('accepts the data URI form when the request states it', () => {
    const body = { messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: `data:image/png;base64,${pngBase64}` } }] }] }
    expect(() => expectPngInRequest({ protocol: 'openai-chat-completions', body }, 'data-uri')).not.toThrow()
  })

  it('refuses a request without PNG bytes, and keeps the body out of the message', () => {
    const body = { messages: [{ role: 'tool', content: 'The image was read.' }] }
    expect(() => expectPngInRequest({ protocol: 'anthropic-messages', body })).toThrow('the anthropic-messages model request carries the tool image')
    expect(() => expectPngInRequest({ protocol: 'anthropic-messages', body })).not.toThrow('The image was read.')
  })

  it('refuses bare base64 when the data URI form is required', () => {
    expect(() => expectPngInRequest({ protocol: 'openai-chat-completions', body: { content: pngBase64 } }, 'data-uri')).toThrow('data:image/png;base64,')
  })
})

describe('runToolImageTurn', () => {
  /** A context whose model script records each step and serves recorded requests by step index. */
  function scriptedContext(options: { start: number, textStep?: NativeScenarioContext['textStep'] }) {
    const queued: MockModelStep[][] = []
    const modelScript = {
      prompt: (text: string) => `marked:${text}`,
      queue: vi.fn(async (...steps: MockModelStep[]) => {
        queued.push(steps)
        browser.events.push('queue')
        return options.start
      }),
      waitForSteps: vi.fn(async (count: number) => {
        browser.events.push(`steps:${count}`)
      }),
      requestAt: vi.fn(async (stepIndex: number) => ({ stepIndex, protocol: 'openai-chat-completions', body: {} })),
    } as unknown as ModelScript
    const context: NativeScenarioContext = { page: {} as Page, modelScript, provider: AgentProvider.KILO, ...(options.textStep ? { textStep: options.textStep } : {}) }
    return { context, queued }
  }

  it('writes the PNG and runs the read as a native tool turn that allows each approval', async () => {
    const { context, queued } = scriptedContext({ start: 3 })
    const result = await runToolImageTurn(context, {
      workingDir,
      marker: 'kilo',
      toolCall: image => ({ id: 'read-image', name: 'read', arguments: { path: image.path } }),
    })
    expect(result.fileName).toBe('tool-image-kilo.png')
    expect(result.path).toBe(join(workingDir, 'tool-image-kilo.png'))
    expect(readdirSync(workingDir)).toEqual(['tool-image-kilo.png'])
    expect(toolTurn.received).toEqual([{
      toolCalls: [{ id: 'read-image', name: 'read', arguments: { path: result.path } }],
      prompt: 'Read tool-image-kilo.png and describe it.',
      answer: 'I inspected tool-image-kilo.png.',
    }])
    expect(result.resultRequest.stepIndex).toBe(7)
    expect(queued).toEqual([])
    expect(browser.events).toEqual([])
  })

  it('requires the banner that shows the file name of the PNG and allows it before the answer step', async () => {
    const { context, queued } = scriptedContext({ start: 3 })
    const result = await runToolImageTurn(context, { workingDir, marker: 'copilot', approve: true, toolCall: image => ({ id: 'view', name: 'view', arguments: { path: image.path } }) })
    expect(queued).toEqual([[{ toolCalls: [{ id: 'view', name: 'view', arguments: { path: result.path } }] }, { text: 'I inspected tool-image-copilot.png.' }]])
    expect(browser.events).toEqual([
      'queue',
      'send:marked:Read tool-image-copilot.png and describe it.',
      'steps:4',
      'banner',
      'banner holds:tool-image-copilot.png',
      'answer:allow',
      'steps:5',
      'idle',
    ])
    expect(result.resultRequest.stepIndex).toBe(4)
    expect(toolTurn.received).toEqual([])
  })

  it('answers through the text step of the provider in an approved turn', async () => {
    const { context, queued } = scriptedContext({ start: 0, textStep: text => ({ toolCalls: [{ id: 'answer', name: 'respond', arguments: { text } }] }) })
    await runToolImageTurn(context, { workingDir, marker: 'dirac', approve: true, toolCall: image => ({ id: 'read', name: 'read', arguments: { path: image.path } }) })
    expect(queued[0]?.[1]).toEqual({ toolCalls: [{ id: 'answer', name: 'respond', arguments: { text: 'I inspected tool-image-dirac.png.' } }] })
  })

  it('refuses a turn with no working directory before it writes or runs anything', async () => {
    const { context, queued } = scriptedContext({ start: 0 })
    await expect(runToolImageTurn(context, { workingDir: '', marker: 'none', toolCall: image => ({ id: 'read', name: 'read', arguments: { path: image.path } }) }))
      .rejects
      .toThrow('needs the working directory')
    expect(queued).toEqual([])
    expect(toolTurn.received).toEqual([])
    expect(browser.events).toEqual([])
  })
})
