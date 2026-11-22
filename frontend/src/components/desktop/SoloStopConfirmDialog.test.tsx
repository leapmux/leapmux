import type { SoloBusyWork } from '~/api/platformBridge'
import { cleanup, fireEvent, render, screen, within } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SoloStopConfirmDialog } from './SoloStopConfirmDialog'

afterEach(cleanup)

const sampleBusyWork: SoloBusyWork = {
  agents: [
    {
      tabId: 'agent-1',
      title: 'Refactor auth',
      activeBackgroundTasks: ['Run unit tests'],
      activeChildAgents: ['Codebase reviewer'],
    },
  ],
  terminals: [
    {
      tabId: 'term-1',
      title: 'Dev server',
      processes: [
        { pid: 51234, name: 'node' },
        { pid: 51240, name: 'esbuild' },
      ],
      totalCount: 2,
    },
  ],
}

describe('SoloStopConfirmDialog', () => {
  it('renders quit intent title, lead, and details for busy agents and terminals', () => {
    render(() => (
      <SoloStopConfirmDialog
        intent="quit"
        busyWork={sampleBusyWork}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />
    ))

    expect(screen.getByText('Quit LeapMux')).toBeInTheDocument()
    expect(screen.getByText('Quitting stops work in 2 tabs:')).toBeInTheDocument()
    expect(screen.getByText('Refactor auth')).toBeInTheDocument()
    expect(screen.getByText('This agent is still working. Quitting stops it.')).toBeInTheDocument()
    expect(within(screen.getByTestId('busy-background-tasks')).getByText('Run unit tests')).toBeInTheDocument()
    expect(within(screen.getByTestId('busy-child-activities')).getByText('Codebase reviewer')).toBeInTheDocument()
    expect(screen.getByText('Dev server')).toBeInTheDocument()
    expect(screen.getByText('2 processes still run in this terminal. Quitting stops them.')).toBeInTheDocument()
    expect(screen.getByText('node (pid 51234)')).toBeInTheDocument()
    expect(screen.getByText('esbuild (pid 51240)')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Quit anyway' })).toBeInTheDocument()
  })

  it('renders switch-mode intent wording and falls back to Agent and Terminal for empty titles', () => {
    render(() => (
      <SoloStopConfirmDialog
        intent="switch-mode"
        busyWork={{
          agents: [
            {
              tabId: 'agent-empty',
              title: '',
              activeBackgroundTasks: [],
              activeChildAgents: [],
            },
          ],
          terminals: [
            {
              tabId: 'term-empty',
              title: '',
              processes: [
                { pid: 41200, name: 'vite' },
              ],
              totalCount: 1,
            },
          ],
        }}
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />
    ))

    expect(screen.getByText('Switch mode')).toBeInTheDocument()
    expect(screen.getByText('Switching mode stops work in 2 tabs:')).toBeInTheDocument()
    expect(screen.getByText('Agent')).toBeInTheDocument()
    expect(screen.getByText('Terminal')).toBeInTheDocument()
    expect(screen.getByText('This agent is still working. Switching mode stops it.')).toBeInTheDocument()
    expect(screen.getByText('1 process still runs in this terminal. Switching mode stops it.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Switch mode anyway' })).toBeInTheDocument()
  })

  it('requires two clicks to confirm and calls onCancel on cancel', () => {
    const onConfirm = vi.fn()
    const onCancel = vi.fn()
    render(() => (
      <SoloStopConfirmDialog
        intent="quit"
        busyWork={sampleBusyWork}
        onConfirm={onConfirm}
        onCancel={onCancel}
      />
    ))

    fireEvent.click(screen.getByRole('button', { name: 'Quit anyway' }))
    expect(onConfirm).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Confirm?' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))
    expect(onConfirm).toHaveBeenCalledOnce()
    expect(onCancel).not.toHaveBeenCalled()

    cleanup()

    const onConfirmSecond = vi.fn()
    const onCancelSecond = vi.fn()
    render(() => (
      <SoloStopConfirmDialog
        intent="quit"
        busyWork={sampleBusyWork}
        onConfirm={onConfirmSecond}
        onCancel={onCancelSecond}
      />
    ))

    fireEvent.click(screen.getByTestId('solo-stop-confirm-cancel'))
    expect(onCancelSecond).toHaveBeenCalledOnce()
    expect(onConfirmSecond).not.toHaveBeenCalled()
  })
})
