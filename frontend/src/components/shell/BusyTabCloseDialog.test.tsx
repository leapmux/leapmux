import type { TabBusyReason } from './tabBusyProbe'
import { create } from '@bufbuild/protobuf'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TerminalProcessSchema } from '~/generated/proto/leapmux/v1/terminal_pb'
import { BusyTabCloseDialog } from './BusyTabCloseDialog'

afterEach(cleanup)

function renderDialog(reason: TabBusyReason, tabTitle = 'my tab') {
  const resolve = vi.fn()
  const onDismiss = vi.fn()
  render(() => (
    <BusyTabCloseDialog state={{ tabTitle, reason, resolve }} onDismiss={onDismiss} />
  ))
  return { resolve, onDismiss }
}

function terminalReason(processes: Array<{ pid: number, name: string }>, totalCount = processes.length): TabBusyReason {
  return {
    kind: 'terminal-processes',
    // Built through the generated schema, so the fixture is checked against the
    // real TerminalProcess shape. A conditional type over this union does not
    // distribute -- the agent arm has no `processes` -- so it resolves to
    // `never`, and a cast to `never` type-checks the fixture against nothing.
    processes: processes.map(p => create(TerminalProcessSchema, p)),
    totalCount,
  }
}

describe('BusyTabCloseDialog', () => {
  it('names the tab it is about to interrupt', () => {
    renderDialog(terminalReason([
      { pid: 1, name: 'node' },
    ]), 'dev server')

    expect(screen.getByText('dev server')).toBeTruthy()
  })

  describe('terminal', () => {
    it('states the reason and lists each process with its pid', () => {
      renderDialog(terminalReason([
        { pid: 51234, name: 'node' },
        { pid: 51240, name: 'esbuild' },
      ]))

      expect(screen.getByText(/2 processes still run in this terminal/)).toBeTruthy()
      const rows = screen.getByTestId('busy-processes').querySelectorAll('li')
      expect([...rows].map(li => li.textContent)).toEqual(['node (pid 51234)', 'esbuild (pid 51240)'])
    })

    it('uses the singular for one process', () => {
      renderDialog(terminalReason([
        { pid: 7, name: 'sleep' },
      ]))

      expect(screen.getByText(/1 process still runs in this terminal. Closing the tab stops it./)).toBeTruthy()
    })

    it('says how many the cap dropped', () => {
      renderDialog(terminalReason([
        { pid: 1, name: 'cc' },
      ], 48))

      const rows = screen.getByTestId('busy-processes').querySelectorAll('li')
      expect([...rows].map(li => li.textContent)).toEqual(['cc (pid 1)', 'and 47 more'])
    })

    it('shows no overflow row when nothing was dropped', () => {
      renderDialog(terminalReason([
        { pid: 1, name: 'cc' },
      ], 1))

      expect(screen.queryByText(/and \d+ more/)).toBeNull()
    })

    it('still shows the pid when the OS gave no name', () => {
      // macOS resolves a long name through a second syscall that fails for
      // another user's process, so the worker reports the pid alone.
      renderDialog(terminalReason([
        { pid: 99, name: '' },
      ]))

      expect(screen.getByText('unnamed process (pid 99)')).toBeTruthy()
    })
  })

  describe('agent', () => {
    it('describes work without inferring an active root turn', () => {
      renderDialog({ kind: 'agent-work', activeBackgroundTasks: [], activeChildAgents: [] })

      expect(screen.getByText('This agent is still working. Closing the tab stops it.')).toBeTruthy()
      expect(screen.queryByTestId('busy-background-tasks')).toBeNull()
    })

    it('lists the active background tasks', () => {
      renderDialog({
        kind: 'agent-work',
        activeBackgroundTasks: ['Research the API', 'npm test'],
        activeChildAgents: [],
      })

      expect(screen.getByText('2 background tasks are active:')).toBeTruthy()
      const rows = screen.getByTestId('busy-background-tasks').querySelectorAll('li')
      expect([...rows].map(li => li.textContent)).toEqual(['Research the API', 'npm test'])
    })

    it('uses the singular for one task', () => {
      renderDialog({
        kind: 'agent-work',
        activeBackgroundTasks: ['One'],
        activeChildAgents: [],
      })

      expect(screen.getByText('1 background task is active:')).toBeTruthy()
    })

    it('does not claim a turn when a background task explains the busy state', () => {
      // The Worker reports a root busy for a turn OR for a running background
      // task, and the wire carries only the answer. A turn that ended while a
      // subagent kept running is the state this branch exists to keep busy, and
      // announcing it as "turn in progress" tells the user something false.
      renderDialog({
        kind: 'agent-work',
        activeBackgroundTasks: ['Research'],
        activeChildAgents: [],
      })

      expect(screen.queryByText(/turn is in progress/)).toBeNull()
      expect(screen.getByText('This agent is still working. Closing the tab stops it.')).toBeTruthy()
    })
  })

  describe('answers', () => {
    it('resolves false on cancel', () => {
      const { resolve, onDismiss } = renderDialog(terminalReason([
        { pid: 1, name: 'node' },
      ]))

      fireEvent.click(screen.getByTestId('busy-tab-close-cancel'))

      expect(resolve).toHaveBeenCalledWith(false)
      expect(onDismiss).toHaveBeenCalled()
    })

    it('takes two clicks to confirm, and resolves true only on the second', () => {
      const { resolve } = renderDialog(terminalReason([
        { pid: 1, name: 'node' },
      ]))

      // `danger` renders the primary as a ConfirmButton, so the destructive
      // answer arms first and Enter cannot bypass it.
      fireEvent.click(screen.getByRole('button', { name: 'Close anyway' }))
      expect(resolve).not.toHaveBeenCalled()

      fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))
      expect(resolve).toHaveBeenCalledWith(true)
    })
  })
})

describe('BusyTabCloseDialog activity roles', () => {
  it('lists child activity separately from coordinator tasks', () => {
    renderDialog({
      kind: 'agent-work',
      activeBackgroundTasks: ['Coordinator task'],
      activeChildAgents: ['Child session'],
    })
    expect(screen.getByText('1 background task is active:')).toBeTruthy()
    expect(screen.getByText('1 child agent is active:')).toBeTruthy()
    expect(screen.getByTestId('busy-background-tasks').textContent).toBe('Coordinator task')
    expect(screen.getByTestId('busy-child-activities').textContent).toBe('Child session')
  })
})
