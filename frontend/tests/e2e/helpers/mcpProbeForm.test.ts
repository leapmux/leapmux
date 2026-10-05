import type { ControlRequest } from '~/stores/control.store'
import { readFileSync } from 'node:fs'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createComponent } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { useControlResponseHandling } from '~/components/chat/controlResponseHandling'
import { createControlAnswerState } from '~/components/chat/controls/types'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { flushStorageWrites, localStorageLoad, PREFIX_CONTROL_STATE, resetBrowserStorageForTests, setStorageAccountForTests } from '~/lib/browserStorage'
import { requestInstanceId } from '~/stores/control.store'
import { ControlRequestContent } from '~/test-support/controlRequestBanner'
import { TEST_USER_ID } from '~/test-support/crdtBridge'
import { collectE2EFiles } from '~/test-support/e2eFiles'
import { useTestStorage } from '~/test-support/persistentStorage'
import { frontendRoot, posixRelative } from '~/test-support/sourceTree'
import { mcpProbeFormDraftSaved } from './mcpProbeForm'

// The saved control answer is on the asynchronous storage tier, which keeps no
// in-memory mirror, so the round trips below need a database.
useTestStorage()

/**
 * Each test of `source` that reloads the page after `fillMcpProbeForm` with no
 * `waitForMcpProbeFormDraft` between the two.
 *
 * The scan splits the file at each test declaration, so a fill in one test and a
 * reload in the next never pair up.
 */
function testsThatReloadBeforeTheDraftLands(source: string): number {
  let count = 0
  for (const body of source.split(/^\s*\w*[Tt]est(?:\.\w+)?\(/m)) {
    for (const fill of body.matchAll(/\bfillMcpProbeForm\(/g)) {
      const afterFill = body.slice(fill.index)
      const reload = afterFill.search(/\bpage\.reload\(/)
      if (reload !== -1 && !afterFill.slice(0, reload).includes('waitForMcpProbeFormDraft('))
        count++
    }
  }
  return count
}

describe('every spec that reloads after fillMcpProbeForm', () => {
  const specs = collectE2EFiles().filter(file => file.endsWith('.spec.ts'))

  it('finds the specs that fill the probe form', () => {
    const fillers = specs.filter(file => readFileSync(file, 'utf8').includes('fillMcpProbeForm('))
    expect(fillers.length).toBeGreaterThan(0)
  })

  it('waits for the form draft to reach durable storage first', () => {
    const offenders = specs
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
  // If the page ever closes this window, this test fails and the wait is
  // unnecessary.
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
