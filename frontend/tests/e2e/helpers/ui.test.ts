import type { BrowserContext, Page, Locator as PlaywrightLocator } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { PermissionShortcutState } from './ui'
import { create } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import { fakeLocator, fakeLocatorTree, recordingLocator } from '~/test-support/fakeLocator'
import { ampControls } from '../../../src/components/chat/providers/amp/pluginControls'
import { permissionPresetAvailable } from '../../../src/components/chat/providerSettings'
import { AMP_PERMISSION_MODE } from '../../../src/generated/contracts/amp-protocol'
import { MIMO_OPTION, MIMO_PERMISSION_POLICY } from '../../../src/generated/contracts/mimo-protocol'
import { AgentInfoSchema, AgentProvider, AgentStatus, AvailableOptionGroupSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { solveCaptchaViaUI } from './captcha'
import { cssAttributeValue } from './cssAttribute'
import { startTestDeadline, WAIT_REPORT_MARGIN_MS } from './testDeadline'
import {
  activeWorkspaceId,
  agentTabs,
  answerControl,
  answerElevationPrompt,
  answerPlanReview,
  applyPermissionPreset,
  archiveWorkspaceViaUI,
  ARITHMETIC_ANSWER,
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  branchGroupRow,
  chatScrollContainer,
  chooseSettingsOption,
  collapseWorkspaceRow,
  COMPOSER_EDITOR_SELECTOR,
  composerEditor,
  controlActions,
  controlBanner,
  controlButton,
  deleteWorkspaceViaUI,
  elevationPrompt,
  enterControlFeedback,
  enterMessageText,
  expandSidebarSection,
  expandWorkspaceRow,
  expectAgentTabCount,
  expectDialogStaysOpen,
  expectNoControlBanner,
  expectPermissionShortcuts,
  expectRowsInOrder,
  focusComposer,
  inputQueue,
  interruptButton,
  isMaybeVisible,
  offeredSettingsOptions,
  openAppAs,
  openTerminalViaUI,
  openWorkspace,
  openWorkspaceRowMenu,
  platformModifier,
  questionPagination,
  queuePauseButton,
  railedRows,
  resumePausedQueue,
  resumeQueueAfterFailure,
  rowOrderProblem,
  SECOND_ARITHMETIC_ANSWER,
  SECOND_ARITHMETIC_ANSWER_TEXT,
  SECOND_ARITHMETIC_PROMPT,
  sidebarLeaves,
  sidebarSectionHeader,
  subagentReportBubble,
  submitLoginForm,
  tabById,
  terminalTabs,
  tiles,
  toggleModeWithShortcut,
  toolCallRow,
  toolRows,
  treeRow,
  waitForAgentIdle,
  waitForAgentStarted,
  waitForControlBanner,
  waitForLayoutSave,
  waitForNativeSettingsHydrated,
  workspaceChildren,
  workspaceMenuItem,
  workspaceRowTitle,
} from './ui'

const native = vi.hoisted(() => ({ agent: vi.fn<typeof import('./nativeScenario').nativeAgentById>() }))
vi.mock('./nativeScenario', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./nativeScenario')>()
  return { ...actual, nativeAgentById: native.agent }
})
vi.mock('./captcha', () => ({ solveCaptchaViaUI: vi.fn(async () => {}) }))
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
  return { ...actual, expect: firstAttempt }
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
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

/**
 * Check each supplied method. Reject every missing method on the opaque test handle.
 * Two reads answer undefined:
 * - `then`, which an async function reads on the handle that it returns, so the handle is not a thenable.
 * - A symbol, which a failure message reads to describe the handle.
 */
