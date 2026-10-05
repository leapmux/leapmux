import type { BrowserContext, Page, Locator as PlaywrightLocator } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { Buffer } from 'node:buffer'
import { dirname, resolve } from 'node:path'
import { create } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import { ampControls } from '../../../src/components/chat/providers/amp/pluginControls'
import { permissionPresetAvailable } from '../../../src/components/chat/providerSettings'
import { AMP_PERMISSION_MODE } from '../../../src/generated/contracts/amp-protocol'
import { MIMO_OPTION, MIMO_PERMISSION_POLICY } from '../../../src/generated/contracts/mimo-protocol'
import { AgentInfoSchema, AgentProvider, AgentStatus, AvailableOptionGroupSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { startTestDeadline, WAIT_REPORT_MARGIN_MS } from './testDeadline'
import {
  applyPermissionPreset,
  ARITHMETIC_ANSWER,
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  chooseSettingsOption,
  enterMessageText,
  isMaybeVisible,
  screenshotIfEnabled,
  SECOND_ARITHMETIC_ANSWER,
  SECOND_ARITHMETIC_ANSWER_TEXT,
  SECOND_ARITHMETIC_PROMPT,
  waitForAgentIdle,
  waitForLayoutSave,
  waitForNativeSettingsHydrated,
} from './ui'

const native = vi.hoisted(() => ({ agent: vi.fn<typeof import('./nativeScenario').nativeAgentById>() }))
const screenshots = vi.hoisted(() => ({
  outputPath: vi.fn<(...parts: string[]) => string>(),
  mkdir: vi.fn<typeof import('node:fs').mkdirSync>(),
}))
vi.mock('node:fs', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:fs')>()
  return { ...actual, mkdirSync: screenshots.mkdir }
})
vi.mock('./nativeScenario', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./nativeScenario')>()
  return { ...actual, nativeAgentById: native.agent }
})
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const firstAttempt = new Proxy(actual.expect, {
    apply: (target, receiver, argumentsList) => {
      const [value] = argumentsList
      const matchers = Reflect.apply(target, receiver, argumentsList)
      if (typeof value !== 'function')
        return matchers
      return new Proxy(matchers, {
        get: (result, property) => property === 'toPass' ? async () => value() : Reflect.get(result, property),
      })
    },
  })
  const testWithCurrentOutput = new Proxy(actual.test, {
    get: (target, property, receiver) => property === 'info'
      ? () => ({ outputPath: screenshots.outputPath })
      : Reflect.get(target, property, receiver),
  })
  return { ...actual, expect: firstAttempt, test: testWithCurrentOutput }
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  screenshots.mkdir.mockReset()
  screenshots.outputPath.mockReset()
})

