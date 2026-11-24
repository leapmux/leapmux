import { fireEvent, render, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { WorkerStatus } from '~/generated/proto/leapmux/v1/worker_pb'
import { AgentSkillsControl } from './AgentSkillsControl'

const mockListAllWorkers = vi.fn()
const mockGetWorkerSkills = vi.fn()
const mockUpdateWorkerSkills = vi.fn()

vi.mock('~/lib/listAllWorkers', () => ({
  listAllWorkers: () => mockListAllWorkers(),
}))

vi.mock('~/api/workerRpc', () => ({
  workerSystem: {
    getWorkerSkills: (...args: unknown[]) => mockGetWorkerSkills(...args),
    updateWorkerSkills: (...args: unknown[]) => mockUpdateWorkerSkills(...args),
  },
}))

function sampleState(overrides: Record<string, unknown> = {}) {
  return {
    autoSync: false,
    targets: ['agents'],
    primarySkillsDir: '/home/dev/.agents/skills',
    skills: [
      {
        name: 'leapmux',
        description: 'Foundational control reference',
        enabled: false,
        state: 'not_installed',
        installedVersion: '',
        bundledVersion: '1.0.0',
        path: '/home/dev/.agents/skills/leapmux/SKILL.md',
      },
      {
        name: 'leapmux-review',
        description: 'Read-only second opinion review',
        enabled: true,
        state: 'up_to_date',
        installedVersion: '1.0.0',
        bundledVersion: '1.0.0',
        path: '/home/dev/.agents/skills/leapmux-review/SKILL.md',
      },
    ],
    ...overrides,
  }
}

describe('AgentSkillsControl', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockListAllWorkers.mockResolvedValue([
      { id: 'w-1', online: true, status: WorkerStatus.ACTIVE },
    ])
    mockGetWorkerSkills.mockResolvedValue(sampleState())
    mockUpdateWorkerSkills.mockImplementation(async (_wid: string, req: Record<string, unknown>) => ({
      state: sampleState({
        autoSync: req.autoSync ?? false,
        targets: req.targets ?? ['agents'],
      }),
    }))
  })

  it('loads skills from the online Worker and toggles auto-sync and individual skills', async () => {
    const screen = render(() => <AgentSkillsControl />)

    await waitFor(() => {
      expect(screen.getByTestId('agent-skill-row-leapmux')).toBeTruthy()
      expect(screen.getByTestId('agent-skill-row-leapmux-review')).toBeTruthy()
    })

    const autoSyncBox = screen.getByTestId('agent-skills-autosync') as HTMLInputElement
    expect(autoSyncBox.checked).toBe(false)
    fireEvent.click(autoSyncBox)

    await waitFor(() => {
      expect(mockUpdateWorkerSkills).toHaveBeenCalledWith('w-1', { autoSync: true })
    })

    const leapmuxToggle = screen.getByTestId('agent-skill-toggle-leapmux') as HTMLInputElement
    fireEvent.click(leapmuxToggle)

    await waitFor(() => {
      expect(mockUpdateWorkerSkills).toHaveBeenCalledWith('w-1', {
        updateEnabledSkills: true,
        enabledSkills: ['leapmux', 'leapmux-review'],
        syncNow: true,
      })
    })

    const geminiTarget = screen.getByTestId('agent-skills-target-gemini') as HTMLInputElement
    expect(geminiTarget.checked).toBe(false)
    fireEvent.click(geminiTarget)

    await waitFor(() => {
      expect(mockUpdateWorkerSkills).toHaveBeenCalledWith('w-1', {
        targets: ['agents', 'gemini'],
        syncNow: true,
      })
    })
  })

  it('shows an empty-state hint when no Worker is online', async () => {
    mockListAllWorkers.mockResolvedValue([])
    const screen = render(() => <AgentSkillsControl />)

    await waitFor(() => {
      expect(screen.getByTestId('agent-skills-no-worker')).toBeTruthy()
    })
    expect(mockGetWorkerSkills).not.toHaveBeenCalled()
  })
})