function opaqueHandle<T extends object>(methods: Partial<T>, prototype: object = {}): T {
  const target = Object.assign(prototype, methods) as T
  return new Proxy(target, {
    get: (value, property, receiver) => {
      if (property in value)
        return Reflect.get(value, property, receiver)
      if (property === 'then' || typeof property === 'symbol')
        return undefined
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
  /** A locator whose checks answer `matches`, after the pending settings request, when one is given, completes. */
  const catalogLocator = (matches = true, pendingSettings?: PendingSettingsRequest) => fakeLocator(async () => {
    if (pendingSettings) {
      pendingSettings.onWait()
      await pendingSettings.completion
    }
    return matches
  })
  const activeTab: PlaywrightLocator = opaqueHandle<PlaywrightLocator>({
    first: () => activeTab,
    filter: () => activeTab,
    getAttribute: async attribute => attribute === 'data-tab-id' ? 'selected-native-agent' : attribute === 'data-tab-type' ? 'agent' : null,
  }, catalogLocator())
  const spinner = opaqueHandle<PlaywrightLocator>({}, catalogLocator(false, options.pendingSettings))
  const presetAction: PlaywrightLocator = opaqueHandle<PlaywrightLocator>({ getByTestId: () => presetAction }, catalogLocator())
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
    const read: string[] = []
    const activeTab: PlaywrightLocator = opaqueHandle<PlaywrightLocator>({
      first: () => activeTab,
      getAttribute: async attribute => attribute === 'data-tab-id' ? 'selected-native-agent' : null,
    }, fakeLocator(() => true))
    const plus = opaqueHandle<PlaywrightLocator>({ getAttribute: async () => 'true' }, fakeLocator(() => true))
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
          return opaqueHandle<PlaywrightLocator>({}, fakeLocator(() => false))
        if (selector === '[data-testid="composer-plus-trigger"]')
          return plus
        if (selector === '[data-testid="composer-plus-popover"]')
          return opaqueHandle<PlaywrightLocator>({}, fakeLocator(() => true))
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
    const page = opaqueHandle<Page>({
      locator: (selector: string) => {
        selectors.push(selector)
        return fakeLocator((check) => {
          if (check.timeout !== undefined)
            timeouts.push(check.timeout)
          return false
        }, {
          waitFor: async () => {
            throw new Error('The thinking indicator never appeared.')
          },
        })
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

/** A page whose `locator` returns the selector, so a test reads the exact selector that a helper builds. */
function selectorPage(): Page {
  return Object.assign({} as Page, { locator: (selector: string) => selector })
}

describe('toolCallRow', () => {
  it('selects the visible result row of the exact call ID', () => {
    expect(toolCallRow(selectorPage(), 'live-child-read')).toBe(
      '[data-testid="message-bubble"][data-tool-call-id="live-child-read"][data-tool-row-role="result"]:visible',
    )
  })

  it.each(['request', 'update', 'result'] as const)('selects the visible %s row of the call', (role) => {
    expect(toolCallRow(selectorPage(), 'call-1', role)).toBe(
      `[data-testid="message-bubble"][data-tool-call-id="call-1"][data-tool-row-role="${role}"]:visible`,
    )
  })

  it('escapes a quote and a backslash in the call ID for a quoted attribute value', () => {
    expect(toolCallRow(selectorPage(), 'a"b\\c')).toBe(
      '[data-testid="message-bubble"][data-tool-call-id="a\\"b\\\\c"][data-tool-row-role="result"]:visible',
    )
  })

  it('keeps an empty call ID as an empty attribute value, so it matches no real call', () => {
    expect(toolCallRow(selectorPage(), '')).toContain('[data-tool-call-id=""]')
  })

  it.each([
    { callId: 'a\nb', escaped: 'a\\a b' },
    { callId: 'a\rb', escaped: 'a\\d b' },
    { callId: 'a\fb', escaped: 'a\\c b' },
  ])('escapes the line break in $callId as a CSS hex escape, because a quoted CSS string cannot hold one', ({ callId, escaped }) => {
    expect(toolCallRow(selectorPage(), callId)).toContain(`[data-tool-call-id="${escaped}"]`)
  })

  // `cssAttribute.test.ts` matches each escaped value against a CSS parser. This file runs without a DOM.
  it('escapes the call ID through cssAttributeValue', () => {
    const callId = 'Run"Shell\\Command\n1'
    expect(toolCallRow(selectorPage(), callId)).toContain(`[data-tool-call-id="${cssAttributeValue(callId)}"]`)
  })
})

describe('chat and tab locators', () => {
  it.each([
    { name: 'toolRows', locate: toolRows, selector: '[data-tool-message]:visible' },
    { name: 'chatScrollContainer', locate: chatScrollContainer, selector: '[data-chat-scroll-container="true"]:visible' },
    { name: 'composerEditor', locate: composerEditor, selector: '[data-testid="composer-editor"]:visible .ProseMirror' },
    { name: 'interruptButton', locate: interruptButton, selector: '[data-testid="interrupt-button"]:visible' },
    { name: 'agentTabs', locate: agentTabs, selector: '[data-testid="tab"][data-tab-type="agent"]' },
    { name: 'terminalTabs', locate: terminalTabs, selector: '[data-testid="tab"][data-tab-type="terminal"]' },
    { name: 'tiles', locate: tiles, selector: '[data-testid="tile"]' },
  ])('$name selects $selector', ({ locate, selector }) => {
    expect(locate(selectorPage())).toBe(selector)
  })

  it('selects a tab by its ID, and escapes the ID through cssAttributeValue', () => {
    expect(tabById(selectorPage(), 'tab-1')).toBe('[data-testid="tab"][data-tab-id="tab-1"]')
    const tabId = 'tab"1\\\n'
    expect(tabById(selectorPage(), tabId)).toBe(`[data-testid="tab"][data-tab-id="${cssAttributeValue(tabId)}"]`)
  })
})

/** A locator whose assertions record their expression and answer from `answer`. */
function assertingLocator(name: string, log: string[], answer: (expression: string, options: { expectedNumber?: number }) => boolean, methods: Partial<PlaywrightLocator> = {}): PlaywrightLocator {
  return opaqueHandle<PlaywrightLocator>(methods, recordingLocator(name, log, check => answer(check.expression, check)))
}

/**
 * A fake queue toggle. Its label follows the paused state, and a click toggles that state.
 * The log records each label check with the label it required, and each click.
 */
function queueToggle(state: { paused: boolean }, log: string[]): { page: Page, button: PlaywrightLocator } {
  const label = () => state.paused ? 'Resume Queue' : 'Pause Queue'
  const button = assertingLocator('queue', log, (expression, options) => {
    const required = JSON.stringify(options).includes('Resume Queue') ? 'Resume Queue' : 'Pause Queue'
    log.push(`requires ${required}`)
    return expression === 'to.have.text' && required === label()
  }, {
    click: async () => {
      log.push('click')
      state.paused = !state.paused
    },
  })
  const page = opaqueHandle<Page>({ locator: (selector: string) => {
    expect(selector).toBe('[data-testid="queue-pause-button"]:visible')
    return button
  } })
  return { page, button }
}

describe('queuePauseButton', () => {
  it('selects the pause toggle of the visible composer', () => {
    const log: string[] = []
    const { page, button } = queueToggle({ paused: false }, log)
    expect(queuePauseButton(page)).toBe(button)
  })
})

describe('inputQueue', () => {
  it('selects the visible input queue of the composer', () => {
    const queue = opaqueHandle<PlaywrightLocator>({})
    const page = opaqueHandle<Page>({
      locator: (selector: string) => {
        expect(selector).toBe('[data-testid="agent-input-queue"]:visible')
        return queue
      },
    })
    expect(inputQueue(page)).toBe(queue)
  })
})

describe('resumePausedQueue', () => {
  it('requires the paused label, clicks once, and requires the running label', async () => {
    const log: string[] = []
    const state = { paused: true }
    await resumePausedQueue(queueToggle(state, log).page)
    expect(log).toEqual(['queue:to.have.text', 'requires Resume Queue', 'click', 'queue:to.have.text', 'requires Pause Queue'])
    expect(state.paused).toBe(false)
  })

  it('refuses a running queue and does not click it', async () => {
    const log: string[] = []
    const state = { paused: false }
    await expect(resumePausedQueue(queueToggle(state, log).page)).rejects.toThrow(/toHaveText/)
    expect(log).not.toContain('click')
    expect(state.paused).toBe(false)
  })
})

describe('resumeQueueAfterFailure', () => {
  it('resumes a queue that the provider paused', async () => {
    const log: string[] = []
    const state = { paused: true }
    await resumeQueueAfterFailure(queueToggle(state, log).page, 'paused')
    expect(log).toContain('click')
    expect(state.paused).toBe(false)
  })

  it('requires a running queue, and leaves it untouched, for a provider that keeps it running', async () => {
    const log: string[] = []
    const state = { paused: false }
    await resumeQueueAfterFailure(queueToggle(state, log).page, 'running')
    expect(log).toEqual(['queue:to.have.text', 'requires Pause Queue'])
  })

  it('fails when the queue state differs from the state that the caller states', async () => {
    await expect(resumeQueueAfterFailure(queueToggle({ paused: true }, []).page, 'running')).rejects.toThrow(/toHaveText/)
    await expect(resumeQueueAfterFailure(queueToggle({ paused: false }, []).page, 'paused')).rejects.toThrow(/toHaveText/)
  })
})

describe('focusComposer', () => {
  it('waits for the visible composer, clicks it once, and returns it', async () => {
    const log: string[] = []
    const click = vi.fn(async () => {
      log.push('editor:click')
    })
    const editor = assertingLocator('editor', log, () => true, { click })
    const page = opaqueHandle<Page>({ locator: (selector: string) => {
      expect(selector).toBe('[data-testid="composer-editor"]:visible .ProseMirror')
      return editor
    } })
    expect(await focusComposer(page)).toBe(editor)
    expect(log).toEqual(['editor:to.be.visible', 'editor:click'])
  })
})

describe('toggleModeWithShortcut', () => {
  function modePage(chip: string) {
    const log: string[] = []
    const click = async () => {
      log.push('editor:click')
    }
    const editor = assertingLocator('editor', log, () => true, { click })
    const spinner = assertingLocator('spinner', log, expression => expression !== 'to.be.visible')
    const chips = opaqueHandle<PlaywrightLocator>({ filter: (options?: Parameters<PlaywrightLocator['filter']>[0]) => {
      const hasText = String(options?.hasText)
      return assertingLocator(`chip ${hasText}`, log, (_expression, count) => (count.expectedNumber === 0) !== (hasText === chip))
    } })
    const page = opaqueHandle<Page>({
      keyboard: opaqueHandle<Page['keyboard']>({ press: async (key: string) => { log.push(`press ${key}`) } }),
      locator: (selector: string) => {
        if (selector === '[data-testid="composer-editor"]:visible .ProseMirror')
          return editor
        if (selector === '[data-testid="settings-loading-spinner"]')
          return spinner
        if (selector === '[data-testid="composer-status-bar"] [data-testid$="-trigger"]')
          return chips
        throw new Error(`The mode page has no locator for ${selector}.`)
      },
    })
    return { page, log }
  }

  it('focuses the composer before the press, waits for the settings, and requires the chip', async () => {
    const { page, log } = modePage('Plan')
    await toggleModeWithShortcut(page, 'Plan')
    expect(log).toEqual(['editor:to.be.visible', 'editor:click', 'press Shift+Tab', 'spinner:to.be.visible', 'chip Plan:to.have.count=0'])
  })

  it('fails when the press selects another chip', async () => {
    await expect(toggleModeWithShortcut(modePage('Act').page, 'Plan')).rejects.toThrow()
  })
})

describe('expectAgentTabCount', () => {
  it('requires the exact count of agent tabs', async () => {
    const log: string[] = []
    const tabs = assertingLocator('tabs', log, (_expression, options) => options.expectedNumber === 2)
    const page = opaqueHandle<Page>({ locator: () => tabs })
    await expectAgentTabCount(page, 2)
    expect(log).toEqual(['tabs:to.have.count=2'])
    await expect(expectAgentTabCount(page, 3)).rejects.toThrow(/toHaveCount/)
  })

  it.each([-1, 1.5, Number.NaN])('refuses the count %s before it reads the page', async (count) => {
    const page = opaqueHandle<Page>({})
    await expect(expectAgentTabCount(page, count)).rejects.toThrow('nonnegative integer')
  })
})

describe('subagentReportBubble', () => {
  function chainPage() {
    const chain: unknown[] = []
    const link: PlaywrightLocator = opaqueHandle<PlaywrightLocator>({ filter: (options) => {
      chain.push(options?.hasText)
      return link
    } })
    const page = opaqueHandle<Page>({ locator: (selector: string) => {
      chain.push(selector)
      return link
    } })
    return { page, chain }
  }

  it('filters the visible bubbles to the report header and then to the report text', () => {
    const { page, chain } = chainPage()
    subagentReportBubble(page, /PONG/)
    expect(chain).toEqual(['[data-testid="message-bubble"]:visible', 'Subagent reported', /PONG/])
  })

  it('uses the label of the reporter that the provider gives', () => {
    const { page, chain } = chainPage()
    subagentReportBubble(page, 'done', 'Explorer')
    expect(chain).toEqual(['[data-testid="message-bubble"]:visible', 'Explorer reported', 'done'])
  })
})

describe('rowOrderProblem', () => {
  const rows = ['user prompt', 'READ_MARKER from the child', 'thinking', 'FINAL answer']

  it('accepts texts in their own rows in order, and reads the first row that holds a text', () => {
    expect(rowOrderProblem(rows, ['READ_MARKER', 'FINAL'])).toBe('')
    expect(rowOrderProblem([...rows, 'READ_MARKER again'], ['READ_MARKER', 'FINAL'])).toBe('')
  })

  it('fails for a missing text, which an order check through findIndex reads as -1', () => {
    expect(rowOrderProblem(rows, ['ABSENT', 'FINAL'])).toBe('no row holds "ABSENT"')
    expect(rowOrderProblem(rows, ['ABSENT', 'GONE'])).toBe('no row holds "ABSENT", "GONE"')
    expect(rowOrderProblem([], ['READ_MARKER', 'FINAL'])).toBe('no row holds "READ_MARKER", "FINAL"')
  })

  it('fails for a reversed order', () => {
    expect(rowOrderProblem(rows, ['FINAL', 'READ_MARKER'])).toBe('"READ_MARKER" (row 1) comes before "FINAL" (row 3)')
  })

  it('fails for two texts in one row', () => {
    expect(rowOrderProblem(['READ_MARKER and FINAL'], ['READ_MARKER', 'FINAL'])).toBe('"READ_MARKER" and "FINAL" are in one row (row 0)')
  })

  it('checks each neighbor pair of a longer list', () => {
    expect(rowOrderProblem(rows, ['user', 'READ_MARKER', 'FINAL'])).toBe('')
    expect(rowOrderProblem(rows, ['user', 'FINAL', 'thinking'])).toBe('"thinking" (row 2) comes before "FINAL" (row 3)')
  })
})

describe('expectRowsInOrder', () => {
  it('passes for rows that hold the texts in order', async () => {
    const rows = opaqueHandle<PlaywrightLocator>({ allTextContents: async () => ['first READ', 'then FINAL'] })
    await expect(expectRowsInOrder(rows, ['READ', 'FINAL'])).resolves.toBeUndefined()
  })

  it.each([
    { label: 'one text', texts: ['READ'], error: 'at least two texts' },
    { label: 'an empty text', texts: ['READ', ''], error: 'not empty' },
  ])('refuses $label before it reads the rows', async ({ texts, error }) => {
    const rows = opaqueHandle<PlaywrightLocator>({})
    await expect(expectRowsInOrder(rows, texts)).rejects.toThrow(error)
  })
})

/** A page with a composer `[+]` menu whose triggers report themselves open. It records each assertion and click. */
function plusMenuPage(options: { popover: PlaywrightLocator, groupPopover?: PlaywrightLocator, log: string[] }): Page {
  const trigger = (name: string) => assertingLocator(name, options.log, () => true, {
    getAttribute: async () => 'true',
    click: async () => { options.log.push(`${name}:click`) },
  })
  return opaqueHandle<Page>({
    evaluate: async () => {
      options.log.push('close-menus')
      return undefined
    },
    locator: (selector: string) => {
      if (selector === '[data-testid="composer-plus-trigger"]')
        return trigger('plus')
      if (selector === '[data-testid="composer-plus-popover"]')
        return options.popover
      if (selector.endsWith('-popover"]') && options.groupPopover)
        return options.groupPopover
      if (selector.startsWith('[data-testid="composer-group-'))
        return trigger('group')
      throw new Error(`The menu page has no locator for ${selector}.`)
    },
  })
}

describe('offeredSettingsOptions', () => {
  function menuWith(testIds: string[]) {
    const log: string[] = []
    const rowSelectors: string[] = []
    const groupPopover = opaqueHandle<PlaywrightLocator>({ locator: (selector: string) => {
      rowSelectors.push(selector)
      return opaqueHandle<PlaywrightLocator>({ evaluateAll: (async () => testIds) as unknown as PlaywrightLocator['evaluateAll'] })
    } })
    const page = plusMenuPage({ popover: opaqueHandle<PlaywrightLocator>({}), groupPopover, log })
    return { page, log, rowSelectors }
  }

  it('returns the offered values in menu order from the option rows alone, and closes the menus', async () => {
    const { page, log, rowSelectors } = menuWith(['permissionMode-default', 'permissionMode-danger-full-access', 'permissionMode-plan'])
    expect(await offeredSettingsOptions(page, 'permissionMode')).toEqual(['default', 'danger-full-access', 'plan'])
    // A label element and the filter box carry the same test ID prefix, so the rows come from their role alone.
    expect(rowSelectors).toEqual(['[role="menuitemradio"], [role="option"]'])
    expect(log.at(-2)).toBe('close-menus')
  })

  it('returns an empty list for a menu without an option row', async () => {
    expect(await offeredSettingsOptions(menuWith([]).page, 'effort')).toEqual([])
  })

  it.each(['effort-high', 'permissionMode-', ''])('fails for an option row whose test ID is not "<groupId>-<value>": %j', async (testId) => {
    await expect(offeredSettingsOptions(menuWith([testId]).page, 'permissionMode')).rejects.toThrow('is not "permissionMode-<value>"')
  })
})

describe('expectPermissionShortcuts', () => {
  function menuWith(states: Partial<Record<'smart' | 'bypass', PermissionShortcutState>>) {
    const log: string[] = []
    const shortcut = (kind: 'smart' | 'bypass') => {
      const state = states[kind] ?? 'absent'
      return assertingLocator(kind, log, (expression) => {
        if (expression === 'to.have.count')
          return state === 'absent'
        if (expression === 'to.be.visible')
          return state !== 'absent'
        if (expression === 'to.be.disabled')
          return state === 'disabled'
        if (expression === 'to.be.enabled')
          return state === 'offered'
        throw new Error(`The shortcut fake has no answer for ${expression}.`)
      })
    }
    const popover = opaqueHandle<PlaywrightLocator>({ getByTestId: (testId: string | RegExp) => {
      const kind = /^composer-(smart|bypass)-permissions$/.exec(String(testId))?.[1]
      if (kind !== 'smart' && kind !== 'bypass')
        throw new Error(`The menu has no shortcut ${String(testId)}.`)
      return shortcut(kind)
    } }, fakeLocator())
    return { page: plusMenuPage({ popover, log }), log }
  }

  it('opens a probe menu first, reads each stated shortcut in the next menu, and closes the menus', async () => {
    const { page, log } = menuWith({ smart: 'absent', bypass: 'offered' })
    await expectPermissionShortcuts(page, { smart: 'absent', bypass: 'offered' })
    const reads = log.filter(entry => entry.startsWith('smart:') || entry.startsWith('bypass:'))
    expect(reads).toEqual(['smart:to.have.count=0', 'bypass:to.be.visible', 'bypass:to.be.enabled'])
    const closes = log.map((entry, index) => entry === 'close-menus' ? index : -1).filter(index => index >= 0)
    expect(closes.length).toBeGreaterThanOrEqual(3)
    expect(log.at(-2)).toBe('close-menus')
  })

  it('requires a disabled shortcut while its preset is active', async () => {
    const { page, log } = menuWith({ bypass: 'disabled' })
    await expectPermissionShortcuts(page, { bypass: 'disabled' })
    expect(log.filter(entry => entry.startsWith('bypass:'))).toEqual(['bypass:to.be.visible', 'bypass:to.be.disabled'])
  })

  it.each([
    { label: 'an offered shortcut that the spec states absent', actual: { smart: 'offered' as const }, expected: { smart: 'absent' as const }, error: 'the menu has no smart permission shortcut' },
    { label: 'an absent shortcut that the spec states offered', actual: {}, expected: { bypass: 'offered' as const }, error: 'the menu offers the bypass permission shortcut' },
    { label: 'an enabled shortcut that the spec states disabled', actual: { bypass: 'offered' as const }, expected: { bypass: 'disabled' as const }, error: 'the bypass permission shortcut is disabled' },
    { label: 'a disabled shortcut that the spec states offered', actual: { smart: 'disabled' as const }, expected: { smart: 'offered' as const }, error: 'the smart permission shortcut is enabled' },
  ])('fails for $label', async ({ actual, expected, error }) => {
    await expect(expectPermissionShortcuts(menuWith(actual).page, expected)).rejects.toThrow(error)
  })

  it('refuses a check that states no shortcut', async () => {
    await expect(expectPermissionShortcuts(opaqueHandle<Page>({}), {})).rejects.toThrow('at least one shortcut')
  })
})

/**
 * A page whose `getByTestId(testId).filter({ visible: true })` returns `visible(testId)`.
 * Any other filter fails, so a test proves that a helper reads only the visible copy.
 */
function visibleTestIdPage(visible: (testId: string) => PlaywrightLocator): Page {
  return opaqueHandle<Page>({
    getByTestId: (testId: string | RegExp) => opaqueHandle<PlaywrightLocator>({ filter: (options?: Parameters<PlaywrightLocator['filter']>[0]) => {
      expect(options).toEqual({ visible: true })
      return visible(String(testId))
    } }),
  })
}

describe('controlBanner', () => {
  it('selects every visible control request banner', () => {
    const banner = opaqueHandle<PlaywrightLocator>({})
    const page = visibleTestIdPage((testId) => {
      expect(testId).toBe('control-banner')
      return banner
    })
    expect(controlBanner(page)).toBe(banner)
  })
})

describe('controlButton', () => {
  it.each(['allow', 'deny', 'submit', 'stop', 'yolo'] as const)('selects every visible %s button', (action) => {
    const button = opaqueHandle<PlaywrightLocator>({})
    const page = visibleTestIdPage((testId) => {
      expect(testId).toBe(`control-${action}-btn`)
      return button
    })
    expect(controlButton(page, action)).toBe(button)
  })
})

describe('questionPagination', () => {
  it('selects the visible page buttons of a question from the page, not from the banner', () => {
    const pagination = opaqueHandle<PlaywrightLocator>({})
    const page = visibleTestIdPage((testId) => {
      expect(testId).toBe('control-pagination')
      return pagination
    })
    expect(questionPagination(page)).toBe(pagination)
  })
})

describe('railedRows', () => {
  it('selects each visible row that draws a span rail', () => {
    const rows = opaqueHandle<PlaywrightLocator>({})
    const page = opaqueHandle<Page>({
      locator: (selector: string) => {
        expect(selector).toBe('[data-span-columns]:not([data-span-columns="0"]):visible')
        return rows
      },
    })
    expect(railedRows(page)).toBe(rows)
  })
})

describe('controlActions', () => {
  it('selects the visible fieldset of the control request actions', () => {
    const fieldset = opaqueHandle<PlaywrightLocator>({})
    const page = visibleTestIdPage((testId) => {
      expect(testId).toBe('control-actions')
      return fieldset
    })
    expect(controlActions(page)).toBe(fieldset)
  })
})

describe('waitForControlBanner', () => {
  it('waits for the visible banner and returns that locator', async () => {
    const log: string[] = []
    const banner = assertingLocator('banner', log, () => true)
    expect(await waitForControlBanner(visibleTestIdPage(() => banner))).toBe(banner)
    expect(log).toEqual(['banner:to.be.visible'])
  })

  it('fails while the page shows no banner', async () => {
    const banner = assertingLocator('banner', [], () => false)
    await expect(waitForControlBanner(visibleTestIdPage(() => banner))).rejects.toThrow(/toBeVisible/)
  })
})

describe('expectNoControlBanner', () => {
  // The fake banner has no `filter`, so a scoped read fails the test.
  function bannerPage(present: boolean, log: string[]): Page {
    return opaqueHandle<Page>({ getByTestId: (testId: string | RegExp) => {
      expect(testId).toBe('control-banner')
      return assertingLocator('banner', log, (_expression, options) => present ? options.expectedNumber !== 0 : options.expectedNumber === 0)
    } })
  }

  it('reads the count of every banner, hidden ones included', async () => {
    const log: string[] = []
    await expectNoControlBanner(bannerPage(false, log))
    expect(log).toEqual(['banner:to.have.count=0'])
  })

  it('fails while the page holds a banner', async () => {
    await expect(expectNoControlBanner(bannerPage(true, []))).rejects.toThrow('the page holds no control request banner')
  })
})

describe('answerControl', () => {
  // The fake button has no `first`, so a click that is not strict fails the test.
  it.each(['allow', 'deny'] as const)('clicks the one visible %s button', async (decision) => {
    const click = vi.fn(async () => {})
    const page = visibleTestIdPage((testId) => {
      expect(testId).toBe(`control-${decision}-btn`)
      return opaqueHandle<PlaywrightLocator>({ click })
    })
    await answerControl(page, decision)
    expect(click).toHaveBeenCalledTimes(1)
    expect(click).toHaveBeenCalledWith()
  })
})

describe('answerPlanReview', () => {
  it.each(['approve', 'reject'] as const)('clicks the one visible %s button of the plan review', async (decision) => {
    const click = vi.fn(async () => {})
    const page = visibleTestIdPage((testId) => {
      expect(testId).toBe(`plan-${decision}-btn`)
      return opaqueHandle<PlaywrightLocator>({ click })
    })
    await answerPlanReview(page, decision)
    expect(click).toHaveBeenCalledTimes(1)
    expect(click).toHaveBeenCalledWith()
  })
})

describe('enterControlFeedback', () => {
  it('focuses the visible composer and types the reason into it', async () => {
    const log: string[] = []
    const editor = assertingLocator('editor', log, () => true, { click: async () => {
      log.push('editor:click')
    } })
    const page = opaqueHandle<Page>({
      locator: (selector: string) => {
        expect(selector).toBe('[data-testid="composer-editor"]:visible .ProseMirror')
        return editor
      },
      keyboard: opaqueHandle<Page['keyboard']>({ type: async (text: string) => {
        log.push(`type:${text}`)
      } }),
    })
    await enterControlFeedback(page, 'Keep the spec read-only.')
    expect(log).toEqual(['editor:to.be.visible', 'editor:click', 'type:Keep the spec read-only.'])
  })

  it.each(['', '  \n'])('refuses the reason %j before it touches the page', async (reason) => {
    await expect(enterControlFeedback(opaqueHandle<Page>({}), reason)).rejects.toThrow('A control feedback needs text')
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

/**
 * A fake locator tree. Each locator has a path that states how it was built, and the shared log records each click,
 * hover, and assertion with that path. `answer` decides each assertion and each visibility read from the path.
 */
interface FakeTree {
  log: string[]
  root: Page
}

function fakeTree(answer: (expression: string, path: string) => boolean = () => true): FakeTree {
  const log: string[] = []
  const { page } = fakeLocatorTree({
    log,
    answer,
    attribute: (name, path) => answer(`attribute ${name}`, path) ? 'workspace-item-ws-active' : 'not-a-workspace-row',
    page: {
      goto: async (url: string) => {
        log.push(`goto ${url}`)
      },
      context: () => ({ addCookies: async (cookies: Array<{ name: string, value: string }>) => {
        log.push(`cookies ${cookies.map(cookie => `${cookie.name}=${cookie.value}`).join(', ')}`)
      } }),
    },
  })
  return { log, root: page }
}

describe('sidebarSectionHeader', () => {
  it('selects the first visible header of the section', () => {
    const { root } = fakeTree()
    expect((sidebarSectionHeader(root, 'workers') as unknown as { path: string }).path)
      .toBe('page >> [data-testid="section-header-workers"]:visible.first')
  })
})

describe('expandSidebarSection', () => {
  function section(closed: { value: boolean }, log: string[]): PlaywrightLocator {
    return fakeLocator((check) => {
      log.push(`${check.isNot ? 'not ' : ''}${check.expression}`)
      return closed.value
    }, {
      evaluate: async () => !closed.value,
      locator: (selector: string) => {
        expect(selector).toBe('> [role="button"]')
        return { click: async () => {
          log.push('click header')
          closed.value = false
        } }
      },
    }) as unknown as PlaywrightLocator
  }

  it('opens a closed section and requires that it is open', async () => {
    const log: string[] = []
    const closed = { value: true }
    await expandSidebarSection(section(closed, log))
    expect(log).toEqual(['click header', 'not to.have.attribute'])
  })

  it('leaves an open section untouched, with no click, and still requires that it is open', async () => {
    const log: string[] = []
    await expandSidebarSection(section({ value: false }, log))
    expect(log).toEqual(['not to.have.attribute'])
  })

  it('fails when the section stays closed after the click', async () => {
    const log: string[] = []
    const closed = { value: true }
    const stuck = section(closed, log)
    const original = stuck.locator.bind(stuck)
    stuck.locator = ((selector: string) => ({ click: async () => {
      original(selector)
      log.push('click header without effect')
    } })) as PlaywrightLocator['locator']
    await expect(expandSidebarSection(stuck)).rejects.toThrow('the sidebar section is open')
  })
})

describe('workspace row menu', () => {
  const row = 'page >> [data-testid="workspace-item-ws-1"]:visible.first'

  it('locates an item of the row\'s own menu by its exact name', () => {
    const { root } = fakeTree()
    expect((workspaceMenuItem(root, 'ws-1', 'Delete') as unknown as { path: string }).path)
      .toBe(`${row} >> role=menuitem[name=Delete exact]`)
  })

  it('opens the menu through the trigger test ID of the hovered row, and waits for the required item', async () => {
    // The item is hidden until the trigger click.
    const { root, log } = fakeTree((expression, path) => expression !== 'isVisible' || !path.endsWith('[name=Unarchive exact]'))
    await openWorkspaceRowMenu(root, 'ws-1', 'Unarchive')
    expect(log).toEqual([
      `hover ${row}`,
      `click ${row} >> testid=workspace-row-menu-trigger`,
      `to.be.visible ${row} >> role=menuitem[name=Unarchive exact]`,
    ])
  })

  it('archives through the menu item and the dialog button, and waits for the archived section', async () => {
    const { root, log } = fakeTree((expression, path) => expression !== 'isVisible' || !path.includes('role=menuitem'))
    await archiveWorkspaceViaUI(root, 'ws-1')
    expect(log.filter(entry => !entry.startsWith('hover'))).toEqual([
      `click ${row} >> testid=workspace-row-menu-trigger`,
      `to.be.visible ${row} >> role=menuitem[name=Archive exact]`,
      `click ${row} >> role=menuitem[name=Archive exact]`,
      'click page >> role=dialog[name=Archive workspace] >> role=button[name=Archive exact]',
      'to.be.hidden page >> role=dialog[name=Archive workspace]',
      'to.be.visible page >> [data-testid="section-header-workspaces_archived"]:visible.first',
    ])
  })

  it('deletes through both steps of the confirm button, and waits until the row is gone', async () => {
    const { root, log } = fakeTree((expression, path) => expression !== 'isVisible' || !path.includes('role=menuitem'))
    await deleteWorkspaceViaUI(root, 'ws-1')
    expect(log.filter(entry => !entry.startsWith('hover'))).toEqual([
      `click ${row} >> testid=workspace-row-menu-trigger`,
      `to.be.visible ${row} >> role=menuitem[name=Delete exact]`,
      `click ${row} >> role=menuitem[name=Delete exact]`,
      'click page >> role=dialog[name=Delete workspace] >> role=button[name=Delete exact]',
      'click page >> role=dialog[name=Delete workspace] >> role=button[name=Confirm?]',
      `to.be.hidden ${row}`,
    ])
  })
})

/**
 * A page with the one visible sidebar row of `ws-1`.
 * Its chevron toggles the expanded state, its title selects the workspace, and each action and check goes to `log`.
 */
function workspaceRowPage(state: { expanded: boolean, active: boolean, chevronWorks?: boolean }, log: string[]): Page {
  const rowSelector = '[data-testid="workspace-item-ws-1"]:visible'
  // `toHaveAttribute(name, value)` sends the attribute name as `expressionArg` and the expected value as the first
  // entry of `expectedText`.
  const row: PlaywrightLocator = fakeLocator((check) => {
    log.push(`${check.expression} ${typeof check.expressionArg === 'string' ? check.expressionArg : ''}`.trim())
    const received = String(check.expressionArg === 'data-expanded' ? state.expanded : state.active)
    return { matches: received === check.expectedText?.[0]?.string, received }
  }, {
    first: () => row,
    waitFor: async () => {
      log.push('wait for row')
    },
    getAttribute: async (name: string) => {
      log.push(`read ${name}`)
      if (name === 'data-expanded')
        return state.expanded ? 'true' : 'false'
      if (name === 'data-active')
        return state.active ? 'true' : 'false'
      throw new Error(`The fake row has no attribute ${name}.`)
    },
    locator: (selector: string) => {
      expect(selector).toBe('[data-testid="workspace-chevron-ws-1"]')
      return { click: async () => {
        log.push('click chevron')
        if (state.chevronWorks ?? true)
          state.expanded = !state.expanded
      } }
    },
    getByTestId: (testId: string) => {
      expect(testId).toBe('workspace-title')
      return { click: async () => {
        log.push('click title')
        state.active = true
      } }
    },
    click: async () => {
      throw new Error('A click on the whole row can land on its pinned three-dot trigger.')
    },
  })
  return opaqueHandle<Page>({
    goto: (async (url: string) => {
      log.push(`goto ${url}`)
      return null
    }) as Page['goto'],
    locator: ((selector: string) => {
      if (selector === rowSelector)
        return row
      // The tab strip that `waitForWorkspaceReady` reads.
      expect(selector).toBe('[data-testid="tab"]')
      return { first: () => ({ isVisible: async () => true }) }
    }) as Page['locator'],
    getByRole: ((role: string, options?: { name?: string }) => {
      expect([role, options?.name]).toEqual(['button', 'Toggle workspaces'])
      return { isVisible: async () => false }
    }) as Page['getByRole'],
  })
}

describe('COMPOSER_EDITOR_SELECTOR', () => {
  it('names the editable node of the composer that composerEditor locates, without the visibility scope', () => {
    const selectors: string[] = []
    composerEditor({ locator: (selector: string) => selectors.push(selector) } as unknown as Page)
    expect(selectors).toHaveLength(1)
    expect(COMPOSER_EDITOR_SELECTOR).toBe('[data-testid="composer-editor"] .ProseMirror')
    expect(selectors[0]?.replace(':visible', '')).toBe(COMPOSER_EDITOR_SELECTOR)
  })
})

describe('workspaceRowTitle', () => {
  it('locates the title inside the visible row', () => {
    const { root } = fakeTree()
    expect((workspaceRowTitle(root, 'ws-1') as unknown as { path: string }).path)
      .toBe('page >> [data-testid="workspace-item-ws-1"]:visible.first >> testid=workspace-title')
  })
})

describe('expandWorkspaceRow', () => {
  it('expands a collapsed row through its chevron, and requires the expanded state', async () => {
    const log: string[] = []
    const state = { expanded: false, active: false }
    await expandWorkspaceRow(workspaceRowPage(state, log), 'ws-1')
    expect(log).toEqual(['read data-expanded', 'click chevron', 'to.have.attribute.value data-expanded'])
    expect(state.expanded).toBe(true)
  })

  it('leaves an expanded row expanded, because a chevron click would collapse it', async () => {
    const log: string[] = []
    const state = { expanded: true, active: false }
    await expandWorkspaceRow(workspaceRowPage(state, log), 'ws-1')
    expect(log).toEqual(['read data-expanded', 'to.have.attribute.value data-expanded'])
    expect(state.expanded).toBe(true)
  })

  it('fails with the workspace when the row stays collapsed', async () => {
    const log: string[] = []
    await expect(expandWorkspaceRow(workspaceRowPage({ expanded: false, active: false, chevronWorks: false }, log), 'ws-1'))
      .rejects
      .toThrow('the sidebar row of ws-1 is expanded')
  })
})

describe('collapseWorkspaceRow', () => {
  it('collapses an expanded row through its chevron, and requires the collapsed state', async () => {
    const log: string[] = []
    const state = { expanded: true, active: false }
    await collapseWorkspaceRow(workspaceRowPage(state, log), 'ws-1')
    expect(log).toEqual(['read data-expanded', 'click chevron', 'to.have.attribute.value data-expanded'])
    expect(state.expanded).toBe(false)
  })

  it('leaves a collapsed row collapsed, because a chevron click would expand it', async () => {
    const log: string[] = []
    const state = { expanded: false, active: false }
    await collapseWorkspaceRow(workspaceRowPage(state, log), 'ws-1')
    expect(log).toEqual(['read data-expanded', 'to.have.attribute.value data-expanded'])
    expect(state.expanded).toBe(false)
  })

  it('fails with the workspace when the row stays expanded', async () => {
    const log: string[] = []
    await expect(collapseWorkspaceRow(workspaceRowPage({ expanded: true, active: false, chevronWorks: false }, log), 'ws-1'))
      .rejects
      .toThrow('the sidebar row of ws-1 is collapsed')
  })
})

describe('openWorkspace', () => {
  it('selects an inactive workspace through the title of its row, then waits for the active row and the shell', async () => {
    const log: string[] = []
    const state = { expanded: false, active: false }
    await openWorkspace(workspaceRowPage(state, log), 'ws-1')
    expect(log).toEqual(['goto /', 'wait for row', 'read data-active', 'click title', 'to.have.attribute.value data-active'])
    expect(state.active).toBe(true)
  })

  it('clicks nothing for a workspace that the load already made active', async () => {
    const log: string[] = []
    await openWorkspace(workspaceRowPage({ expanded: false, active: true }, log), 'ws-1')
    expect(log).toEqual(['goto /', 'wait for row', 'read data-active', 'to.have.attribute.value data-active'])
  })
})

describe('sidebar subtree locators', () => {
  const row = 'page >> [data-testid="workspace-item-ws-1"]:visible.first'

  it('reaches the children wrapper as the next sibling of the visible row, and requires its test ID', () => {
    const { root } = fakeTree()
    expect((workspaceChildren(root, 'ws-1') as unknown as { path: string }).path)
      .toBe(`${row} >> xpath=following-sibling::*[1][@data-testid="workspace-children-ws-1"]`)
  })

  it('finds the leaves inside that wrapper', () => {
    const { root } = fakeTree()
    expect((sidebarLeaves(root, 'ws-1') as unknown as { path: string }).path)
      .toBe(`${row} >> xpath=following-sibling::*[1][@data-testid="workspace-children-ws-1"] >> [data-testid="tab-tree-leaf"]`)
  })

  it('scopes a branch group row below the root that the caller passes', () => {
    const { root } = fakeTree()
    const subtree = workspaceChildren(root, 'ws-1')
    expect((branchGroupRow(subtree) as unknown as { path: string }).path)
      .toBe(`${row} >> xpath=following-sibling::*[1][@data-testid="workspace-children-ws-1"] >> [data-testid="tab-tree-branch-group"]:visible.first`)
    expect((branchGroupRow(root) as unknown as { path: string }).path)
      .toBe('page >> [data-testid="tab-tree-branch-group"]:visible.first')
  })
})

describe('activeWorkspaceId', () => {
  it('reads the workspace ID from the test ID of the visible active row', async () => {
    const { root, log } = fakeTree()
    await expect(activeWorkspaceId(root)).resolves.toBe('ws-active')
    expect(log).toEqual([
      'to.be.visible page >> [data-testid^="workspace-item-"][data-active="true"]:visible.first',
      'read data-testid page >> [data-testid^="workspace-item-"][data-active="true"]:visible.first',
    ])
  })

  it('refuses a row whose test ID is not a workspace row ID', async () => {
    const { root } = fakeTree(expression => !expression.startsWith('attribute'))
    await expect(activeWorkspaceId(root)).rejects.toThrow('unexpected test ID: not-a-workspace-row')
  })
})

describe('treeRow', () => {
  const rows = 'page >> [data-testid="tree-row"]:visible'

  it('matches the name as a substring of the row text by default', () => {
    const { root } = fakeTree()
    expect((treeRow(root, 'src') as unknown as { path: string }).path).toBe(`${rows}[hasText=src].first`)
  })

  it('matches the row label exactly when asked, so a longer name that contains it stays out', () => {
    const { root } = fakeTree()
    expect((treeRow(root, 'src', { exact: true }) as unknown as { path: string }).path)
      .toBe(`${rows}[has=(page >> testid=tree-row-name >> text=src exact)].first`)
  })
})

describe('waitForAgentStarted', () => {
  const composer = 'page >> [data-testid="composer-editor"]:visible .ProseMirror'
  const overlay = 'page >> testid=agent-startup-overlay[visible=true]'
  const failure = 'page >> testid=agent-startup-error[visible=true]'

  it('requires the visible composer, then no visible startup overlay, then no visible startup error', async () => {
    const { root, log } = fakeTree()
    await waitForAgentStarted(root)
    expect(log).toEqual([`to.be.visible ${composer}`, `to.have.count ${overlay}`, `to.have.count ${failure}`])
  })

  it('fails while the startup overlay shows, and reads no startup error', async () => {
    const { root, log } = fakeTree((_expression, path) => path !== overlay)
    await expect(waitForAgentStarted(root)).rejects.toThrow('the agent ends its startup')
    expect(log).not.toContain(`to.have.count ${failure}`)
  })

  it('fails when the startup failed', async () => {
    const { root } = fakeTree((_expression, path) => path !== failure)
    await expect(waitForAgentStarted(root)).rejects.toThrow('the agent starts with no error')
  })

  it('fails before it reads the overlay when no composer shows', async () => {
    const { root, log } = fakeTree((_expression, path) => path !== composer)
    await expect(waitForAgentStarted(root)).rejects.toThrow('the agent tab shows its composer')
    expect(log).toEqual([`to.be.visible ${composer}`])
  })
})

describe('openTerminalViaUI', () => {
  /** A page whose terminal tabs are `t-1` before the click and `idsAfterClick` after it. */
  function terminalPage(log: string[], idsAfterClick: string[], xtermShows = true): Page {
    let clicked = false
    return opaqueHandle<Page>({
      locator: (selector: string) => {
        if (selector === '[data-working-dir]:not([data-working-dir=""])') {
          return opaqueHandle<PlaywrightLocator>({ first: () => opaqueHandle<PlaywrightLocator>({ waitFor: async () => {
            log.push('directory known')
          } }) })
        }
        if (selector === '[data-testid="tab"][data-tab-type="terminal"]')
          return opaqueHandle<PlaywrightLocator>({ evaluateAll: (async () => clicked ? idsAfterClick : ['t-1']) as unknown as PlaywrightLocator['evaluateAll'] })
        if (selector === '[data-testid="new-terminal-button"]') {
          return opaqueHandle<PlaywrightLocator>({ click: async () => {
            log.push('click')
            clicked = true
          } })
        }
        log.push(`xterm of ${selector}`)
        return assertingLocator('xterm', log, () => xtermShows)
      },
    })
  }

  it('clicks once the directory is known, and waits for the xterm of the terminal that the click added', async () => {
    const log: string[] = []
    await expect(openTerminalViaUI(terminalPage(log, ['t-1', 't-2']))).resolves.toBe('t-2')
    expect(log).toEqual(['directory known', 'click', 'xterm of [data-terminal-id="t-2"] .xterm', 'xterm:to.be.visible'])
  })

  it('finds the new terminal by ID, wherever the tab bar places it', async () => {
    await expect(openTerminalViaUI(terminalPage([], ['t-0', 't-1']))).resolves.toBe('t-0')
  })

  it('fails when the new terminal shows no xterm', async () => {
    await expect(openTerminalViaUI(terminalPage([], ['t-1', 't-2'], false))).rejects.toThrow('the new terminal shows its xterm')
  })
})

describe('platformModifier', () => {
  it('uses Meta on macOS', () => {
    expect(platformModifier('darwin')).toBe('Meta')
  })

  it.each(['linux', 'win32', 'freebsd'] as const)('uses Control on %s', (platform) => {
    expect(platformModifier(platform)).toBe('Control')
  })
})

describe('submitLoginForm', () => {
  it('fills both credentials, solves the captcha, and submits once', async () => {
    const { root, log } = fakeTree()
    await submitLoginForm(root, 'alice', 'secret')
    expect(log).toEqual([
      'fill page >> label=Username with alice',
      'fill page >> label=Password with secret',
      'click page >> role=button[name=Sign in]',
    ])
    expect(solveCaptchaViaUI).toHaveBeenCalledWith(root)
  })
})

describe('openAppAs', () => {
  const shell = '(page >> testid=app-menu-trigger.first | page >> testid=collapsed-new-tab-button).first'

  it('sets the session cookie before the load, and requires the authenticated shell', async () => {
    const { root, log } = fakeTree()
    await openAppAs(root, 'leapmux-session=abc=def')
    expect(log).toEqual(['cookies leapmux-session=abc=def', 'goto /', `to.be.visible ${shell}`])
  })

  it('fails when the app does not accept the session', async () => {
    const { root } = fakeTree((_expression, path) => path !== shell)
    await expect(openAppAs(root, 'leapmux-session=expired')).rejects.toThrow('the app accepts the session')
  })
})

describe('elevationPrompt', () => {
  it('selects the step-up dialog by its name', () => {
    const { root } = fakeTree()
    expect((elevationPrompt(root) as unknown as { path: string }).path).toBe('page >> role=dialog[name=Verify your identity]')
  })
})

describe('expectDialogStaysOpen', () => {
  /** A CSS transition as the browser reports it, for the `instanceof` check of the helper. */
  class FakeCssTransition {
    constructor(readonly transitionProperty: string) {}
  }

  /** A dialog whose visibility, computed opacity, and running animations the test states. */
  function dialog(state: { visible: boolean, opacity: string, animations: object[] }) {
    vi.stubGlobal('CSSTransition', FakeCssTransition)
    vi.stubGlobal('getComputedStyle', () => ({ opacity: state.opacity }))
    const element = { getAnimations: () => state.animations }
    return fakeLocator(() => state.visible, {
      evaluate: async <R>(read: (element: Element) => R) => read(element as unknown as Element),
    })
  }

  it('accepts an open dialog that runs no animation', async () => {
    await expect(expectDialogStaysOpen(dialog({ visible: true, opacity: '1', animations: [] }), 'the dialog stays')).resolves.toBeUndefined()
  })

  it('refuses a dialog whose closing transition runs, although it is still visible', async () => {
    const closing = dialog({ visible: true, opacity: '1', animations: [new FakeCssTransition('opacity')] })
    await expect(expectDialogStaysOpen(closing, 'the dialog stays')).rejects.toThrow('transition of opacity')
  })

  it('refuses a dialog that faded out but is not unmounted yet', async () => {
    await expect(expectDialogStaysOpen(dialog({ visible: true, opacity: '0', animations: [] }), 'the dialog stays')).rejects.toThrow('the dialog stays')
  })

  it('names another animation by its kind', async () => {
    class CSSAnimation {}
    const animated = dialog({ visible: true, opacity: '1', animations: [new CSSAnimation()] })
    await expect(expectDialogStaysOpen(animated, 'the dialog stays')).rejects.toThrow('CSSAnimation')
  })

  it('refuses a dialog that is gone, before it reads the closing state', async () => {
    const evaluate = vi.fn()
    const gone = fakeLocator(() => false, { evaluate })
    await expect(expectDialogStaysOpen(gone, 'the dialog stays')).rejects.toThrow('the dialog stays')
    expect(evaluate).not.toHaveBeenCalled()
  })
})

describe('answerElevationPrompt', () => {
  const prompt = 'page >> role=dialog[name=Verify your identity]'

  it('requires the prompt, enters the password, submits, and returns the prompt', async () => {
    const { root, log } = fakeTree()
    const answered = await answerElevationPrompt(root, 'secret')
    expect((answered as unknown as { path: string }).path).toBe(prompt)
    expect(log).toEqual([
      `to.be.visible ${prompt}`,
      `fill ${prompt} >> testid=elevate-password with secret`,
      `click ${prompt} >> testid=elevate-password-submit`,
    ])
  })

  it('answers the standalone page when the caller passes its card', async () => {
    const { root, log } = fakeTree()
    const card = root.getByTestId('elevate-card')
    expect(await answerElevationPrompt(root, 'secret', card)).toBe(card)
    expect(log).toEqual([
      'to.be.visible page >> testid=elevate-card',
      'fill page >> testid=elevate-card >> testid=elevate-password with secret',
      'click page >> testid=elevate-card >> testid=elevate-password-submit',
    ])
  })

  it('types nothing when the hub asked for no factor', async () => {
    const { root, log } = fakeTree((_expression, path) => path !== prompt)
    await expect(answerElevationPrompt(root, 'secret')).rejects.toThrow('the hub asks the session to prove a factor')
    expect(log).toEqual([`to.be.visible ${prompt}`])
  })
})
