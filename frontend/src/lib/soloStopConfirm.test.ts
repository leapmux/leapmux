import type { SoloBusyWork, SwitchModeStartResult } from '~/api/platformBridge'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { platformBridge } from '~/api/platformBridge'
import {
  confirmSoloSwitchMode,
  isSoloBusyWorkEmpty,
  requestDesktopSwitchMode,
  setSoloSwitchModePrompter,
} from './soloStopConfirm'

const emptyWork: SoloBusyWork = { agents: [], terminals: [] }
const busyWork: SoloBusyWork = {
  agents: [
    {
      tabId: 'agent-1',
      title: 'Refactor auth',
      activeBackgroundTasks: ['Run unit tests'],
      activeChildAgents: [],
    },
  ],
  terminals: [],
}

afterEach(() => {
  setSoloSwitchModePrompter(null)
  vi.restoreAllMocks()
})

describe('isSoloBusyWorkEmpty', () => {
  it('reports empty only when both agents and terminals are empty', () => {
    expect(isSoloBusyWorkEmpty(emptyWork)).toBe(true)
    expect(isSoloBusyWorkEmpty(busyWork)).toBe(false)
    expect(isSoloBusyWorkEmpty({
      agents: [],
      terminals: [
        {
          tabId: 'term-1',
          title: 'Dev server',
          processes: [
            { pid: 123, name: 'vite' },
          ],
          totalCount: 1,
        },
      ],
    })).toBe(false)
  })
})

describe('confirmSoloSwitchMode', () => {
  it('returns the switchId without calling the prompter when Rust authorizes immediately', async () => {
    vi.spyOn(platformBridge, 'beginSwitchMode').mockResolvedValue({ kind: 'proceed', switchId: 4 })
    const prompter = vi.fn(async () => false)
    setSoloSwitchModePrompter(prompter)

    await expect(confirmSoloSwitchMode()).resolves.toBe(4)
    expect(prompter).not.toHaveBeenCalled()
  })

  it('returns null without calling the prompter when Rust blocks switch mode', async () => {
    vi.spyOn(platformBridge, 'beginSwitchMode').mockResolvedValue({ kind: 'blocked' })
    const prompter = vi.fn(async () => true)
    setSoloSwitchModePrompter(prompter)

    await expect(confirmSoloSwitchMode()).resolves.toBeNull()
    expect(prompter).not.toHaveBeenCalled()
  })

  it('forwards the registered prompter answer to resolveSwitchModeConfirmation', async () => {
    vi.spyOn(platformBridge, 'beginSwitchMode')
      .mockResolvedValueOnce({ kind: 'confirm', promptId: 10, busyWork })
      .mockResolvedValueOnce({ kind: 'confirm', promptId: 11, busyWork })
    const resolveSpy = vi.spyOn(platformBridge, 'resolveSwitchModeConfirmation')
      .mockResolvedValueOnce(null)
      .mockResolvedValueOnce(21)
    const prompter = vi.fn<(_busy: SoloBusyWork) => Promise<boolean>>()
      .mockResolvedValueOnce(false)
      .mockResolvedValueOnce(true)
    setSoloSwitchModePrompter(prompter)

    await expect(confirmSoloSwitchMode()).resolves.toBeNull()
    await expect(confirmSoloSwitchMode()).resolves.toBe(21)
    expect(prompter).toHaveBeenCalledTimes(2)
    expect(prompter).toHaveBeenCalledWith(busyWork)
    expect(resolveSpy).toHaveBeenNthCalledWith(1, 10, false)
    expect(resolveSpy).toHaveBeenNthCalledWith(2, 11, true)
  })

  it('fails open to Rust resolution when no prompter is registered or when the prompter throws or rejects', async () => {
    vi.spyOn(platformBridge, 'beginSwitchMode').mockResolvedValue({ kind: 'confirm', promptId: 3, busyWork })
    const resolveSpy = vi.spyOn(platformBridge, 'resolveSwitchModeConfirmation')
      .mockImplementation(async (_id, confirm) => confirm ? 9 : null)

    // No prompter registered.
    await expect(confirmSoloSwitchMode()).resolves.toBe(9)

    // Rejecting prompter fails open and clears inFlight.
    setSoloSwitchModePrompter(async () => {
      throw new Error('prompt failed asynchronously')
    })
    await expect(confirmSoloSwitchMode()).resolves.toBe(9)

    // Synchronously throwing prompter fails open and clears inFlight.
    setSoloSwitchModePrompter(() => {
      throw new Error('prompt failed synchronously')
    })
    await expect(confirmSoloSwitchMode()).resolves.toBe(9)

    // Healthy prompter works on the next call.
    const healthy = vi.fn(async () => false)
    setSoloSwitchModePrompter(healthy)
    await expect(confirmSoloSwitchMode()).resolves.toBeNull()
    expect(healthy).toHaveBeenCalledOnce()
    expect(resolveSpy).toHaveBeenLastCalledWith(3, false)
  })

  it('coalesces concurrent calls and starts fresh after settlement', async () => {
    let resolveStart!: (value: SwitchModeStartResult) => void
    const startSpy = vi.spyOn(platformBridge, 'beginSwitchMode')
      .mockImplementationOnce(() => new Promise<SwitchModeStartResult>((resolve) => {
        resolveStart = resolve
      }))
      .mockResolvedValueOnce({ kind: 'confirm', promptId: 2, busyWork })
    vi.spyOn(platformBridge, 'resolveSwitchModeConfirmation')
      .mockImplementation(async (_id, confirm) => confirm ? 7 : null)

    let resolvePrompt!: (confirmed: boolean) => void
    const prompter = vi.fn<(_busy: SoloBusyWork) => Promise<boolean>>()
      .mockImplementationOnce(() => new Promise<boolean>((resolve) => {
        resolvePrompt = resolve
      }))
      .mockResolvedValueOnce(true)
    setSoloSwitchModePrompter(prompter)

    const first = confirmSoloSwitchMode()
    const second = confirmSoloSwitchMode()
    expect(first).toBe(second)
    expect(startSpy).toHaveBeenCalledOnce()

    resolveStart({ kind: 'confirm', promptId: 1, busyWork })
    await vi.waitFor(() => expect(prompter).toHaveBeenCalledOnce())
    resolvePrompt(false)

    await expect(first).resolves.toBeNull()
    await expect(second).resolves.toBeNull()

    await expect(confirmSoloSwitchMode()).resolves.toBe(7)
    expect(startSpy).toHaveBeenCalledTimes(2)
    expect(prompter).toHaveBeenCalledTimes(2)
  })
})

