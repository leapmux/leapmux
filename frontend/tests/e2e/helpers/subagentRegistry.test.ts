/**
 * The unit tests check the source rules for shared registry locators.
 * claude-code/background-tasks-sidebar.spec.ts checks actual Worker state before hydration.
 * ./goalsAndTodos.test.ts checks the locators of the Goals & To-dos section.
 *
 * The .test.ts extension selects Vitest. The .spec.ts extension selects Playwright.
 * Both runner configurations and testFileNaming.test.ts enforce that distinction.
 */
import type { Locator, Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import type { HeldChildCase, HeldChildContext } from './subagentRegistry'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { exportedFunctionBody, selectorsIn } from '~/test-support/locatorSource'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ohMyPiYieldToolCall, spawnSubagentToolCall } from './providerToolCalls'
import { exerciseHeldChildRow, HELD_CHILD_NAME, HELD_CHILD_REPORT, HELD_CHILD_TASK, HELD_CHILD_TITLE, heldChildAnswer, openChildTabFromRow, openHeldChildTab, requireRegistryRow } from './subagentRegistry'
import { startWaitLimitForTests } from './testDeadline'

/** The end of the wait limit that a failure case starts, so that a wait which never passes ends inside the case. */
let endWaitLimit: (() => void) | undefined

afterEach(() => {
  endWaitLimit?.()
  endWaitLimit = undefined
})

/** The Worker registry that the fake snapshot read returns, and the order of the reads and model script calls. */
const registry = vi.hoisted(() => ({ tasks: [] as unknown[], log: [] as string[], rules: [] as unknown[], steps: [] as unknown[] }))

vi.mock('./nativeSidebarSnapshot', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeSidebarSnapshot')>(),
  readNativeSidebarSnapshot: async () => {
    registry.log.push('registry')
    return { backgroundTasks: registry.tasks }
  },
}))

const source = readFileSync(join(import.meta.dirname, 'subagentRegistry.ts'), 'utf-8')

/** The source of one exported helper, from its signature to its closing brace. */
function bodyOf(name: string): string {
  return exportedFunctionBody(source, name, 'subagentRegistry.ts')
}

/**
 * Check the visible scope for shared locators.
 * ChatView can render a hidden premeasure copy of an unmeasured row.
 * The sidebar also has desktop and mobile mounts.
 * A bare test ID can match both copies and fail strict mode.
 * An unscoped first() can select a hidden copy and inspect state that the user cannot see.
 *
 * Present-element locators require :visible.
 * Sidebar section locators select the first visible mount that receives Worker metadata.
 * Absence locators require every copy to be absent, including hidden rows.
 * ABSENCE_HELPERS identifies that separate rule.
 * Source checks detect these defects before a slow browser spec times out.
 */
describe('registry locators', () => {
  it.each(['backgroundTasksSection'])('selects the first visible sidebar mount in %s', (name) => {
    const body = bodyOf(name)
    expect(selectorsIn(body)).toHaveLength(1)
    expect(body).toMatch(/return page\.locator\([^\n]*:visible[^\n]*\)\.first\(\)/)
  })
  /** Test IDs for surfaces that the app mounts more than once. */
  const DUPLICATED = ['bg-task-', 'section-header-']

  /**
   * A zero-count assertion requires both mounts to contain no rows.
   * A :visible scope could hide rows in a collapsed section and falsely report zero.
   */
  const ABSENCE_HELPERS = ['expectNoRegistryRows']

  it('scopes every duplicated-surface locator to :visible', () => {
    const locators = selectorsIn(
      ABSENCE_HELPERS.reduce((rest, name) => rest.replace(bodyOf(name), ''), source),
    )
    // Require selectors so a parser or helper change cannot pass through an empty scan.
    expect(locators.length).toBeGreaterThan(5)

    const offenders = locators.filter(selector =>
      DUPLICATED.some(id => selector.includes(`data-testid="${id}`)) && !selector.includes(':visible'),
    )
    expect(offenders).toEqual([])
  })

  it('leaves an absence assertion unscoped, so a collapsed section cannot pass it', () => {
    for (const name of ABSENCE_HELPERS) {
      const locators = selectorsIn(bodyOf(name))
      expect(locators.length, `${name} should build at least one locator`).toBeGreaterThan(0)
      expect(locators.filter(selector => selector.includes(':visible'))).toEqual([])
    }
  })
})