describe('screenshotIfEnabled', () => {
  it('writes each screenshot through the current test output directory', async () => {
    vi.stubEnv('E2E_SCREENSHOTS', '1')
    vi.stubEnv('E2E_THEME', 'dark')
    const outputFile = resolve('.tmp', 'ui-screenshots', 'shard-2', 'test-3', 'screenshots', 'dark', 'chat.png')
    screenshots.outputPath.mockReset().mockReturnValue(outputFile)
    const mkdir = screenshots.mkdir.mockReset().mockImplementation(() => undefined)
    const screenshot = vi.fn<Page['screenshot']>().mockResolvedValue(Buffer.alloc(0))

    await screenshotIfEnabled(opaqueHandle<Page>({ screenshot }), 'chat')

    expect(screenshots.outputPath).toHaveBeenCalledWith('screenshots', 'dark', 'chat.png')
    expect(mkdir).toHaveBeenCalledWith(dirname(outputFile), { recursive: true })
    expect(screenshot).toHaveBeenCalledWith({ path: outputFile, fullPage: false })
  })

  it.each([undefined, ''])('uses the system theme when no theme is selected: %j', async (theme) => {
    vi.stubEnv('E2E_SCREENSHOTS', '1')
    vi.stubEnv('E2E_THEME', theme)
    const outputFile = resolve('.tmp', 'ui-screenshots', 'shard-1', 'test-1', 'screenshots', 'system', 'chat.png')
    screenshots.outputPath.mockReset().mockReturnValue(outputFile)
    screenshots.mkdir.mockReset().mockImplementation(() => undefined)
    const screenshot = vi.fn<Page['screenshot']>().mockResolvedValue(Buffer.alloc(0))

    await screenshotIfEnabled(opaqueHandle<Page>({ screenshot }), 'chat')

    expect(screenshots.outputPath).toHaveBeenCalledWith('screenshots', 'system', 'chat.png')
    expect(screenshot).toHaveBeenCalledWith({ path: outputFile, fullPage: false })
  })

  it('does not access test output or the page when screenshots are disabled', async () => {
    vi.stubEnv('E2E_SCREENSHOTS', undefined)
    screenshots.outputPath.mockReset()
    const mkdir = screenshots.mkdir.mockReset().mockImplementation(() => undefined)
    const screenshot = vi.fn<Page['screenshot']>().mockResolvedValue(Buffer.alloc(0))

    await screenshotIfEnabled(opaqueHandle<Page>({ screenshot }), 'chat')

    expect(screenshots.outputPath).not.toHaveBeenCalled()
    expect(mkdir).not.toHaveBeenCalled()
    expect(screenshot).not.toHaveBeenCalled()
  })

  it('returns the screenshot error without losing its test output destination', async () => {
    vi.stubEnv('E2E_SCREENSHOTS', '1')
    vi.stubEnv('E2E_THEME', 'system')
    const outputFile = resolve('.tmp', 'ui-screenshots', 'shard-1', 'test-1', 'screenshots', 'system', 'chat.png')
    screenshots.outputPath.mockReset().mockReturnValue(outputFile)
    screenshots.mkdir.mockReset().mockImplementation(() => undefined)
    const failure = new Error('The page closed before the screenshot.')
    const screenshot = vi.fn<Page['screenshot']>().mockRejectedValue(failure)

    await expect(screenshotIfEnabled(opaqueHandle<Page>({ screenshot }), 'chat')).rejects.toBe(failure)

    expect(screenshot).toHaveBeenCalledWith({ path: outputFile, fullPage: false })
  })

  it('rejects an escaped destination before creating a screenshot directory or taking a screenshot', async () => {
    vi.stubEnv('E2E_SCREENSHOTS', '1')
    vi.stubEnv('E2E_THEME', 'system')
    const failure = new Error('The screenshot path escapes the current test output directory.')
    screenshots.outputPath.mockImplementation(() => {
      throw failure
    })
    const screenshot = vi.fn<Page['screenshot']>().mockResolvedValue(Buffer.alloc(0))

    await expect(screenshotIfEnabled(opaqueHandle<Page>({ screenshot }), '../../../foreign')).rejects.toBe(failure)

    expect(screenshots.mkdir).not.toHaveBeenCalled()
    expect(screenshot).not.toHaveBeenCalled()
  })

  it('returns a directory error before it takes a screenshot', async () => {
    vi.stubEnv('E2E_SCREENSHOTS', '1')
    vi.stubEnv('E2E_THEME', 'system')
    const outputFile = resolve('.tmp', 'ui-screenshots', 'shard-1', 'test-1', 'screenshots', 'system', 'chat.png')
    screenshots.outputPath.mockReturnValue(outputFile)
    const failure = new Error('The screenshot directory is not writable.')
    screenshots.mkdir.mockImplementation(() => {
      throw failure
    })
    const screenshot = vi.fn<Page['screenshot']>().mockResolvedValue(Buffer.alloc(0))

    await expect(screenshotIfEnabled(opaqueHandle<Page>({ screenshot }), 'chat')).rejects.toBe(failure)

    expect(screenshots.mkdir).toHaveBeenCalledWith(dirname(outputFile), { recursive: true })
    expect(screenshot).not.toHaveBeenCalled()
  })
})

