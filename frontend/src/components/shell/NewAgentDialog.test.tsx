import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { NewAgentDialog } from '~/components/shell/NewAgentDialog'
import { WorkerSchema } from '~/generated/proto/leapmux/v1/worker_pb'
import { createRepoGitStore } from '~/stores/repoGit.store'
/// <reference types="vitest/globals" />
import { withPreferences } from '~/test-support/preferencesProvider'

// The agent dialog's own guard wiring, mirroring the terminal twin: the
// parent computes the reason (AppShellDialogs suite), but THIS component
// stops the worker RPC — the notice renders, submit disables, and a form
// submit fires no openAgent. The wiring is token-identical to
// NewTerminalDialog's today; without this file it had no component-level
// pin, so dropping the `blockedReason` field or the notice passed every
// suite (the AppShellDialogs stub replaces the component entirely).
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
  statFile: vi.fn(async () => ({})),
  listDirectory: vi.fn(async () => ({ entries: [] })),
  openAgent: vi.fn(async () => ({})),
}))

const openAgentMock = workerRpc.openAgent as unknown as ReturnType<typeof vi.fn>

function renderDialog(blockedReason?: () => string | undefined) {
  return render(withPreferences(() => (
    <NewAgentDialog
      defaultWorkerId="w-1"
      defaultWorkingDir="/tmp"
      blockedReason={blockedReason}
      onCreated={() => {}}
      onClose={() => {}}
      repoGitStore={createRepoGitStore()}
    />
  )))
}

async function findCreateButton(): Promise<HTMLButtonElement> {
  await screen.findByRole('button', { name: 'Create' })
  return screen.getByRole('button', { name: 'Create' }) as HTMLButtonElement
}

describe('newAgentDialog tab-placement guard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('shows the blocked reason, disables submit, and fires no openAgent', async () => {
    renderDialog(() => 'Create a workspace first — a tab lives inside a workspace.')

    expect(await screen.findByTestId('new-tab-blocked-reason'))
      .toHaveTextContent(/create a workspace first/i)
    expect(await findCreateButton()).toBeDisabled()

    // The footer's button is `type=submit`, but programmatic submits (Enter
    // in an input) reach the same form handler — which re-checks the gate
    // and bails before the RPC.
    const form = document.querySelector('form')
    expect(form, 'the shell wraps body + footer in a form').toBeTruthy()
    fireEvent.submit(form!)

    expect(openAgentMock, 'the blocked guard holds before the worker RPC').not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Create' })).toBeDisabled()
  })

  it('keeps submit enabled once the placement precondition passes', async () => {
    // The companion that proves the BLOCKED reason is the disabler above:
    // with no reason and the same worker/dir state, submit arms.
    renderDialog()

    await waitFor(async () => {
      expect(await findCreateButton()).not.toBeDisabled()
    })
    expect(screen.queryByTestId('new-tab-blocked-reason')).toBeNull()
  })
})

describe('newAgentDialog title', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  // The pre-filled default has to keep the shape the worker's plan-mode
  // auto-rename accepts (`^Agent [A-Z][A-Za-z]+$`). A title the user leaves
  // alone is then still overwritable by a plan title, exactly as the
  // worker-picked name it replaced was.
  it('pre-fills a title from the shared pool', async () => {
    renderDialog()

    const input = await screen.findByTestId('title-input') as HTMLInputElement
    expect(input.value).toMatch(/^Agent [A-Z][A-Za-z]+$/)
  })

  it('re-rolls the title from the refresh button', async () => {
    renderDialog()

    const input = await screen.findByTestId('title-input') as HTMLInputElement
    // The pool has hundreds of names, so a single re-roll can legitimately
    // repeat. Click until the value changes rather than asserting one click
    // differs, which would be flaky by construction.
    const first = input.value
    for (let i = 0; i < 50 && input.value === first; i++)
      fireEvent.click(screen.getByTestId('title-regenerate'))
    expect(input.value).not.toBe(first)
    expect(input.value).toMatch(/^Agent [A-Z][A-Za-z]+$/)
  })

  it('sends the cleaned title to the worker, and reports that the user chose it', async () => {
    renderDialog()

    fireEvent.input(await screen.findByTestId('title-input'), {
      target: { value: '  Auth  fix  ' },
    })
    const createButton = await findCreateButton()
    await waitFor(() => expect(createButton).not.toBeDisabled())
    fireEvent.click(createButton)

    await waitFor(() => expect(openAgentMock).toHaveBeenCalledOnce())
    expect(openAgentMock).toHaveBeenCalledWith('w-1', expect.objectContaining({
      title: 'Auth fix',
      // The user typed over the suggestion, so plan mode must not replace it.
      titleAutoGenerated: false,
    }))
  })

  // The other half, and the one only this side can answer: the pre-filled
  // `Agent <Name>` reaches the worker looking exactly like a typed title, so
  // the dialog has to say that nobody chose it.
  it('reports a title the user left alone as auto-generated', async () => {
    renderDialog()

    await screen.findByTestId('title-input')
    const createButton = await findCreateButton()
    await waitFor(() => expect(createButton).not.toBeDisabled())
    fireEvent.click(createButton)

    await waitFor(() => expect(openAgentMock).toHaveBeenCalledOnce())
    const [, req] = openAgentMock.mock.calls[0]
    expect(req.titleAutoGenerated).toBe(true)
    expect(req.title).toMatch(/^Agent [A-Z][A-Za-z]+$/)
  })

  // Re-rolling is still the generator choosing, so the tab stays overwritable.
  it('reports a re-rolled title as auto-generated', async () => {
    renderDialog()

    await screen.findByTestId('title-input')
    fireEvent.click(screen.getByTestId('title-regenerate'))
    const createButton = await findCreateButton()
    await waitFor(() => expect(createButton).not.toBeDisabled())
    fireEvent.click(createButton)

    await waitFor(() => expect(openAgentMock).toHaveBeenCalledOnce())
    expect(openAgentMock.mock.calls[0][1].titleAutoGenerated).toBe(true)
  })

  it('disables submit and fires no RPC when the title is emptied', async () => {
    renderDialog()

    const createButton = await findCreateButton()
    await waitFor(() => expect(createButton).not.toBeDisabled())
    fireEvent.input(screen.getByTestId('title-input'), { target: { value: '' } })

    await waitFor(() => expect(createButton).toBeDisabled())
    expect(screen.getByText('Name must not be empty')).toBeInTheDocument()
    fireEvent.submit(document.querySelector('form')!)
    expect(openAgentMock).not.toHaveBeenCalled()
  })
})
