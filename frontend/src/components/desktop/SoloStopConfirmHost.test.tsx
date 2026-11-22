import type { Component } from 'solid-js'
import type { SoloBusyWork, SwitchModeStartResult } from '~/api/platformBridge'
import { cleanup, fireEvent, render, screen, within } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Dialog } from '~/components/common/Dialog'
import { TAURI_EVENT_APP_CONFIRM_QUIT } from '~/generated/contracts/desktop'
import { confirmSoloSwitchMode, setSoloSwitchModePrompter } from '~/lib/soloStopConfirm'
import { motion } from '~/styles/tokens'
import { deferred, flush, settle } from '~/test-support/async'
import { SoloStopConfirmHost } from './SoloStopConfirmHost'

const {
  ackQuitConfirmation,
  beginSwitchMode,
  onEvent,
  resolveQuitConfirmation,
  resolveSwitchModeConfirmation,
} = vi.hoisted(() => ({
  ackQuitConfirmation: vi.fn<(promptId: number) => Promise<boolean>>(),
  beginSwitchMode: vi.fn<() => Promise<SwitchModeStartResult>>(),
  onEvent: vi.fn<(event: string, callback: (payload: unknown) => void) => Promise<() => void>>(),
  resolveQuitConfirmation: vi.fn<(promptId: number, confirm: boolean) => Promise<void>>(),
  resolveSwitchModeConfirmation: vi.fn<(promptId: number, confirm: boolean) => Promise<number | null>>(),
}))

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    platformBridge: {
      ...actual.platformBridge,
      ackQuitConfirmation,
      beginSwitchMode,
      onEvent,
      resolveQuitConfirmation,
      resolveSwitchModeConfirmation,
    },
  }
})

const BUSY_WORK: SoloBusyWork = {
  agents: [
    {
      tabId: 'tab-agent',
      title: 'Fix auth bug',
      activeBackgroundTasks: ['Run unit tests'],
      activeChildAgents: ['Explore repo'],
    },
  ],
  terminals: [
    {
      tabId: 'tab-term',
      title: 'dev-shell',
      processes: [
        { pid: 42, name: 'sleep' },
      ],
      totalCount: 1,
    },
  ],
}

const SurfaceDialog: Component = () => (
  <Dialog title="Underlying modal" onClose={() => {}}>
    <button type="button" data-testid="underlying-action">Continue</button>
  </Dialog>
)

let eventHandler: ((payload: unknown) => void) | null = null
let unlistenMock: ReturnType<typeof vi.fn<() => void>>

function emitQuitEvent(payload: unknown) {
  expect(eventHandler).not.toBeNull()
  eventHandler?.(payload)
}

beforeEach(() => {
  eventHandler = null
  unlistenMock = vi.fn<() => void>()
  ackQuitConfirmation.mockReset()
  ackQuitConfirmation.mockResolvedValue(true)
  beginSwitchMode.mockReset()
  beginSwitchMode.mockResolvedValue({ kind: 'confirm', promptId: 100, busyWork: BUSY_WORK })
  resolveQuitConfirmation.mockReset()
  resolveQuitConfirmation.mockResolvedValue(undefined)
  resolveSwitchModeConfirmation.mockReset()
  resolveSwitchModeConfirmation.mockImplementation(async (_promptId, confirm) => confirm ? 200 : null)
  onEvent.mockReset()
  onEvent.mockImplementation(async (event, callback) => {
    if (event === TAURI_EVENT_APP_CONFIRM_QUIT)
      eventHandler = callback
    return unlistenMock
  })
})

afterEach(() => {
  vi.useRealTimers()
  cleanup()
  setSoloSwitchModePrompter(null)
})