describe('requestDesktopSwitchMode', () => {
  it('coalesces concurrent switch-mode transitions into one fade and switchMode(switchId) call', async () => {
    let resolvePrompt!: (confirmed: boolean) => void
    vi.spyOn(platformBridge, 'beginSwitchMode').mockResolvedValue({ kind: 'confirm', promptId: 5, busyWork })
    vi.spyOn(platformBridge, 'resolveSwitchModeConfirmation').mockResolvedValue(42)
    setSoloSwitchModePrompter(() => new Promise<boolean>((resolve) => {
      resolvePrompt = resolve
    }))
    const switchSpy = vi.spyOn(platformBridge, 'switchMode').mockResolvedValue(undefined)
    const disconnectSpy = vi.fn()
    const previousDisconnect = window.__leapmux_disconnectDesktop
    window.__leapmux_disconnectDesktop = disconnectSpy
    try {
      const first = requestDesktopSwitchMode()
      const second = requestDesktopSwitchMode()
      expect(first).toBe(second)

      await vi.waitFor(() => expect(resolvePrompt).toBeDefined())
      resolvePrompt(true)
      await first

      expect(switchSpy).toHaveBeenCalledExactlyOnceWith(42)
      expect(disconnectSpy).toHaveBeenCalledOnce()
      expect(document.querySelector('[style*="2147483647"]')).toBeNull()
    }
    finally {
      if (previousDisconnect === undefined)
        delete window.__leapmux_disconnectDesktop
      else
        window.__leapmux_disconnectDesktop = previousDisconnect
    }
  })

  it('catches switchMode failure, removes the overlay, and skips disconnect', async () => {
    vi.spyOn(platformBridge, 'beginSwitchMode').mockResolvedValue({ kind: 'proceed', switchId: 8 })
    vi.spyOn(platformBridge, 'switchMode').mockRejectedValueOnce(new Error('rpc failed'))
    const disconnectSpy = vi.fn()
    const previousDisconnect = window.__leapmux_disconnectDesktop
    window.__leapmux_disconnectDesktop = disconnectSpy
    try {
      await expect(requestDesktopSwitchMode()).resolves.toBeUndefined()
      expect(disconnectSpy).not.toHaveBeenCalled()
      expect(document.querySelector('[style*="2147483647"]')).toBeNull()
    }
    finally {
      if (previousDisconnect === undefined)
        delete window.__leapmux_disconnectDesktop
      else
        window.__leapmux_disconnectDesktop = previousDisconnect
    }
  })

  it('skips fade and switchMode when beginSwitchMode is blocked or confirmation resolution is superseded', async () => {
    const switchSpy = vi.spyOn(platformBridge, 'switchMode').mockResolvedValue(undefined)
    const beginSpy = vi.spyOn(platformBridge, 'beginSwitchMode')
      .mockResolvedValueOnce({ kind: 'blocked' })
      .mockResolvedValueOnce({ kind: 'confirm', promptId: 9, busyWork })
    const resolveSpy = vi.spyOn(platformBridge, 'resolveSwitchModeConfirmation')
      .mockResolvedValueOnce(null)
    setSoloSwitchModePrompter(async () => true)

    await requestDesktopSwitchMode()
    await requestDesktopSwitchMode()

    expect(beginSpy).toHaveBeenCalledTimes(2)
    expect(resolveSpy).toHaveBeenCalledExactlyOnceWith(9, true)
    expect(switchSpy).not.toHaveBeenCalled()
    expect(document.querySelector('[style*="2147483647"]')).toBeNull()
  })
})
