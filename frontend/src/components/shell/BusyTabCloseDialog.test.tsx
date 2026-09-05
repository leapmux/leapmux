import type { TabBusyReason } from './tabBusyProbe'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { BusyTabCloseDialog } from './BusyTabCloseDialog'

// jsdom implements neither showModal nor close on <dialog>. Same shim
// ConfirmDialog.test.tsx installs, for the same reason.
beforeEach(() => {
  HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
    this.setAttribute('open', '')
  })
  HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
    this.removeAttribute('open')
    this.dispatchEvent(new Event('close'))
  })
})

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
    processes: processes as TabBusyReason extends { processes: infer P } ? P : never,
    totalCount,
  }
}

describe('busyTabCloseDialog', () => {
  it('names the tab it is about to interrupt', () => {
    renderDialog(terminalReason([{ pid: 1, name: 'node' }]), 'dev server')

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
      renderDialog(terminalReason([{ pid: 7, name: 'sleep' }]))

      expect(screen.getByText(/1 process still runs in this terminal. Closing the tab stops it./)).toBeTruthy()
    })

    it('says how many the cap dropped', () => {
      renderDialog(terminalReason([{ pid: 1, name: 'cc' }], 48))

      const rows = screen.getByTestId('busy-processes').querySelectorAll('li')
      expect([...rows].map(li => li.textContent)).toEqual(['cc (pid 1)', 'and 47 more'])
    })

    it('shows no overflow row when nothing was dropped', () => {
      renderDialog(terminalReason([{ pid: 1, name: 'cc' }], 1))

      expect(screen.queryByText(/and \d+ more/)).toBeNull()
    })

    it('still shows the pid when the OS gave no name', () => {
      // macOS resolves a long name through a second syscall that fails for
      // another user's process, so the worker reports the pid alone.
      renderDialog(terminalReason([{ pid: 99, name: '' }]))

      expect(screen.getByText('unnamed process (pid 99)')).toBeTruthy()
    })
  })

  describe('agent', () => {
    it('states the turn is in progress', () => {
      renderDialog({ kind: 'agent-turn', activeTasks: [] })

      expect(screen.getByText('This agent\'s turn is in progress. Closing the tab stops it.')).toBeTruthy()
      expect(screen.queryByTestId('busy-background-tasks')).toBeNull()
    })

    it('lists the active background tasks', () => {
      renderDialog({
        kind: 'agent-turn',
        activeTasks: [
          { rowKey: 'r1', kind: 'subagent', title: 'Research the API', activity: 'a', status: 'running' },
          { rowKey: 'r2', kind: 'shell', title: 'npm test', activity: 'a', status: 'pending' },
        ],
      })

      expect(screen.getByText('2 background tasks are active:')).toBeTruthy()
      const rows = screen.getByTestId('busy-background-tasks').querySelectorAll('li')
      expect([...rows].map(li => li.textContent)).toEqual(['Research the API', 'npm test'])
    })

    it('uses the singular for one task', () => {
      renderDialog({
        kind: 'agent-turn',
        activeTasks: [{ rowKey: 'r1', kind: 'subagent', title: 'One', activity: 'a', status: 'running' }],
      })

      expect(screen.getByText('1 background task is active:')).toBeTruthy()
    })
  })

  describe('answers', () => {
    it('resolves false on cancel', () => {
      const { resolve, onDismiss } = renderDialog(terminalReason([{ pid: 1, name: 'node' }]))

      fireEvent.click(screen.getByTestId('busy-tab-close-cancel'))

      expect(resolve).toHaveBeenCalledWith(false)
      expect(onDismiss).toHaveBeenCalled()
    })

    it('takes two clicks to confirm, and resolves true only on the second', () => {
      const { resolve } = renderDialog(terminalReason([{ pid: 1, name: 'node' }]))

      // `danger` renders the primary as a ConfirmButton, so the destructive
      // answer arms first and Enter cannot bypass it.
      fireEvent.click(screen.getByRole('button', { name: 'Close anyway' }))
      expect(resolve).not.toHaveBeenCalled()

      fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))
      expect(resolve).toHaveBeenCalledWith(true)
    })
  })
})