describe('SoloStopConfirmHost', () => {
  it('acknowledges a quit event and resolves true after two-click confirmation', async () => {
    render(() => <SoloStopConfirmHost />)
    await settle()

    emitQuitEvent({ promptId: 7, busyWork: BUSY_WORK })

    const dialog = await screen.findByTestId('solo-stop-confirm-dialog')
    expect(ackQuitConfirmation).toHaveBeenCalledExactlyOnceWith(7)
    expect(within(dialog).getByRole('heading', { name: 'Quit LeapMux' })).toBeInTheDocument()

    const confirmBtn = screen.getByTestId('solo-stop-confirm-confirm')
    expect(confirmBtn).toHaveAccessibleName('Quit anyway')

    fireEvent.click(confirmBtn)
    expect(confirmBtn).toHaveAccessibleName('Confirm?')
    expect(resolveQuitConfirmation).not.toHaveBeenCalled()

    fireEvent.click(confirmBtn)
    expect(resolveQuitConfirmation).toHaveBeenCalledExactlyOnceWith(7, true)
    expect(screen.queryByTestId('solo-stop-confirm-dialog')).toBeNull()
  })

  it('ignores quit events that are declined or malformed', async () => {
    ackQuitConfirmation.mockResolvedValueOnce(false)
    render(() => <SoloStopConfirmHost />)
    await settle()

    emitQuitEvent({ promptId: 11, busyWork: BUSY_WORK })
    await settle()
    expect(ackQuitConfirmation).toHaveBeenCalledExactlyOnceWith(11)
    expect(screen.queryByTestId('solo-stop-confirm-dialog')).toBeNull()

    ackQuitConfirmation.mockClear()
    emitQuitEvent(null)
    emitQuitEvent({ promptId: 0, busyWork: BUSY_WORK })
    emitQuitEvent({ promptId: -3, busyWork: BUSY_WORK })
    emitQuitEvent({ promptId: Number.NaN, busyWork: BUSY_WORK })
    emitQuitEvent({ promptId: 12 })
    emitQuitEvent({ promptId: 13, busyWork: null })
    emitQuitEvent({ promptId: 14, busyWork: {} })
    emitQuitEvent({
      promptId: 15,
      busyWork: { agents: [], terminals: [] },
    })
    await settle()

    expect(ackQuitConfirmation).not.toHaveBeenCalled()
    expect(screen.queryByTestId('solo-stop-confirm-dialog')).toBeNull()
  })

  it('normalizes malformed entries in quit event payloads without crashing render', async () => {
    render(() => <SoloStopConfirmHost />)
    await settle()

    emitQuitEvent({
      promptId: 16,
      busyWork: {
        agents: [
          null,
          { tabId: 'a1', title: 'Partial agent' },
        ],
        terminals: [
          null,
          {
            tabId: 't1',
            title: 'Partial term',
            processes: [
              null,
              { pid: 99, name: 'node' },
            ],
          },
        ],
      },
    })

    const dialog = await screen.findByTestId('solo-stop-confirm-dialog')
    expect(within(dialog).getByText('Partial agent')).toBeInTheDocument()
    expect(within(dialog).getByText('Partial term')).toBeInTheDocument()
    expect(within(dialog).getByText('node (pid 99)')).toBeInTheDocument()
  })

  it('resolves false and closes the dialog when a quit event is cancelled', async () => {
    render(() => <SoloStopConfirmHost />)
    await settle()

    emitQuitEvent({ promptId: 19, busyWork: BUSY_WORK })
    await screen.findByTestId('solo-stop-confirm-dialog')

    fireEvent.click(screen.getByTestId('solo-stop-confirm-cancel'))
    expect(resolveQuitConfirmation).toHaveBeenCalledExactlyOnceWith(19, false)
    expect(screen.queryByTestId('solo-stop-confirm-dialog')).toBeNull()
  })

  it('handles switch-mode confirmation and gives an active quit prompt precedence', async () => {
    render(() => <SoloStopConfirmHost />)
    await settle()

    const cancelled = confirmSoloSwitchMode()
    const cancelDialog = await screen.findByTestId('solo-stop-confirm-dialog')
    expect(within(cancelDialog).getByRole('heading', { name: 'Switch mode' })).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('solo-stop-confirm-cancel'))
    await expect(cancelled).resolves.toBeNull()
    expect(screen.queryByTestId('solo-stop-confirm-dialog')).toBeNull()

    const confirmed = confirmSoloSwitchMode()
    await screen.findByTestId('solo-stop-confirm-dialog')
    const confirmBtn = screen.getByTestId('solo-stop-confirm-confirm')
    fireEvent.click(confirmBtn)
    fireEvent.click(confirmBtn)
    await expect(confirmed).resolves.toBe(200)
    expect(screen.queryByTestId('solo-stop-confirm-dialog')).toBeNull()

    // While a quit prompt is open, switch-mode resolves null immediately and
    // leaves the quit prompt mounted.
    emitQuitEvent({ promptId: 23, busyWork: BUSY_WORK })
    await screen.findByTestId('solo-stop-confirm-dialog')
    expect(screen.getByRole('heading', { name: 'Quit LeapMux' })).toBeInTheDocument()

    await expect(confirmSoloSwitchMode()).resolves.toBeNull()
    expect(screen.getByRole('heading', { name: 'Quit LeapMux' })).toBeInTheDocument()
  })

  it('supersedes an open or closing switch-mode prompt with a fresh unarmed quit dialog', async () => {
    vi.useFakeTimers()
    render(() => (
      <>
        <SurfaceDialog />
        <SoloStopConfirmHost />
      </>
    ))
    await flush()

    const underlying = screen.getByRole('dialog', { name: 'Underlying modal' })
    expect(underlying).not.toHaveAttribute('inert')

    // Case 1: switch-mode is open and armed ("Confirm?").
    const firstSwitch = confirmSoloSwitchMode()
    await flush()
    const armedBtn = screen.getByTestId('solo-stop-confirm-confirm')
    expect(armedBtn).toHaveAccessibleName('Switch mode anyway')
    fireEvent.click(armedBtn)
    expect(armedBtn).toHaveAccessibleName('Confirm?')
    expect(underlying).toHaveAttribute('inert')

    emitQuitEvent({ promptId: 31, busyWork: BUSY_WORK })
    await flush()

    await expect(firstSwitch).resolves.toBeNull()
    const firstQuitDialog = screen.getByTestId('solo-stop-confirm-dialog')
    expect(within(firstQuitDialog).getByRole('heading', { name: 'Quit LeapMux' })).toBeInTheDocument()
    expect(firstQuitDialog).not.toHaveAttribute('inert')
    expect(underlying).toHaveAttribute('inert')

    // Fresh unarmed button: first click only arms, does not resolve.
    const firstQuitBtn = screen.getByTestId('solo-stop-confirm-confirm')
    expect(firstQuitBtn).toHaveAccessibleName('Quit anyway')
    fireEvent.click(firstQuitBtn)
    expect(firstQuitBtn).toHaveAccessibleName('Confirm?')
    expect(resolveQuitConfirmation).not.toHaveBeenCalled()

    // Dismiss first quit prompt so we can test superseding a closing switch-mode dialog.
    fireEvent.click(screen.getByTestId('solo-stop-confirm-cancel'))
    expect(resolveQuitConfirmation).toHaveBeenCalledExactlyOnceWith(31, false)
    expect(underlying).not.toHaveAttribute('inert')

    // Case 2: switch-mode dialog is mid-close-fade after clicking [×].
    const secondSwitch = confirmSoloSwitchMode()
    await flush()
    const switchDialog = screen.getByTestId('solo-stop-confirm-dialog')
    expect(underlying).toHaveAttribute('inert')
    fireEvent.click(within(switchDialog).getByRole('button', { name: 'Close' }))
    expect(switchDialog).toHaveAttribute('data-closing', '')

    emitQuitEvent({ promptId: 32, busyWork: BUSY_WORK })
    await flush()

    await expect(secondSwitch).resolves.toBeNull()
    const secondQuitDialog = screen.getByTestId('solo-stop-confirm-dialog')
    expect(within(secondQuitDialog).getByRole('heading', { name: 'Quit LeapMux' })).toBeInTheDocument()
    expect(secondQuitDialog).not.toHaveAttribute('data-closing')
    expect(secondQuitDialog).not.toHaveAttribute('inert')
    expect(underlying).toHaveAttribute('inert')

    // Advancing past Dialog's close fade must not cancel the new quit dialog.
    vi.advanceTimersByTime(motion.fast + 50)
    await flush()
    expect(screen.getByTestId('solo-stop-confirm-dialog')).toBeInTheDocument()
    expect(resolveQuitConfirmation).toHaveBeenCalledTimes(1)
  })

  it('covers underlying open dialogs with inert and restores focus on dismiss', async () => {
    render(() => (
      <>
        <SurfaceDialog />
        <SoloStopConfirmHost />
      </>
    ))
    await settle()

    const underlying = screen.getByRole('dialog', { name: 'Underlying modal' })
    const trigger = screen.getByTestId('underlying-action')
    trigger.focus()
    expect(document.activeElement).toBe(trigger)
    expect(underlying).not.toHaveAttribute('inert')

    emitQuitEvent({ promptId: 41, busyWork: BUSY_WORK })
    await screen.findByTestId('solo-stop-confirm-dialog')
    expect(underlying).toHaveAttribute('inert')

    fireEvent.click(screen.getByTestId('solo-stop-confirm-cancel'))
    expect(underlying).not.toHaveAttribute('inert')
    expect(document.activeElement).toBe(trigger)
  })

  it('preserves inert on underlying dialogs that another modal host already covered', async () => {
    render(() => (
      <>
        <SurfaceDialog />
        <SoloStopConfirmHost />
      </>
    ))
    await settle()

    const underlying = screen.getByRole('dialog', { name: 'Underlying modal' })
    underlying.setAttribute('inert', '')

    emitQuitEvent({ promptId: 42, busyWork: BUSY_WORK })
    await screen.findByTestId('solo-stop-confirm-dialog')
    expect(underlying).toHaveAttribute('inert')

    fireEvent.click(screen.getByTestId('solo-stop-confirm-cancel'))
    expect(underlying).toHaveAttribute('inert')
  })

  it('cancels an active quit prompt and releases covered dialogs on unmount and pagehide', async () => {
    // (a1) Unmount while quit prompt is open.
    const first = render(() => (
      <>
        <SurfaceDialog />
        <SoloStopConfirmHost />
      </>
    ))
    await settle()
    const underlying1 = screen.getByRole('dialog', { name: 'Underlying modal' })
    emitQuitEvent({ promptId: 51, busyWork: BUSY_WORK })
    await screen.findByTestId('solo-stop-confirm-dialog')
    expect(underlying1).toHaveAttribute('inert')

    first.unmount()
    expect(resolveQuitConfirmation).toHaveBeenCalledExactlyOnceWith(51, false)
    expect(underlying1).not.toHaveAttribute('inert')
    expect(unlistenMock).toHaveBeenCalledOnce()

    // (a2) pagehide while quit prompt is open.
    resolveQuitConfirmation.mockClear()
    unlistenMock.mockClear()
    const second = render(() => (
      <>
        <SurfaceDialog />
        <SoloStopConfirmHost />
      </>
    ))
    await settle()
    const underlying2 = screen.getByRole('dialog', { name: 'Underlying modal' })
    emitQuitEvent({ promptId: 52, busyWork: BUSY_WORK })
    await screen.findByTestId('solo-stop-confirm-dialog')
    expect(underlying2).toHaveAttribute('inert')

    window.dispatchEvent(new Event('pagehide'))
    expect(resolveQuitConfirmation).toHaveBeenCalledExactlyOnceWith(52, false)
    expect(underlying2).not.toHaveAttribute('inert')
    expect(screen.queryByTestId('solo-stop-confirm-dialog')).toBeNull()

    // Subsequent unmount is idempotent and does not double-resolve.
    second.unmount()
    expect(resolveQuitConfirmation).toHaveBeenCalledTimes(1)
  })

  it('releases a claimed prompt when unmounted mid-ack without mounting a dialog', async () => {
    const ackDeferred = deferred<boolean>()
    ackQuitConfirmation.mockReturnValueOnce(ackDeferred.promise)

    const { unmount } = render(() => <SoloStopConfirmHost />)
    await settle()

    emitQuitEvent({ promptId: 61, busyWork: BUSY_WORK })
    expect(ackQuitConfirmation).toHaveBeenCalledExactlyOnceWith(61)

    unmount()
    ackDeferred.resolve(true)
    await settle()

    expect(resolveQuitConfirmation).toHaveBeenCalledExactlyOnceWith(61, false)
    expect(screen.queryByTestId('solo-stop-confirm-dialog')).toBeNull()
  })

  it('ignores post-disposal quit events and cleans up the pagehide listener', async () => {
    const removeSpy = vi.spyOn(window, 'removeEventListener')
    const deferredListen = deferred<() => void>()
    let capturedCallback: ((payload: unknown) => void) | null = null
    onEvent.mockImplementationOnce(async (_event, cb) => {
      capturedCallback = cb
      return deferredListen.promise
    })

    const { unmount } = render(() => <SoloStopConfirmHost />)
    unmount()

    expect(removeSpy).toHaveBeenCalledWith('pagehide', expect.any(Function))
    const lateUnlisten = vi.fn<() => void>()
    deferredListen.resolve(lateUnlisten)
    await settle()
    expect(lateUnlisten).toHaveBeenCalledOnce()

    // Simulate a late event firing after disposal: must neither ack nor resolve.
    expect(capturedCallback).not.toBeNull()
    capturedCallback!({ promptId: 71, busyWork: BUSY_WORK })
    await settle()
    expect(ackQuitConfirmation).not.toHaveBeenCalled()
    expect(resolveQuitConfirmation).not.toHaveBeenCalled()
    expect(screen.queryByTestId('solo-stop-confirm-dialog')).toBeNull()

    // Also verify pagehide disposal ignores subsequent quit events.
    const host2 = render(() => <SoloStopConfirmHost />)
    await settle()
    const handlerBeforePagehide = eventHandler
    window.dispatchEvent(new Event('pagehide'))
    handlerBeforePagehide?.({ promptId: 72, busyWork: BUSY_WORK })
    await settle()
    expect(ackQuitConfirmation).not.toHaveBeenCalled()
    expect(resolveQuitConfirmation).not.toHaveBeenCalled()
    host2.unmount()
    removeSpy.mockRestore()
  })
})
