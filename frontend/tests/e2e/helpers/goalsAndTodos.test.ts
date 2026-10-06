/**
 * The unit tests check the source rules for the Goals & To-dos locators, the order of the composite goal steps,
 * and the goal transition count.
 * The session-goal browser specs of each provider check the actual goal card.
 *
 * The .test.ts extension selects Vitest. The .spec.ts extension selects Playwright.
 * Both runner configurations and testFileNaming.test.ts enforce that distinction.
 */
import type { Page } from '@playwright/test'
import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { exportedFunctionBody, selectorsIn } from '~/test-support/locatorSource'
import { ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clearGoal, countGoalTransitions, expectGoalObjective, nativeGoalProbeTurn, pauseResumeClearGoal, scriptedObjective, setGoal, submitGoal } from './goalsAndTodos'
import { SCENARIO_MARKER } from './mockModelScript'

const source = readFileSync(join(import.meta.dirname, 'goalsAndTodos.ts'), 'utf-8')

/** The source of one exported helper, from its signature to its closing brace. */
function bodyOf(name: string): string {
  return exportedFunctionBody(source, name, 'goalsAndTodos.ts')
}

/**
 * The sidebar mounts twice, and both mounts can be visible.
 * A bare test ID can match both mounts and fail strict mode, and an unscoped first() can select a hidden mount.
 * Source checks detect these defects before a slow browser spec times out.
 */
describe('goals and to-dos locators', () => {
  it.each(['goalsAndTodosSection', 'goalsAndTodosList'])('selects the first visible sidebar mount in %s', (name) => {
    const body = bodyOf(name)
    expect(selectorsIn(body)).toHaveLength(1)
    expect(body).toMatch(/return page\.locator\([^\n]*:visible[^\n]*\)\.first\(\)/)
  })

  it('scopes every locator of a goal surface or of the section to :visible', () => {
    const locators = selectorsIn(source)
    // Require selectors so a parser or helper change cannot pass through an empty scan.
    expect(locators.length).toBeGreaterThan(10)
    const duplicated = ['goal-', 'section-header-', 'goals-and-todos', 'set-goal-']
    const offenders = locators.filter(selector =>
      duplicated.some(id => selector.includes(`data-testid="${id}`)) && !selector.includes(':visible'),
    )
    expect(offenders).toEqual([])
  })
})

describe('scriptedObjective', () => {
  const script = { id: 'goal-unit-script', prompt: (text: string) => `${text}\n\n${SCENARIO_MARKER}goal-unit-script` }

  it('types the prompt of the script and keeps the text and the marker apart', () => {
    const objective = scriptedObjective(script, 'Keep the build green.')
    expect(objective).toEqual({
      input: `Keep the build green.\n\n${SCENARIO_MARKER}goal-unit-script`,
      text: 'Keep the build green.',
      marker: `${SCENARIO_MARKER}goal-unit-script`,
    })
    expect(objective.input.endsWith(objective.marker)).toBe(true)
  })

  it.each(['', ' ', '\n'])('refuses an objective with no text: %j', (text) => {
    expect(() => scriptedObjective(script, text)).toThrow('needs text')
  })
})

/** The browser actions and checks that the fakes record, in order. */
const recorded = vi.hoisted(() => ({ events: [] as string[], status: 'none' }))

vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  expandSidebarSection: async () => {
    recorded.events.push('expand')
  },
  stableBox: async () => ({ x: 0, y: 0, width: 1, height: 1 }),
}))

const reads = vi.hoisted(() => ({ messages: vi.fn<() => Promise<AgentChatMessage[]>>() }))
vi.mock('./nativeMessages', () => ({ readAllAgentMessages: reads.messages }))

const answers = vi.hoisted(() => ({ send: vi.fn() }))
vi.mock('./nativeConversation', () => ({ sendNativeAnswer: answers.send }))

/** A fake locator records its actions. A click on a goal action changes the status that the status dot reports. */
function fakeLocator(selector: string) {
  const locator = {
    selector,
    first: () => locator,
    click: async () => {
      recorded.events.push(`click ${selector}`)
      const action = /goal-action-(\w+)/.exec(selector)?.[1]
      if (selector.includes('set-goal-submit') || action === 'resume')
        recorded.status = 'active'
      else if (action === 'pause')
        recorded.status = 'paused'
      else if (action === 'clear')
        recorded.status = 'none'
    },
    fill: async (text: string) => {
      recorded.events.push(`fill ${selector} ${text}`)
    },
    getAttribute: async () => recorded.status,
  }
  return locator
}

function fakePage(): Page {
  return Object.assign({} as Page, {
    locator: (selector: string) => fakeLocator(selector),
    reload: async () => {
      recorded.events.push('reload')
      return null
    },
  })
}

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: { selector: string }) => ({
    toBeVisible: async () => {
      recorded.events.push(`visible ${value.selector}`)
    },
    toContainText: async (text: string) => {
      recorded.events.push(`text ${text}`)
    },
  })
  return {
    ...actual,
    expect: Object.assign(check, {
      poll: (read: () => Promise<unknown>) => ({
        toBe: async (expected: unknown) => {
          expect(await read()).toBe(expected)
          recorded.events.push(`status ${String(expected)}`)
        },
      }),
    }),
  }
})

const EDITOR = '[data-testid="goal-editor"]:visible .ProseMirror'
const SUBMIT = 'click [data-testid="set-goal-submit"]:visible'
const MENU = 'click [data-testid="goal-actions-trigger"]:visible'
const action = (name: string) => `click [data-testid="goal-action-${name}"]:visible`

