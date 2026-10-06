import type { Locator, Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ControlRequest } from '~/stores/control.store'
import { readFileSync } from 'node:fs'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createComponent } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useControlResponseHandling } from '~/components/chat/controlResponseHandling'
import { createControlAnswerState } from '~/components/chat/controls/types'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { accountStorageKey, flushStorageWrites, localStorageLoad, PREFIX_CONTROL_STATE, resetBrowserStorageForTests, setStorageAccountForTests } from '~/lib/browserStorage'
import { requestInstanceId } from '~/stores/control.store'
import { ControlRequestContent } from '~/test-support/controlRequestBanner'
import { TEST_USER_ID } from '~/test-support/crdtBridge'
import { collectE2EFiles } from '~/test-support/e2eFiles'
import { fakeLocator } from '~/test-support/fakeLocator'
import { useTestStorage } from '~/test-support/persistentStorage'
import { frontendRoot, posixRelative } from '~/test-support/sourceTree'
import { exerciseMcpProbeFormRoundTrip, mcpProbeFormDraftSaved } from './mcpProbeForm'
import { nativeToolResult } from './nativeToolResult'

/** The browser and model steps of a round trip, in order. */
const trip = vi.hoisted(() => ({ events: [] as string[], body: 'FORM_ROUND_TRIP_OK', resultCallId: 'probe-call' }))
vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  sendMessage: async (_page: unknown, text: string) => { trip.events.push(`send ${text}`) },
  waitForAgentIdle: async () => { trip.events.push('idle') },
  messageBubbles: () => ({ filter: () => ({ first: () => assertingLocator('result bubble') }) }),
}))
vi.mock('./providerToolCalls', () => ({
  mcpToolCall: (_provider: unknown, id: string, request: { server: string, tool: string }) => ({ id, name: `${request.server}.${request.tool}`, arguments: {} }),
}))

// The saved control answer is on the asynchronous storage tier, which keeps no
// in-memory mirror, so the round trips below need a database.
useTestStorage()

/**
 * Each test of `source` that reloads the page after `fillMcpProbeForm` with no
 * `waitForMcpProbeFormDraft` between the two.
 *
 * The scan splits the file at each test declaration, so a fill in one test and a
 * reload in the next never pair up. It reads the code alone: a comment that
 * mentions a reload, as the guide of `waitForMcpProbeFormDraft` does, is no reload.
 * It removes each block comment and each line that holds only a line comment. A
 * line comment after code stays, because `//` also occurs inside a string.
 */