describe('isMaybeVisible', () => {
  it.each([true, false])('returns immediate visibility without timed options: %s', async (visible) => {
    const isVisible = vi.fn(async () => visible)
    const locator = opaqueHandle<PlaywrightLocator>({ isVisible })
    await expect(isMaybeVisible(locator)).resolves.toBe(visible)
    expect(isVisible).toHaveBeenCalledWith()
  })

  it('returns false when the immediate visibility read fails', async () => {
    const locator = opaqueHandle<PlaywrightLocator>({ isVisible: async () => {
      throw new Error('The native locator detached.')
    } })
    await expect(isMaybeVisible(locator)).resolves.toBe(false)
  })
})

/** Check each supplied method. Reject every missing method on the opaque test handle. */
function opaqueHandle<T extends object>(methods: Partial<T>, prototype: object = {}): T {
  const target = Object.assign(prototype, methods) as T
  return new Proxy(target, {
    get: (value, property, receiver) => {
      if (property in value)
        return Reflect.get(value, property, receiver)
      throw new Error(`The UI unit accessed an unimplemented handle property: ${String(property)}.`)
    },
  })
}

function settingsAgent(): AgentInfo {
  return create(AgentInfoSchema, {
    id: 'selected-native-agent',
    workerId: 'resolved-native-worker',
    agentProvider: AgentProvider.MIMO_CODE,
    status: AgentStatus.ACTIVE,
    optionGroups: [{ id: MIMO_OPTION.PermissionPolicy, label: 'Permissions', mutable: true, currentValue: MIMO_PERMISSION_POLICY.Ask, options: [{ id: MIMO_PERMISSION_POLICY.Ask, name: 'Ask' }, { id: MIMO_PERMISSION_POLICY.Bypass, name: 'Bypass' }] }],
  })
}

interface PendingSettingsRequest {
  onWait: () => void
  completion: Promise<void>
}

function pageWithNativeCatalog(options: { agent?: AgentInfo, presetMenuWitness?: boolean, pendingSettings?: PendingSettingsRequest, pageUrl?: string } = {}): Page {
  class Locator {
    readonly _apiName = 'Locator'
    constructor(private readonly matches = true, private readonly pendingSettings?: PendingSettingsRequest) {}
    async _expect() {
      if (this.pendingSettings) {
        this.pendingSettings.onWait()
        await this.pendingSettings.completion
      }
      return { matches: this.matches, received: true, log: [], timedOut: false }
    }
  }
  const activeTab: PlaywrightLocator = opaqueHandle<PlaywrightLocator>({
    first: () => activeTab,
    filter: () => activeTab,
    getAttribute: async attribute => attribute === 'data-tab-id' ? 'selected-native-agent' : attribute === 'data-tab-type' ? 'agent' : null,
  }, new Locator())
  const spinner = opaqueHandle<PlaywrightLocator>({}, new Locator(false, options.pendingSettings))
  const presetAction: PlaywrightLocator = opaqueHandle<PlaywrightLocator>({ getByTestId: () => presetAction }, new Locator())
  const context = opaqueHandle<BrowserContext>({ cookies: async () => [{ name: 'leapmux-session', value: 'unit-native-session', domain: 'localhost', path: '/', expires: -1, httpOnly: true, secure: false, sameSite: 'Lax' }] })
  native.agent.mockReset().mockResolvedValue(options.agent ?? settingsAgent())
  vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(JSON.stringify({ tab: { tabId: 'selected-native-agent', workerId: 'resolved-native-worker', tabType: 'TAB_TYPE_AGENT' } }), { status: 200, headers: { 'content-type': 'application/json' } }))
  return opaqueHandle<Page>({
    context: () => context,
    url: () => options.pageUrl ?? 'http://127.0.0.1:61616/workspaces/unit-workspace',
    locator: (selector: string) => {
      if (selector.includes('aria-selected="true"'))
        return activeTab
      if (selector === '[data-testid="settings-loading-spinner"]')
        return spinner
      if (options.presetMenuWitness && selector === '[data-testid="composer-plus-popover"]')
        return presetAction
      throw new Error('The invalid native option reached a browser menu before catalog validation.')
    },
    evaluate: async () => {
      if (options.presetMenuWitness)
        throw new Error('The preset menu witness reached the validated native option.')
      throw new Error('The invalid native option reached a browser menu before catalog validation.')
    },
  })
}