beforeEach(() => {
  recorded.events.length = 0
  recorded.status = 'none'
  reads.messages.mockReset()
})

describe('submitGoal', () => {
  it('expands the section, opens the editor from the set action, fills it, and submits', async () => {
    await submitGoal(fakePage(), 'Keep the build green.')
    expect(recorded.events).toEqual(['expand', action('set'), `fill ${EDITOR} Keep the build green.`, SUBMIT])
  })
})

describe('clearGoal', () => {
  it('clears through the goal menu', async () => {
    await clearGoal(fakePage())
    expect(recorded.events).toEqual([MENU, action('clear')])
  })
})

describe('expectGoalObjective', () => {
  it('requires the text of a plain objective', async () => {
    await expectGoalObjective(fakePage(), 'Keep the build green.')
    expect(recorded.events).toEqual(['text Keep the build green.'])
  })

  it('requires the text and the scenario marker of a scripted objective', async () => {
    await expectGoalObjective(fakePage(), { input: 'unused', text: 'Keep the build green.', marker: `${SCENARIO_MARKER}id` })
    expect(recorded.events).toEqual(['text Keep the build green.', `text ${SCENARIO_MARKER}id`])
  })
})

describe('setGoal', () => {
  const objective = { input: 'Keep the build green.\n\nMARKER', text: 'Keep the build green.', marker: 'MARKER' }

  it('runs the after-submit step before it checks the objective and the active status', async () => {
    await setGoal(fakePage(), objective, async () => {
      recorded.events.push('after submit')
    })
    expect(recorded.events).toEqual([
      'expand',
      action('set'),
      `fill ${EDITOR} ${objective.input}`,
      SUBMIT,
      'after submit',
      `text ${objective.text}`,
      `text ${objective.marker}`,
      'status active',
    ])
  })

  it('types a plain objective as it is and requires only its text', async () => {
    await setGoal(fakePage(), 'Keep the Qoder goal until I clear it.')
    expect(recorded.events).toEqual([
      'expand',
      action('set'),
      `fill ${EDITOR} Keep the Qoder goal until I clear it.`,
      SUBMIT,
      'text Keep the Qoder goal until I clear it.',
      'status active',
    ])
  })
})

describe('pauseResumeClearGoal', () => {
  const objective = { input: 'unused', text: 'Keep the build green.', marker: 'MARKER' }
  const beforeClear = [
    MENU,
    action('pause'),
    'status paused',
    'reload',
    'expand',
    `text ${objective.text}`,
    `text ${objective.marker}`,
    'status paused',
    MENU,
    action('resume'),
    'status active',
    MENU,
    action('clear'),
  ]

  it('pauses, keeps the goal across a reload, resumes, and clears to the empty card', async () => {
    recorded.status = 'active'
    await pauseResumeClearGoal(fakePage(), objective)
    expect(recorded.events).toEqual([...beforeClear, 'visible [data-testid="goal-card-empty"]:visible'])
  })

  it('runs the provider confirmation between the clear and the empty-card check', async () => {
    recorded.status = 'active'
    await pauseResumeClearGoal(fakePage(), objective, {
      afterClear: async () => {
        recorded.events.push('confirm clear')
      },
    })
    expect(recorded.events).toEqual([...beforeClear, 'confirm clear', 'visible [data-testid="goal-card-empty"]:visible'])
  })
})

describe('countGoalTransitions', () => {
  const context = { leapmuxServer: { hubUrl: 'http://unit.invalid', adminToken: 'unit-token', workerId: 'worker' } }
  const message = (body: string) => ({ content: new TextEncoder().encode(body), contentCompression: ContentCompression.NONE }) as AgentChatMessage

  it('counts the transitions of every message that the paged read returns', async () => {
    reads.messages.mockResolvedValue([
      message('{"type":"goal_updated"}'),
      message('{"type":"notification_thread","messages":[{"type":"goal_updated"},{"type":"goal_cleared"}]}'),
      message('{"type":"assistant","text":"goal_updated"}'),
    ])
    expect(await countGoalTransitions(context, 'agent')).toBe(3)
    expect(reads.messages).toHaveBeenCalledExactlyOnceWith(context, 'agent')
  })

  it('returns null while the Worker read fails, so a poll can wait through a reconnection', async () => {
    reads.messages.mockRejectedValue(new Error('The Worker channel closed.'))
    expect(await countGoalTransitions(context, 'agent')).toBeNull()
  })

  it.each(['', ' '])('refuses an absent agent ID before any Worker read: %j', async (agentId) => {
    await expect(countGoalTransitions(context, agentId)).rejects.toThrow('requires an agent ID')
    expect(reads.messages).not.toHaveBeenCalled()
  })
})

describe('nativeGoalProbeTurn', () => {
  it('runs one native answer turn of the context and returns its request', async () => {
    const request = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: {} }
    answers.send.mockReset()
    answers.send.mockResolvedValueOnce(request)
    const context = { page: fakePage(), provider: 0 } as unknown as Parameters<typeof nativeGoalProbeTurn>[0]
    expect(await nativeGoalProbeTurn(context)).toBe(request)
    expect(answers.send).toHaveBeenCalledTimes(1)
    expect(answers.send).toHaveBeenCalledWith(context, 'Complete before the native goal refusal.', 'The native goal probe completed.')
  })
})