/**
 * CI runs no E2E. A provider whose registry row shows a subagent name takes that
 * name from its spawn call, so a change to the name that a builder derives fails
 * here, before a browser spec waits on a row title that never appears.
 */
describe('HELD_CHILD_NAME', () => {
  const request = { description: HELD_CHILD_TITLE, prompt: `${HELD_CHILD_TASK}.` }

  it('is the task_name that the Codex spawn call derives from the held child description', () => {
    expect(spawnSubagentToolCall(AgentProvider.CODEX, 'spawn-held-child', request).arguments).toMatchObject({ task_name: HELD_CHILD_NAME })
  })

  it('is the task name of the Oh My Pi task call, which omp takes as the subagent ID', () => {
    expect(spawnSubagentToolCall(AgentProvider.OH_MY_PI, 'spawn-held-child', request).arguments).toMatchObject({ tasks: [{ name: HELD_CHILD_NAME }] })
  })

  it('is the session name of the Codewhale agent start call, whose arguments hold no description', () => {
    const args = spawnSubagentToolCall(AgentProvider.CODEWHALE, 'spawn-held-child', request).arguments
    expect(args).toMatchObject({ action: 'start', name: HELD_CHILD_NAME })
    expect(JSON.stringify(args)).not.toContain(HELD_CHILD_TITLE)
  })

  it('keeps the name, the description, and the task distinct, so a row title assertion identifies the field that the row shows', () => {
    expect(HELD_CHILD_TITLE).not.toContain(HELD_CHILD_NAME)
    expect(HELD_CHILD_TASK).not.toContain(HELD_CHILD_NAME)
    expect(HELD_CHILD_TASK).not.toContain(HELD_CHILD_TITLE)
  })
})

/**
 * CI runs no E2E. The held child's answer decides whether a provider nudges its
 * child into extra turns after the hold releases, and the composition that pins
 * the hold gate onto a provider's own answer is checked here.
 */
describe('heldChildAnswer', () => {
  const base: Pick<HeldChildCase, 'heldAnswer'> = {}

  it('answers with the shared text under the hold gate by default', () => {
    const answer = heldChildAnswer(base)
    expect(answer.text).toBe('One, two, three.')
    expect(answer.gate).toBeTruthy()
  })

  it('keeps the provider answer and applies the hold gate to it', () => {
    const answer = heldChildAnswer({ ...base, heldAnswer: { toolCalls: [ohMyPiYieldToolCall('held-child-yield', HELD_CHILD_REPORT)] } })
    expect(answer.toolCalls).toEqual([{ id: 'held-child-yield', name: 'yield', arguments: { data: HELD_CHILD_REPORT } }])
    expect(answer.gate).toBe(heldChildAnswer(base).gate)
  })

  it('overrides a gate that the provider answer carries, so the hold cannot be dropped', () => {
    const answer = heldChildAnswer({ ...base, heldAnswer: { text: 'Done.', gate: 'caller-gate' } })
    expect(answer.text).toBe('Done.')
    expect(answer.gate).toBe(heldChildAnswer(base).gate)
  })
})

interface NavigationState {
  ids: string[]
  selectedId: string
  childId: string | null
}

/**
 * Narrow adapters test the real navigation helper without a browser-install dependency.
 * The adapters provide only the methods that the helper calls.
 * Any new unsupported method fails the test rather than supplying a default result.
 */
