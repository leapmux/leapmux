import type { SoloBusyWork } from '~/api/platformBridge'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { platformBridge } from '~/api/platformBridge'
import {
  confirmSoloSwitchMode,
  isSoloBusyWorkEmpty,
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
  it('resolves true without calling the prompter when busy work is empty', async () => {
    vi.spyOn(platformBridge, 'inspectSoloBusyWork').mockResolvedValue(emptyWork)
    const prompter = vi.fn(async () => false)
    setSoloSwitchModePrompter(prompter)

    await expect(confirmSoloSwitchMode()).resolves.toBe(true)
    expect(prompter).not.toHaveBeenCalled()
  })

  it('forwards the registered prompter answer when busy work exists', async () => {
    vi.spyOn(platformBridge, 'inspectSoloBusyWork').mockResolvedValue(busyWork)
    const prompter = vi.fn<(_busy: SoloBusyWork) => Promise<boolean>>()
      .mockResolvedValueOnce(false)
      .mockResolvedValueOnce(true)
    setSoloSwitchModePrompter(prompter)

    await expect(confirmSoloSwitchMode()).resolves.toBe(false)
    await expect(confirmSoloSwitchMode()).resolves.toBe(true)
    expect(prompter).toHaveBeenCalledTimes(2)
    expect(prompter).toHaveBeenCalledWith(busyWork)
  })

  it('fails open when no prompter is registered or when the prompter throws or rejects', async () => {
    vi.spyOn(platformBridge, 'inspectSoloBusyWork').mockResolvedValue(busyWork)

    // No prompter registered.
    await expect(confirmSoloSwitchMode()).resolves.toBe(true)

    // Rejecting prompter fails open and clears inFlight.
    setSoloSwitchModePrompter(async () => {
      throw new Error('prompt failed asynchronously')
    })
    await expect(confirmSoloSwitchMode()).resolves.toBe(true)

    // Synchronously throwing prompter fails open and clears inFlight.
    setSoloSwitchModePrompter(() => {
      throw new Error('prompt failed synchronously')
    })
    await expect(confirmSoloSwitchMode()).resolves.toBe(true)

    // Healthy prompter works on the next call.
    const healthy = vi.fn(async () => false)
    setSoloSwitchModePrompter(healthy)
    await expect(confirmSoloSwitchMode()).resolves.toBe(false)
    expect(healthy).toHaveBeenCalledOnce()
  })

  it('coalesces concurrent calls and starts fresh after settlement', async () => {
    let resolveInspect!: (value: SoloBusyWork) => void
    const inspectSpy = vi.spyOn(platformBridge, 'inspectSoloBusyWork')
      .mockImplementationOnce(() => new Promise<SoloBusyWork>((resolve) => {
        resolveInspect = resolve
      }))
      .mockResolvedValueOnce(busyWork)

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
    expect(inspectSpy).toHaveBeenCalledOnce()

    resolveInspect(busyWork)
    await vi.waitFor(() => expect(prompter).toHaveBeenCalledOnce())
    resolvePrompt(false)

    await expect(first).resolves.toBe(false)
    await expect(second).resolves.toBe(false)

    await expect(confirmSoloSwitchMode()).resolves.toBe(true)
    expect(inspectSpy).toHaveBeenCalledTimes(2)
    expect(prompter).toHaveBeenCalledTimes(2)
  })
})