describe('chooseSettingsOption', () => {
  it.each([
    { label: 'active catalog', settled: settingsAgent(), expected: 'The native catalog has no option group permissionPolicy.' },
    { label: 'startup failure', settled: create(AgentInfoSchema, { ...settingsAgent(), status: AgentStatus.STARTUP_FAILED, startupError: 'The native replacement refused its settings.' }), expected: 'The native settings agent failed to start: The native replacement refused its settings.' },
    { label: 'inactive agent', settled: create(AgentInfoSchema, { ...settingsAgent(), status: AgentStatus.INACTIVE }), expected: 'The native settings agent is not active on its owning Worker (INACTIVE).' },
    { label: 'absent row', settled: null, expected: 'The native settings agent is not active on its owning Worker (no row).' },
  ])('waits for the previous settings request before reading the $label', async ({ settled, expected }) => {
    const waiting = deferred<void>()
    const completed = deferred<void>()
    const queried = deferred<void>()
    const page = pageWithNativeCatalog({ pendingSettings: { onWait: () => waiting.resolve(), completion: completed.promise } })
    let settingsCompleted = false
    native.agent.mockImplementation(async () => {
      queried.resolve()
      return settingsCompleted ? settled : create(AgentInfoSchema, { ...settingsAgent(), status: AgentStatus.INACTIVE })
    })
    const selection = chooseSettingsOption(page, 'permissionPolicy-ask')
    const outcome = selection.then(() => undefined, (error: unknown) => error)
    try {
      // Observe the first real operation. The old helper queries the Worker before it observes settings completion.
      await Promise.race([waiting.promise, queried.promise])
      expect(native.agent).not.toHaveBeenCalled()
    }
    finally {
      settingsCompleted = true
      completed.resolve()
      await outcome
    }
    const observed = await outcome
    expect(observed).toBeInstanceOf(Error)
    if (!(observed instanceof Error))
      throw new Error('The settings regression reached the browser menu instead of catalog validation.')
    expect(observed.message).toBe(expected)
    expect(native.agent).toHaveBeenCalledTimes(1)
    expect(native.agent).toHaveBeenCalledWith({ leapmuxServer: { hubUrl: 'http://127.0.0.1:61616', adminToken: 'leapmux-session=unit-native-session', workerId: 'resolved-native-worker' } }, 'selected-native-agent')
  })

  it('reads the tab and its Worker from the hub that serves the page, not the suite hub', async () => {
    // A spec can run its own hub and Worker. That hub's session cookie is not valid on the suite hub.
    const page = pageWithNativeCatalog({ pageUrl: 'http://127.0.0.1:52222/workspaces/private-workspace' })
    await expect(chooseSettingsOption(page, 'permissionPolicy-ask')).rejects.toThrow('The native catalog has no option group permissionPolicy.')
    expect(vi.mocked(globalThis.fetch)).toHaveBeenCalledWith('http://127.0.0.1:52222/leapmux.v1.WorkspaceService/LocateTab', expect.anything())
    expect(native.agent).toHaveBeenCalledWith({ leapmuxServer: { hubUrl: 'http://127.0.0.1:52222', adminToken: 'leapmux-session=unit-native-session', workerId: 'resolved-native-worker' } }, 'selected-native-agent')
  })

  it.each(['about:blank', 'data:text/html,native'])('refuses a page that no hub serves: %s', async (pageUrl) => {
    const page = pageWithNativeCatalog({ pageUrl })
    await expect(chooseSettingsOption(page, 'permissionPolicy-ask')).rejects.toThrow('needs a page that a hub serves')
    expect(vi.mocked(globalThis.fetch)).not.toHaveBeenCalled()
  })

  it('rejects an absent native group before opening a settings menu', async () => {
    const page = pageWithNativeCatalog()
    await expect(chooseSettingsOption(page, 'permissionPolicy-ask')).rejects.toThrow('The native catalog has no option group permissionPolicy.')
  })

  it('rejects an absent native option before opening a settings menu', async () => {
    const page = pageWithNativeCatalog()
    await expect(chooseSettingsOption(page, `${MIMO_OPTION.PermissionPolicy}-not-a-native-value`)).rejects.toThrow('The native catalog has no value not-a-native-value for option group permission_policy.')
  })

  it('waits for native startup before validating the actual option catalog', async () => {
    const page = pageWithNativeCatalog()
    native.agent.mockResolvedValueOnce(create(AgentInfoSchema, { ...settingsAgent(), status: AgentStatus.STARTING }))
    await expect(chooseSettingsOption(page, 'permissionPolicy-ask')).rejects.toThrow('The native catalog has no option group permissionPolicy.')
    expect(native.agent.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  it('reports the exact native startup failure before any menu retry', async () => {
    const failed = create(AgentInfoSchema, { ...settingsAgent(), status: AgentStatus.STARTUP_FAILED, startupError: 'The installed native process refused the private configuration.' })
    const page = pageWithNativeCatalog({ agent: failed })
    native.agent.mockResolvedValueOnce(create(AgentInfoSchema, { ...settingsAgent(), status: AgentStatus.STARTING }))
    await expect(chooseSettingsOption(page, 'permissionPolicy-ask')).rejects.toThrow(failed.startupError)
    expect(native.agent.mock.calls.length).toBeGreaterThanOrEqual(2)
  })
})

describe('applyPermissionPreset', () => {
  it('uses the actual Amp preset metadata for a mutable native Worker catalog', async () => {
    const agent = create(AgentInfoSchema, {
      ...settingsAgent(),
      agentProvider: AgentProvider.AMP,
      optionGroups: [create(AvailableOptionGroupSchema, { id: 'permissionMode', label: 'Permissions', mutable: true, currentValue: AMP_PERMISSION_MODE.AllowAll, options: [{ id: AMP_PERMISSION_MODE.Ask, name: 'Ask' }, { id: AMP_PERMISSION_MODE.AllowAll, name: 'Allow All' }] })],
    })
    expect(permissionPresetAvailable(ampControls.permissionPresets?.bypass, agent.optionGroups)).toBe(true)
    const page = pageWithNativeCatalog({ agent, presetMenuWitness: true })
    await expect(applyPermissionPreset(page, 'bypass')).rejects.toThrow('The preset menu witness reached the validated native option.')
  })
})

describe('enterMessageText', () => {
  function keyboard() {
    const calls: string[] = []
    const handle = opaqueHandle<Page>({ keyboard: opaqueHandle<Page['keyboard']>({
      type: vi.fn(async (text: string) => { calls.push(`type:${text}`) }),
      insertText: vi.fn(async (text: string) => { calls.push(`insert:${text}`) }),
      press: vi.fn(async (key: string) => { calls.push(`press:${key}`) }),
    }) })
    return { handle, calls }
  }

  it('types the whole text by default', async () => {
    const { handle, calls } = keyboard()
    await enterMessageText(handle, 'first\n\nsecond', 'type')
    expect(calls).toEqual(['type:first\n\nsecond'])
  })

  // `keyboard.type` sends key events for each character, so a 144 KB prompt outlasts a test.
  it('inserts each line at once and presses Enter between the lines', async () => {
    const { handle, calls } = keyboard()
    await enterMessageText(handle, 'first line\n\nsecond line', 'insert')
    expect(calls).toEqual(['insert:first line', 'press:Enter', 'press:Enter', 'insert:second line'])
  })

  it('inserts a long line with one request', async () => {
    const { handle, calls } = keyboard()
    const line = 'padding '.repeat(18_000)
    await enterMessageText(handle, line, 'insert')
    expect(calls).toEqual([`insert:${line}`])
  })

  it('presses Enter for a leading and a trailing line break and inserts nothing for an empty line', async () => {
    const { handle, calls } = keyboard()
    await enterMessageText(handle, '\nbody\n', 'insert')
    expect(calls).toEqual(['press:Enter', 'insert:body', 'press:Enter'])
  })

  it('does nothing for empty text', async () => {
    const { handle, calls } = keyboard()
    await enterMessageText(handle, '', 'insert')
    expect(calls).toEqual([])
  })
})

describe('waitForNativeSettingsHydrated', () => {
  function fastAgentCatalog(groups: Parameters<typeof create<typeof AvailableOptionGroupSchema>>[1][]): AgentInfo {
    return create(AgentInfoSchema, {
      ...settingsAgent(),
      agentProvider: AgentProvider.FAST_AGENT,
      optionGroups: groups.map(group => create(AvailableOptionGroupSchema, group)),
    })
  }

  const agentMode = { id: 'permissionMode', label: 'Mode', mutable: true, currentValue: 'agent', options: [{ id: 'agent', name: 'Agent' }] }

  /** A page whose plus menu offers the `offered` group triggers. It records each group that the helper reads. */
  function pageWithSettingsMenu(offered: readonly string[]): { page: Page, read: string[] } {
    class Locator {
      readonly _apiName = 'Locator'
      constructor(private readonly matches: boolean) {}
      async _expect() {
        return { matches: this.matches, received: true, log: [], timedOut: false }
      }
    }
    const read: string[] = []
    const activeTab: PlaywrightLocator = opaqueHandle<PlaywrightLocator>({
      first: () => activeTab,
      getAttribute: async attribute => attribute === 'data-tab-id' ? 'selected-native-agent' : null,
    }, new Locator(true))
    const plus = opaqueHandle<PlaywrightLocator>({ getAttribute: async () => 'true' }, new Locator(true))
    const context = opaqueHandle<BrowserContext>({ cookies: async () => [{ name: 'leapmux-session', value: 'unit-native-session', domain: 'localhost', path: '/', expires: -1, httpOnly: true, secure: false, sameSite: 'Lax' }] })
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(JSON.stringify({ tab: { tabId: 'selected-native-agent', workerId: 'resolved-native-worker', tabType: 'TAB_TYPE_AGENT' } }), { status: 200, headers: { 'content-type': 'application/json' } }))
    const page = opaqueHandle<Page>({
      context: () => context,
      url: () => 'http://127.0.0.1:61616/workspaces/unit-workspace',
      evaluate: async () => undefined,
      locator: (selector: string) => {
        if (selector.includes('aria-selected="true"'))
          return activeTab
        if (selector === '[data-testid="settings-loading-spinner"]')
          return opaqueHandle<PlaywrightLocator>({}, new Locator(false))
        if (selector === '[data-testid="composer-plus-trigger"]')
          return plus
        if (selector === '[data-testid="composer-plus-popover"]')
          return opaqueHandle<PlaywrightLocator>({}, new Locator(true))
        const group = /^\[data-testid="composer-group-([^"]+)"\]$/.exec(selector)?.[1]
        if (group !== undefined) {
          return opaqueHandle<PlaywrightLocator>({ isVisible: async () => {
            read.push(group)
            return offered.includes(group)
          } })
        }
        throw new Error(`The settings hydration unit read an unexpected locator: ${selector}.`)
      },
    })
    return { page, read }
  }

  it('waits for the live groups of an agent that has no model group', async () => {
    const { page, read } = pageWithSettingsMenu(['permissionMode'])
    native.agent.mockReset().mockResolvedValue(fastAgentCatalog([agentMode]))
    await expect(waitForNativeSettingsHydrated(page)).resolves.toBeUndefined()
    expect(read).toEqual(['permissionMode'])
    expect(native.agent).toHaveBeenCalledWith({ leapmuxServer: { hubUrl: 'http://127.0.0.1:61616', adminToken: 'leapmux-session=unit-native-session', workerId: 'resolved-native-worker' } }, 'selected-native-agent')
  })

  it('requires every live group that the menu can draw, and skips a group with no option', async () => {
    const { page, read } = pageWithSettingsMenu(['model', 'permissionMode'])
    native.agent.mockReset().mockResolvedValue(fastAgentCatalog([
      { id: 'model', label: 'Model', mutable: false, currentValue: 'gpt-4o', options: [{ id: 'gpt-4o', name: 'gpt-4o' }] },
      { id: 'effort', label: 'Effort', mutable: true, currentValue: '', options: [] },
      agentMode,
    ]))
    await expect(waitForNativeSettingsHydrated(page)).resolves.toBeUndefined()
    expect(read).toEqual(['model', 'permissionMode'])
  })

  it('fails while the menu still lacks a group of the live catalog', async () => {
    // The read-only model group of a starting agent is not the live catalog of a running one.
    const { page } = pageWithSettingsMenu(['model'])
    native.agent.mockReset().mockResolvedValue(fastAgentCatalog([agentMode]))
    await expect(waitForNativeSettingsHydrated(page)).rejects.toThrow('the menu offers the permissionMode group')
  })

  it('reads the live catalog again in the attempt instead of the first snapshot', async () => {
    const { page, read } = pageWithSettingsMenu(['permissionMode'])
    native.agent.mockReset()
      .mockResolvedValueOnce(fastAgentCatalog([{ id: 'model', label: 'Model', mutable: false, currentValue: 'gpt-4o', options: [{ id: 'gpt-4o', name: 'gpt-4o' }] }]))
      .mockResolvedValue(fastAgentCatalog([agentMode]))
    await expect(waitForNativeSettingsHydrated(page)).resolves.toBeUndefined()
    expect(read).toEqual(['permissionMode'])
    expect(native.agent).toHaveBeenCalledTimes(2)
  })

  it.each([
    { label: 'no group', groups: [] },
    { label: 'only groups without an option', groups: [{ id: 'effort', label: 'Effort', mutable: true, currentValue: '', options: [] }] },
  ])('fails for a live catalog with $label, which the menu cannot show', async ({ groups }) => {
    const { page, read } = pageWithSettingsMenu(['model'])
    native.agent.mockReset().mockResolvedValue(fastAgentCatalog(groups))
    await expect(waitForNativeSettingsHydrated(page)).rejects.toThrow('The live native catalog has no group with an option')
    expect(read).toEqual([])
  })

  it('fails when the agent leaves the active state before the menu shows its catalog', async () => {
    const { page, read } = pageWithSettingsMenu(['permissionMode'])
    native.agent.mockReset()
      .mockResolvedValueOnce(fastAgentCatalog([agentMode]))
      .mockResolvedValue(create(AgentInfoSchema, { ...fastAgentCatalog([agentMode]), status: AgentStatus.INACTIVE }))
    await expect(waitForNativeSettingsHydrated(page)).rejects.toThrow('The native settings agent is not active on its owning Worker (INACTIVE).')
    expect(read).toEqual([])
  })
})

describe('waitForAgentIdle', () => {
  const ends: Array<() => void> = []
  afterEach(() => {
    for (const end of ends.splice(0))
      end()
  })

  /** A page whose thinking indicator is never shown. It records the timeout of each assertion on the indicator. */
  function idlePage(): { page: Page, timeouts: number[], selectors: string[] } {
    const timeouts: number[] = []
    const selectors: string[] = []
    class Locator {
      readonly _apiName = 'Locator'
      async waitFor() {
        throw new Error('The thinking indicator never appeared.')
      }

      async _expect(_expression: string, options: { timeout: number }) {
        timeouts.push(options.timeout)
        return { matches: false, received: false, log: [], timedOut: false }
      }
    }
    const page = opaqueHandle<Page>({
      locator: (selector: string) => {
        selectors.push(selector)
        return new Locator() as unknown as PlaywrightLocator
      },
    })
    return { page, timeouts, selectors }
  }

  it('waits on the visible indicator until the report margin before the test deadline', async () => {
    ends.push(startTestDeadline(1_000_000, () => 120_000))
    vi.spyOn(Date, 'now').mockReturnValue(1_010_000)
    const { page, timeouts, selectors } = idlePage()
    await waitForAgentIdle(page)
    expect(selectors).toEqual(['[data-testid="thinking-indicator"]:visible'])
    expect(timeouts).toEqual([120_000 - 10_000 - WAIT_REPORT_MARGIN_MS])
  })

  it('follows a timeout that the test raised, as the Kilo goal specs do', async () => {
    let timeout = 120_000
    ends.push(startTestDeadline(1_000_000, () => timeout))
    timeout = 240_000
    vi.spyOn(Date, 'now').mockReturnValue(1_010_000)
    const { page, timeouts } = idlePage()
    await waitForAgentIdle(page)
    expect(timeouts).toEqual([240_000 - 10_000 - WAIT_REPORT_MARGIN_MS])
  })

  it('waits without a limit when the test has no timeout', async () => {
    ends.push(startTestDeadline(1_000_000, () => 0))
    const { page, timeouts } = idlePage()
    await waitForAgentIdle(page)
    expect(timeouts).toEqual([0])
  })

  it('fails at once instead of removing the limit when the deadline is inside the margin', async () => {
    ends.push(startTestDeadline(1_000_000, () => 120_000))
    vi.spyOn(Date, 'now').mockReturnValue(1_119_000)
    const { page, timeouts } = idlePage()
    await waitForAgentIdle(page)
    expect(timeouts).toEqual([1])
  })
})

describe('waitForLayoutSave', () => {
  function browser(): { page: Pick<Page, 'evaluate'>, target: EventTarget } {
    const target = new EventTarget()
    vi.stubGlobal('window', target)
    return { target, page: { evaluate: vi.fn().mockImplementation(callback => callback()) } }
  }

  it('uses only the transient listener for each wait', async () => {
    vi.useFakeTimers()
    const { page, target } = browser()
    const add = vi.spyOn(target, 'addEventListener')
    const saved = waitForLayoutSave(page)
    expect(add).toHaveBeenCalledTimes(1)
    target.dispatchEvent(new Event('leapmux:layout-saved'))
    await saved
  })

  it('removes its transient listener when the save deadline expires', async () => {
    vi.useFakeTimers()
    const { page, target } = browser()
    const remove = vi.spyOn(target, 'removeEventListener')
    const pending = waitForLayoutSave(page)
    const rejection = expect(pending).rejects.toThrow('layout save timeout')
    await vi.advanceTimersByTimeAsync(30_000)
    await rejection
    expect(remove).toHaveBeenCalledWith('leapmux:layout-saved', expect.any(Function))
    expect(vi.getTimerCount()).toBe(0)
  })

  it('observes a save before the returned promise is awaited', async () => {
    vi.useFakeTimers()
    const { page, target } = browser()
    const saved = waitForLayoutSave(page)
    target.dispatchEvent(new Event('leapmux:layout-saved'))
    await saved
    expect(vi.getTimerCount()).toBe(0)
  })

  it('lets concurrent waiters observe one save and requires a new save for a later waiter', async () => {
    vi.useFakeTimers()
    const { page, target } = browser()
    const first = waitForLayoutSave(page)
    const second = waitForLayoutSave(page)
    target.dispatchEvent(new Event('leapmux:layout-saved'))
    await Promise.all([first, second])
    let thirdResolved = false
    const third = waitForLayoutSave(page).then(() => thirdResolved = true)
    await Promise.resolve()
    expect(thirdResolved).toBe(false)
    target.dispatchEvent(new Event('leapmux:layout-saved'))
    await third
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('ARITHMETIC_ANSWER', () => {
  it('matches the literal a scenario returns', () => {
    expect(ARITHMETIC_ANSWER.test(ARITHMETIC_ANSWER_TEXT)).toBe(true)
    expect(SECOND_ARITHMETIC_ANSWER.test(SECOND_ARITHMETIC_ANSWER_TEXT)).toBe(true)
  })

  it('states the arithmetic each prompt asks for', () => {
    expect(String(1234 + 5678)).toBe(ARITHMETIC_ANSWER_TEXT)
    expect(ARITHMETIC_PROMPT).toContain('1234 + 5678')
    expect(String(1111 + 2222)).toBe(SECOND_ARITHMETIC_ANSWER_TEXT)
    expect(SECOND_ARITHMETIC_PROMPT).toContain('1111 + 2222')
  })

  it('keeps the two answers from satisfying each other', () => {
    expect(ARITHMETIC_ANSWER.test(SECOND_ARITHMETIC_ANSWER_TEXT)).toBe(false)
    expect(SECOND_ARITHMETIC_ANSWER.test(ARITHMETIC_ANSWER_TEXT)).toBe(false)
  })
})