function navigation(state: NavigationState, change: () => void) {
  const locator = (selector: string): Locator => {
    const selected = /\[data-tab-id="([^"\]]+)"\]/.exec(selector)?.[1]
    const ids = () => selected === undefined ? state.ids : state.ids.filter(id => id === selected)
    const attributes = (name: string) => name === 'data-tab-id'
      ? selected ?? ids()[0] ?? null
      : name === 'aria-selected' ? String(state.selectedId === (selected ?? ids()[0])) : null
    const probe = Object.assign({} as Locator, {
      readCount: () => ids().length,
      readAttribute: attributes,
      count: async () => ids().length,
      getAttribute: async (name: string) => attributes(name),
      evaluateAll: async (read: (elements: Element[]) => unknown) => read(ids().map((id) => {
        return Object.assign({} as Element, { getAttribute: (name: string) => name === 'data-tab-id' ? id : null })
      })),
      filter: () => probe,
      first: () => probe,
      isVisible: async () => ids().length > 0,
    })
    return probe
  }
  const page = Object.assign({} as Page, { locator })
  const click = vi.fn(async () => change())
  const row = Object.assign({} as Locator, { getAttribute: async () => state.childId, click })
  return { page, row, click }
}

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown, message?: string) => {
    if (typeof value === 'object' && value !== null && 'readCount' in value && typeof value.readCount === 'function'
      && 'readAttribute' in value && typeof value.readAttribute === 'function') {
      const count = value.readCount
      const attribute = value.readAttribute
      return {
        toHaveCount: async (expected: number) => expect(count(), message).toBe(expected),
        toBeVisible: async () => expect(count(), message).toBeGreaterThan(0),
        toHaveAttribute: async (key: string, expected: string) => expect(attribute(key), message).toBe(expected),
        // The negated form without a value, as `expandSidebarSection` checks that the section carries no `data-closed`.
        not: { toHaveAttribute: async (key: string) => expect(attribute(key), message).toBeNull() },
      }
    }
    return expect(value, message)
  }
  // A fake poll reads once. A read that throws rejects the assertion with that error, as the last attempt of a real poll does.
  return { ...actual, expect: Object.assign(check, {
    poll: (read: () => Promise<unknown>, options?: { message?: string }) => ({
      toBe: async (expected: unknown) => expect(await read(), options?.message).toBe(expected),
      toMatch: async (expected: RegExp) => expect(await read(), options?.message).toMatch(expected),
      not: { toBe: async (expected: unknown) => expect(await read(), options?.message).not.toBe(expected) },
    }),
  }) }
})

describe('requireRegistryRow', () => {
  /** A page whose every locator is one probe row. The probe also stands for the open section header. */
  function registry(isVisible: () => Promise<boolean>) {
    const selectors: string[] = []
    const row: Locator = Object.assign({} as Locator, {
      first: () => row,
      isVisible,
      // `expandSidebarSection` reads the header. An open header needs no click, and it carries no `data-closed`,
      // which the fake `expect` below reads to confirm that the section is open.
      evaluate: async () => true,
      readCount: () => 1,
      readAttribute: () => null,
    })
    const page = Object.assign({} as Page, {
      locator: (selector: string) => {
        selectors.push(selector)
        return row
      },
    })
    return { page, row, selectors }
  }

  it('returns the first visible row of the requested kind', async () => {
    const view = registry(async () => true)
    expect(await requireRegistryRow(view.page, 'shell')).toBe(view.row)
    expect(view.selectors).toContain('[data-testid="bg-task-row"]:visible[data-kind="shell"]')
  })

  it('states the missing subagent row in its failure', async () => {
    endWaitLimit = startWaitLimitForTests(300)
    const view = registry(async () => false)
    await expect(requireRegistryRow(view.page)).rejects.toThrow('the scripted spawn produced no subagent row in the registry')
  })

  it('states the missing shell row in its failure', async () => {
    endWaitLimit = startWaitLimitForTests(300)
    const view = registry(async () => false)
    await expect(requireRegistryRow(view.page, 'shell')).rejects.toThrow('the scripted command produced no shell row in the registry')
  })

  it('reads again after a row read that throws, and returns the row that then shows', async () => {
    const reads = [async () => {
      throw new Error('Element is not attached to the DOM')
    }, async () => false, async () => true]
    const view = registry(async () => (reads.shift() ?? (async () => true))())
    expect(await requireRegistryRow(view.page)).toBe(view.row)
    expect(reads).toEqual([])
  })

  it('keeps the error of a row read that always fails as the error and the cause of its failure', async () => {
    endWaitLimit = startWaitLimitForTests(300)
    const closed = new Error('Target page, context or browser has been closed')
    const view = registry(async () => {
      throw closed
    })
    const failure = await requireRegistryRow(view.page).catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(Error)
    expect((failure as Error).message.split('\n')[0]).toBe(closed.message)
    expect((failure as Error).cause).toBe(closed)
  })
})