function testsThatReloadBeforeTheDraftLands(source: string): number {
  const code = source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
  let count = 0
  for (const body of code.split(/^\s*\w*[Tt]est(?:\.\w+)?\(/m)) {
    for (const fill of body.matchAll(/\bfillMcpProbeForm\(/g)) {
      const afterFill = body.slice(fill.index)
      const reload = afterFill.search(/\bpage\.reload\(/)
      if (reload !== -1 && !afterFill.slice(0, reload).includes('waitForMcpProbeFormDraft('))
        count++
    }
  }
  return count
}

describe('every E2E file that reloads after fillMcpProbeForm', () => {
  // The specs and the shared scenarios both fill the form, so the scan reads every E2E source that is not a unit test.
  const sources = collectE2EFiles().filter(file => file.endsWith('.ts') && !file.endsWith('.test.ts'))

  it('finds the files that fill the probe form, including the round trip scenario', () => {
    const fillers = sources.filter(file => readFileSync(file, 'utf8').includes('fillMcpProbeForm('))
    expect(fillers.map(file => posixRelative(frontendRoot, file))).toContain('tests/e2e/helpers/mcpProbeForm.ts')
  })

  it('waits for the form draft to reach durable storage first', () => {
    const offenders = sources
      .filter(file => testsThatReloadBeforeTheDraftLands(readFileSync(file, 'utf8')) > 0)
      .map(file => posixRelative(frontendRoot, file))
    expect(offenders).toEqual([])
  })
})

describe('testsThatReloadBeforeTheDraftLands', () => {
  const fill = '  const form = await fillMcpProbeForm(page)\n'
  const wait = '  await waitForMcpProbeFormDraft(page, userId)\n'
  const reload = '  await page.reload()\n'

  it('flags a reload that follows the fill directly', () => {
    expect(testsThatReloadBeforeTheDraftLands(`test('a', async () => {\n${fill}${reload}})\n`)).toBe(1)
  })

  it('accepts a reload that follows the wait', () => {
    expect(testsThatReloadBeforeTheDraftLands(`test('a', async () => {\n${fill}${wait}${reload}})\n`)).toBe(0)
  })

  it('accepts a test that never reloads', () => {
    expect(testsThatReloadBeforeTheDraftLands(`test('a', async () => {\n${fill}})\n`)).toBe(0)
  })

  it('does not pair a fill with the reload of the next test', () => {
    const source = `gooseTest('a', async () => {\n${fill}})\ngooseTest('b', async () => {\n${reload}})\n`
    expect(testsThatReloadBeforeTheDraftLands(source)).toBe(0)
  })

  it('ignores a reload that only a comment mentions', () => {
    const comments = '  /** Call it before a `page.reload()`. */\n  // await page.reload()\n'
    expect(testsThatReloadBeforeTheDraftLands(`test('a', async () => {\n${fill}${comments}${wait}${reload}})\n`)).toBe(0)
  })

  it('counts each test that reloads early', () => {
    const source = `test('a', async () => {\n${fill}${reload}})\ntest('b', async () => {\n${fill}${wait}${reload}})\ntest('c', async () => {\n${fill}${reload}})\n`
    expect(testsThatReloadBeforeTheDraftLands(source)).toBe(2)
  })
})

describe('mcpProbeFormDraftSaved', () => {
  const saved: Record<string, string> = { 'elicitation:"count"': '0', 'elicitation:"enabled"': 'false', 'elicitation:"color"': '"b"' }

  it('accepts a record that holds the three answers', () => {
    expect(mcpProbeFormDraftSaved({ choices: saved })).toBe(true)
  })

  it('accepts a record that also holds an unrelated choice', () => {
    expect(mcpProbeFormDraftSaved({ choices: { ...saved, 'control-permissions-pill': 'once' }, currentPage: 0 })).toBe(true)
  })

  it.each(['elicitation:"count"', 'elicitation:"enabled"', 'elicitation:"color"'])('rejects a record that lacks %s', (key) => {
    const { [key]: _removed, ...rest } = saved
    expect(mcpProbeFormDraftSaved({ choices: rest })).toBe(false)
  })

  it.each([
    ['the count', { 'elicitation:"count"': '1' }],
    ['the boolean', { 'elicitation:"enabled"': 'true' }],
    ['the color', { 'elicitation:"color"': '"r"' }],
    ['the color as bare text', { 'elicitation:"color"': 'b' }],
    ['the count as a number', { 'elicitation:"count"': 0 }],
  ])('rejects a record with a wrong value for %s', (_name, override) => {
    expect(mcpProbeFormDraftSaved({ choices: { ...saved, ...override } })).toBe(false)
  })

  it.each([undefined, null, 0, '', 'choices', [], {}, { choices: null }, { choices: [] }, { choices: 'x' }])('rejects the non-record %j', (value) => {
    expect(mcpProbeFormDraftSaved(value)).toBe(false)
  })
})

describe('the saved answers of the probe form', () => {
  const request: ControlRequest = {
    requestId: 'probe-form',
    agentId: 'agent-1',
    agentProvider: AgentProvider.GOOSE,
    payload: {
      method: 'elicitation/create',
      params: {
        mode: 'form',
        message: 'Choose the probe settings.',
        requestedSchema: {
          type: 'object',
          required: ['count', 'enabled', 'color'],
          properties: {
            count: { type: 'integer', title: 'Count', minimum: 0, maximum: 3 },
            enabled: { type: 'boolean', title: 'Enabled' },
            color: { type: 'string', title: 'Color', oneOf: [{ const: 'b', title: 'Blue' }, { const: 'r', title: 'Red' }] },
          },
        },
      },
    },
  }
  const answerKey = `${PREFIX_CONTROL_STATE}${request.agentId}:${requestInstanceId(request)}` as const

  /**
   * Mount the probe form with the persistence hook that the composer runs, and
   * wait for the restore of its saved answers to end.
   */
  async function mountForm() {
    const answerState = createControlAnswerState()
    const view = render(() => {
      useControlResponseHandling({ agentId: request.agentId, controlRequests: [request], onSendMessage: vi.fn() }, answerState, () => undefined, vi.fn())
      return createComponent(ControlRequestContent, { request, answerState, agentProvider: AgentProvider.GOOSE })
    })
    await vi.waitFor(() => expect(answerState.ready()).toBe(true))
    return view
  }

  function fillCount() {
    fireEvent.input(screen.getByLabelText('Count *'), { target: { value: '0' } })
  }

  // JSDOM keeps popover children outside its accessibility tree, so the options are found with `hidden`.
  function chooseNo() {
    fireEvent.click(screen.getByRole('button', { name: 'Enabled *' }))
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'No', hidden: true }))
  }

  function chooseBlue() {
    fireEvent.click(screen.getByRole('button', { name: 'Color *' }))
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'Blue', hidden: true }))
  }

  it('reads as saved only after the last answer reaches storage', async () => {
    await mountForm()
    fillCount()
    chooseNo()
    await flushStorageWrites()
    expect(mcpProbeFormDraftSaved(await localStorageLoad(answerKey))).toBe(false)

    chooseBlue()
    await flushStorageWrites()
    expect(mcpProbeFormDraftSaved(await localStorageLoad(answerKey))).toBe(true)
  })

  it('restores every answer into a form that mounts after the reload', async () => {
    const first = await mountForm()
    fillCount()
    chooseNo()
    chooseBlue()
    await flushStorageWrites()
    first.unmount()

    await mountForm()
    expect(screen.getByLabelText('Count *')).toHaveValue('0')
    expect(screen.getByRole('button', { name: 'Enabled *' })).toHaveTextContent('No')
    expect(screen.getByRole('button', { name: 'Color *' })).toHaveTextContent('Blue')
  })

  // This test states why `waitForMcpProbeFormDraft` exists. `App` flushes the
  // write queue on `pagehide`, which narrows the window but cannot close it: the
  // flush awaits the database connection before it opens a transaction. A click
  // that an unload follows within one IndexedDB round trip loses its answer,
  // and each earlier answer survives. That is the failure of the Goose form
  // specs: the count and the switch restored, and the color did not.
  // The test simulates the unload with `resetBrowserStorageForTests`, which
  // settles every pending write as failed. It does not run the `pagehide`
  // flush of `App`, so it cannot show whether the page closes this window.
  it('loses only the answer that an unload overtakes before the queue commits it', async () => {
    const first = await mountForm()
    fillCount()
    chooseNo()
    await flushStorageWrites()
    chooseBlue()
    // No await since the click: the write waits in the queue, as it does when
    // the reload starts a few milliseconds after the click.
    resetBrowserStorageForTests()
    setStorageAccountForTests(TEST_USER_ID)
    first.unmount()

    await mountForm()
    expect(screen.getByLabelText('Count *')).toHaveValue('0')
    expect(screen.getByRole('button', { name: 'Enabled *' })).toHaveTextContent('No')
    expect(screen.getByRole('button', { name: 'Color *' })).toHaveTextContent('Select an option')
    expect(mcpProbeFormDraftSaved(await localStorageLoad(answerKey))).toBe(false)
  })
})

