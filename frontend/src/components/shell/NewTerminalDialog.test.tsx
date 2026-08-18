import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { NewTerminalDialog } from '~/components/shell/NewTerminalDialog'
import { WorkerSchema } from '~/generated/leapmux/v1/worker_pb'
/// <reference types="vitest/globals" />
import { withPreferences } from '~/test-support/preferencesProvider'

// The dialog's own guard wiring — the parent computes the reason, but THIS
// component is what stops the worker RPC: the notice renders, submit
// disables, and a form submit fires no openTerminal. The AppShellDialogs
// suite covers the reason's computation with stubbed dialogs; this file
// renders the real one, so its worker/shell/git fetches are mocked at the
// API boundary.
vi.mock('~/api/clients', () => ({
  workerClient: {
    // The dialog context filters the fleet to ONLINE workers before it
    // applies the preselection, so the one mock worker must be online.
    listWorkers: vi.fn(async () => ({
      workers: [create(WorkerSchema, { id: 'w-1', online: true })],
    })),
  },
}))

// Bare-success shapes are enough: the dialog only reads resp fields it
// needs, and the point of these mocks is to let the tree mount — a MISSING
// export throws inside the render chain and takes the shells effect down
// with it, which is exactly what these mocks exist to prevent.
vi.mock('~/api/workerRpc', () => ({
  getGitInfo: vi.fn(async () => ({})),
  getWorkerSystemInfo: vi.fn(async () => ({})),
  listAvailableShells: vi.fn(async () => ({ shells: ['bash', 'zsh'], defaultShell: 'bash' })),
  statFile: vi.fn(async () => ({})),
  listDirectory: vi.fn(async () => ({ entries: [] })),
  openTerminal: vi.fn(async () => ({ terminalId: 'tid-1', title: 'Terminal 1' })),
}))

const openTerminalMock = workerRpc.openTerminal as unknown as ReturnType<typeof vi.fn>

function renderDialog(blockedReason?: () => string | undefined) {
  return render(withPreferences(() => (
    <NewTerminalDialog
      defaultWorkerId="w-1"
      defaultWorkingDir="/tmp"
      blockedReason={blockedReason}
      onCreated={() => {}}
      onClose={() => {}}
    />
  )))
}

async function findCreateButton(): Promise<HTMLButtonElement> {
  await screen.findByRole('button', { name: 'Create' })
  return screen.getByRole('button', { name: 'Create' }) as HTMLButtonElement
}

describe('newTerminalDialog tab-placement guard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('shows the blocked reason, disables submit, and fires no openTerminal', async () => {
    renderDialog(() => 'Create a workspace first — a tab lives inside a workspace.')

    expect(await screen.findByTestId('new-tab-blocked-reason'))
      .toHaveTextContent(/create a workspace first/i)
    expect(await findCreateButton()).toBeDisabled()

    // The footer's button is `type=submit`, but programmatic submits (Enter
    // in an input) reach the same form handler — which re-checks the gate
    // and bails before the RPC (and before the loading state, which is why
    // the label stays "Create").
    const form = document.querySelector('form')
    expect(form, 'the shell wraps body + footer in a form').toBeTruthy()
    fireEvent.submit(form!)

    expect(openTerminalMock, 'the blocked guard holds before the worker RPC').not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Create' })).toBeDisabled()
  })

  it('keeps submit enabled once the placement precondition passes', async () => {
    // The companion that proves the BLOCKED reason is the disabler above:
    // with no reason and the same worker/dir/shell state, submit arms.
    renderDialog()

    await waitFor(async () => {
      expect(await findCreateButton()).not.toBeDisabled()
    })
    expect(screen.queryByTestId('new-tab-blocked-reason')).toBeNull()
  })
})