/** A locator probe that the fake `expect` reads, with a count and attributes. */
function probe(count: number, attributes: Record<string, string> = {}): Locator {
  const locator: Locator = Object.assign({} as Locator, {
    readCount: () => count,
    readAttribute: (name: string) => attributes[name] ?? null,
    first: () => locator,
    getAttribute: async (name: string) => attributes[name] ?? null,
  })
  return locator
}

/**
 * A context with one root agent tab and an empty registry in the DOM.
 * The model script logs each call and keeps each rule. Its queue fails, which ends the setup after the spawn is
 * scripted.
 */
function context(): HeldChildContext {
  const page = Object.assign({} as Page, {
    locator: (selector: string) => {
      if (selector.includes('[data-tab-type="agent"]'))
        return probe(1, { 'data-tab-id': 'root-agent' })
      if (selector.startsWith('[data-testid="bg-task-'))
        return probe(0)
      throw new Error(`The fake page has no locator for ${selector}.`)
    },
  })
  const modelScript = Object.assign({} as ModelScript, {
    prompt: (text: string) => text,
    rule: async (...rules: unknown[]) => {
      registry.log.push('rule')
      registry.rules.push(...rules)
    },
    queue: async (...steps: unknown[]) => {
      registry.log.push('queue')
      registry.steps.push(...steps)
      throw new Error('The fake model script stops after the queue.')
    },
    releaseGateIfHeld: async () => {
      registry.log.push('release')
      return false
    },
  })
  return { page, modelScript, provider: AgentProvider.KIRO, leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' } }
}

describe('openHeldChildTab', () => {
  beforeEach(() => {
    registry.tasks = []
    registry.log = []
  })

  const heldCase: HeldChildCase = { childTurn: { user: HELD_CHILD_TASK }, rootTurnsAfterSpawn: [{ text: 'The root completed.' }] }

  it('reads the Worker registry before it scripts the spawn, and releases the hold when the setup fails', async () => {
    await expect(openHeldChildTab(context(), heldCase)).rejects.toThrow('stops after the queue')
    expect(registry.log).toEqual(['registry', 'rule', 'queue', 'release'])
  })

  it('refuses a Worker registry that holds a row before it scripts the spawn', async () => {
    registry.tasks = [{ id: 'earlier-task', childAgentId: 'earlier-child' }]
    await expect(openHeldChildTab(context(), heldCase)).rejects.toThrow('the Worker registry must hold no task rows before a spawn')
    expect(registry.log).toEqual(['registry'])
  })
})

describe('exerciseHeldChildRow', () => {
  beforeEach(() => {
    registry.tasks = []
    registry.log = []
    registry.rules = []
    registry.steps = []
  })

  it('holds the child turn of the shared task, answers the root once after the spawn, and releases the hold on a failure', async () => {
    await expect(exerciseHeldChildRow(context())).rejects.toThrow('stops after the queue')
    expect(registry.log).toEqual(['registry', 'rule', 'queue', 'release'])
    expect(registry.rules).toEqual([expect.objectContaining({ when: { user: HELD_CHILD_TASK }, respond: heldChildAnswer({}) })])
    expect(registry.steps.slice(1)).toEqual([{ text: 'The actual native child completed.' }])
  })

  it('holds the answer that the provider gives in place of text', async () => {
    const heldAnswer = { toolCalls: [ohMyPiYieldToolCall('held-child-yield', 'Counted.')] }
    await expect(exerciseHeldChildRow(context(), { rowTitle: HELD_CHILD_NAME, heldAnswer })).rejects.toThrow('stops after the queue')
    expect(registry.rules).toEqual([expect.objectContaining({ respond: heldChildAnswer({ heldAnswer }) })])
  })
})

describe('openChildTabFromRow', () => {
  it('opens the exact absent child and preserves the added-tab check', async () => {
    const state: NavigationState = { ids: ['parent'], selectedId: 'parent', childId: 'actual-child' }
    const view = navigation(state, () => {
      state.ids.push('actual-child')
      state.selectedId = 'actual-child'
    })
    expect(await openChildTabFromRow(view.page, view.row)).toBe('actual-child')
    expect(view.click).toHaveBeenCalledTimes(1)
    expect(state.ids).toEqual(['parent', 'actual-child'])
    expect(state.selectedId).toBe('actual-child')
  })

  it('selects the existing native child without requiring another tab', async () => {
    const state: NavigationState = { ids: ['parent', 'actual-child'], selectedId: 'parent', childId: 'actual-child' }
    const view = navigation(state, () => state.selectedId = 'actual-child')
    expect(await openChildTabFromRow(view.page, view.row)).toBe('actual-child')
    expect(state.ids).toEqual(['parent', 'actual-child'])
    expect(state.selectedId).toBe('actual-child')
  })

  it('keeps an already selected child on a repeated row click', async () => {
    const state: NavigationState = { ids: ['parent', 'actual-child'], selectedId: 'actual-child', childId: 'actual-child' }
    const view = navigation(state, () => {})
    expect(await openChildTabFromRow(view.page, view.row)).toBe('actual-child')
    expect(view.click).toHaveBeenCalledTimes(1)
    expect(state.ids).toEqual(['parent', 'actual-child'])
  })

  it.each([null, '', '   '])('refuses an absent native child ID before clicking: %j', async (childId) => {
    const state: NavigationState = { ids: ['parent'], selectedId: 'parent', childId }
    const view = navigation(state, () => {
      state.ids.push('unrelated-child')
      state.selectedId = 'unrelated-child'
    })
    await expect(openChildTabFromRow(view.page, view.row)).rejects.toThrow()
    expect(view.click).not.toHaveBeenCalled()
  })

  it('refuses an unrelated new tab even when the count increases correctly', async () => {
    const state: NavigationState = { ids: ['parent'], selectedId: 'parent', childId: 'actual-child' }
    const view = navigation(state, () => {
      state.ids.push('other-child')
      state.selectedId = 'other-child'
    })
    await expect(openChildTabFromRow(view.page, view.row)).rejects.toThrow()
  })

  it('requires the exact opened child to become selected', async () => {
    const state: NavigationState = { ids: ['parent'], selectedId: 'parent', childId: 'actual-child' }
    const view = navigation(state, () => state.ids.push('actual-child'))
    await expect(openChildTabFromRow(view.page, view.row)).rejects.toThrow()
  })

  it('refuses duplicate rendered tab IDs before clicking', async () => {
    const state: NavigationState = { ids: ['parent', 'parent'], selectedId: 'parent', childId: 'actual-child' }
    const view = navigation(state, () => state.ids.push('actual-child'))
    await expect(openChildTabFromRow(view.page, view.row)).rejects.toThrow()
    expect(view.click).not.toHaveBeenCalled()
  })
})