/** A fake locator that records each Playwright check and each click, and passes each check. */
function assertingLocator(name: string, children: Record<string, Locator> = {}): Locator {
  return fakeLocator((check) => {
    trip.events.push(`${name} ${check.expression}`)
    return true
  }, {
    getByLabel: (label: string) => children[label] ?? assertingLocator(`${name} > ${label}`),
    getByRole: (_role: string, options: { name: string }) => children[options.name] ?? assertingLocator(`${name} > ${options.name}`),
    filter: () => assertingLocator(name, children),
    fill: async (value: string) => { trip.events.push(`${name} fill ${value}`) },
    click: async () => { trip.events.push(`${name} click`) },
  })
}

/** The saved answers of `fillMcpProbeForm`: the count 0, the boolean false, and the color constant "b" (Blue). */
const SAVED_FORM_CHOICES = { 'elicitation:"count"': '0', 'elicitation:"enabled"': 'false', 'elicitation:"color"': '"b"' }

describe('exerciseMcpProbeFormRoundTrip', () => {
  beforeEach(() => {
    trip.events = []
    trip.body = 'FORM_ROUND_TRIP_OK'
    trip.resultCallId = 'probe-call'
  })

  function roundTripContext(adminUserId: string | undefined = TEST_USER_ID): ManagedNativeScenarioContext {
    const form = assertingLocator('form')
    const page = Object.assign({} as Page, {
      getByTestId: (testId: string) => testId === 'elicitation-form' ? form : assertingLocator(testId),
      getByRole: (_role: string, options: { name: string }) => assertingLocator(`menu ${options.name}`),
      reload: async () => {
        trip.events.push('reload')
        return null
      },
      // The fake storage holds the saved answers of the fill. A key read gets two arguments, and an entry read three.
      evaluate: async (_read: unknown, args: readonly unknown[]) => args.length === 3
        ? { v: { choices: SAVED_FORM_CHOICES } }
        : [`${accountStorageKey(TEST_USER_ID, PREFIX_CONTROL_STATE)}agent-1`],
    })
    const modelScript = {
      prompt: (text: string) => text,
      queue: async () => {
        trip.events.push('queue')
        return 3
      },
      waitForSteps: async (count: number) => { trip.events.push(`steps ${count}`) },
      requestAt: async (index: number) => ({
        protocol: 'openai-chat-completions',
        path: '/v1/chat/completions',
        stepIndex: index,
        body: { messages: [{ role: 'tool', tool_call_id: trip.resultCallId, content: trip.body }] },
      }),
    } as unknown as ModelScript
    return { page, modelScript, provider: AgentProvider.GOOSE, workspaceId: 'workspace', leapmuxServer: { hubUrl: '', adminToken: '', workerId: '', ...(adminUserId === undefined ? {} : { adminUserId }) } }
  }

  it('approves the tool, fills the form, and submits it, and returns the request after the call', async () => {
    const approveTool = async () => {
      trip.events.push('approve tool')
    }
    const request = await exerciseMcpProbeFormRoundTrip(roundTripContext(), { callId: 'probe-call', reloadBeforeSubmit: false, approveTool })
    expect(request.stepIndex).toBe(4)
    expect(trip.events.slice(0, 5)).toEqual(['queue', 'send Call the form_probe ask tool exactly once.', 'steps 4', 'approve tool', 'form to.be.visible'])
    expect(trip.events).toContain('control-actions > Approve click')
    expect(trip.events).not.toContain('reload')
    expect(trip.events.slice(-4)).toEqual(['steps 5', 'idle', 'result bubble to.be.visible', 'form to.have.count'])
  })

  it('reloads after the answers land, and requires the restored answers before the submit', async () => {
    await exerciseMcpProbeFormRoundTrip(roundTripContext(), { callId: 'probe-call', reloadBeforeSubmit: true })
    const reload = trip.events.indexOf('reload')
    const approve = trip.events.indexOf('control-actions > Approve click')
    expect(reload).toBeGreaterThan(trip.events.indexOf('menu Blue click'))
    expect(trip.events.slice(reload + 1, approve)).toEqual([
      'form > Count * to.have.value',
      'form > Enabled * to.have.text',
      'form > Color * to.have.text',
    ])
  })

  it('refuses a reload without the account that saves the answers, before it queues a step', async () => {
    await expect(exerciseMcpProbeFormRoundTrip(roundTripContext(''), { callId: 'probe-call', reloadBeforeSubmit: true })).rejects.toThrow('needs the account')
    expect(trip.events).toEqual([])
  })

  it('fails when the model does not read the accepted answers', async () => {
    trip.body = 'FORM_ROUND_TRIP_FAILED'
    await expect(exerciseMcpProbeFormRoundTrip(roundTripContext(), { callId: 'probe-call', reloadBeforeSubmit: false })).rejects.toThrow('accepted answers')
  })

  it('fails when the accepted answers are the result of another call', async () => {
    trip.resultCallId = 'another-call'
    await expect(exerciseMcpProbeFormRoundTrip(roundTripContext(), { callId: 'probe-call', reloadBeforeSubmit: false })).rejects.toThrow('0 results for probe-call')
  })

  it('reads the result through the reader of the provider', async () => {
    trip.resultCallId = 'native-probe-call'
    const context = { ...roundTripContext(), readToolResult: (request: Parameters<typeof nativeToolResult>[0]) => ({ text: nativeToolResult(request, 'native-probe-call') }) }
    const request = await exerciseMcpProbeFormRoundTrip(context, { callId: 'probe-call', reloadBeforeSubmit: false })
    expect(request.stepIndex).toBe(4)
  })
})
