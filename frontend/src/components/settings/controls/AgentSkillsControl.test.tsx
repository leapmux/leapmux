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

interface SampleTargetStatus {
  target: string
  state: string
  installedVersion: string
  path: string
}

interface SampleSkillStatus {
  name: string
  description: string
  enabled: boolean
  state: string
  installedVersion: string
  bundledVersion: string
  path: string
  references: string[]
  targets: SampleTargetStatus[]
}

function sampleState(overrides: Record<string, unknown> = {}) {
  const skills: SampleSkillStatus[] = [
    {
      name: 'leapmux',
      description: 'Foundational control reference',
      enabled: false,
      state: 'not_installed',
      installedVersion: '',
      bundledVersion: '1.0.0',
      path: '/home/dev/.agents/skills/leapmux/SKILL.md',
      references: [],
      targets: [
        {
          target: 'agents',
          state: 'not_installed',
          installedVersion: '',
          path: '/home/dev/.agents/skills/leapmux/SKILL.md',
        },
      ],
    },
    {
      name: 'leapmux-review',
      description: 'Read-only second opinion review',
      enabled: true,
      state: 'up_to_date',
      installedVersion: '1.0.0',
      bundledVersion: '1.0.0',
      path: '/home/dev/.agents/skills/leapmux-review/SKILL.md',
      references: [],
      targets: [
        {
          target: 'agents',
          state: 'up_to_date',
          installedVersion: '1.0.0',
          path: '/home/dev/.agents/skills/leapmux-review/SKILL.md',
        },
      ],
    },
  ]
  return {
    autoSync: false,
    targets: ['agents'],
    primarySkillsDir: '/home/dev/.agents/skills',
    skills,
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
    mockUpdateWorkerSkills.mockImplementation(async (_wid: string, req: Record<string, unknown>) => {
      const current = sampleState()
      const targets = new Set(current.targets)
      for (const t of (req.addTargets as string[] | undefined) ?? [])
        targets.add(t)
      for (const t of (req.removeTargets as string[] | undefined) ?? [])
        targets.delete(t)
      const addSkills = new Set((req.addSkills as string[] | undefined) ?? [])
      const removeSkills = new Set((req.removeSkills as string[] | undefined) ?? [])
      return {
        state: sampleState({
          autoSync: req.autoSync ?? current.autoSync,
          targets: Array.from(targets),
          skills: current.skills.map((skill) => {
            if (addSkills.has(skill.name))
              return { ...skill, enabled: true }
            if (removeSkills.has(skill.name))
              return { ...skill, enabled: false }
            return skill
          }),
        }),
      }
    })
  })

  it('sends atomic delta updates when toggling auto-sync, skills, and targets', async () => {
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
        addSkills: ['leapmux'],
        syncNow: true,
      })
    })

    const reviewToggle = screen.getByTestId('agent-skill-toggle-leapmux-review') as HTMLInputElement
    expect(reviewToggle.checked).toBe(true)
    fireEvent.click(reviewToggle)

    await waitFor(() => {
      expect(mockUpdateWorkerSkills).toHaveBeenCalledWith('w-1', {
        removeSkills: ['leapmux-review'],
      })
    })

    const agentsTarget = screen.getByTestId('agent-skills-target-agents') as HTMLInputElement
    expect(agentsTarget.checked).toBe(true)
    expect(agentsTarget.disabled).toBe(true)

    const geminiTarget = screen.getByTestId('agent-skills-target-gemini') as HTMLInputElement
    expect(geminiTarget.checked).toBe(false)
    fireEvent.click(geminiTarget)

    await waitFor(() => {
      expect(mockUpdateWorkerSkills).toHaveBeenCalledWith('w-1', {
        addTargets: ['gemini'],
        syncNow: true,
      })
    })

    await waitFor(() => {
      expect((screen.getByTestId('agent-skills-target-gemini') as HTMLInputElement).checked).toBe(true)
      expect((screen.getByTestId('agent-skills-target-agents') as HTMLInputElement).disabled).toBe(false)
    })

    fireEvent.click(screen.getByTestId('agent-skills-target-gemini'))

    await waitFor(() => {
      expect(mockUpdateWorkerSkills).toHaveBeenCalledWith('w-1', {
        removeTargets: ['gemini'],
      })
    })

    fireEvent.click(screen.getByTestId('agent-skills-sync-now'))

    await waitFor(() => {
      expect(mockUpdateWorkerSkills).toHaveBeenCalledWith('w-1', {
        syncNow: true,
      })
    })
  })

  it('surfaces secondary-target drift alongside the affected target name', async () => {
    mockGetWorkerSkills.mockResolvedValue(sampleState({
      targets: ['agents', 'claude'],
      skills: [
        {
          name: 'leapmux',
          description: 'Foundational control reference',
          enabled: true,
          state: 'up_to_date',
          installedVersion: '1.0.0',
          bundledVersion: '1.0.0',
          path: '/home/dev/.agents/skills/leapmux/SKILL.md',
          references: [],
          targets: [
            {
              target: 'agents',
              state: 'up_to_date',
              installedVersion: '1.0.0',
              path: '/home/dev/.agents/skills/leapmux/SKILL.md',
            },
            {
              target: 'claude',
              state: 'up_to_date',
              installedVersion: '1.0.0',
              path: '/home/dev/.claude/skills/leapmux/SKILL.md',
            },
          ],
        },
        {
          name: 'leapmux-review',
          description: 'Read-only second opinion review',
          enabled: true,
          state: 'modified',
          installedVersion: '1.0.0',
          bundledVersion: '1.0.0',
          path: '/home/dev/.agents/skills/leapmux-review/SKILL.md',
          references: [],
          targets: [
            {
              target: 'agents',
              state: 'up_to_date',
              installedVersion: '1.0.0',
              path: '/home/dev/.agents/skills/leapmux-review/SKILL.md',
            },
            {
              target: 'claude',
              state: 'modified',
              installedVersion: '1.0.0',
              path: '/home/dev/.claude/skills/leapmux-review/SKILL.md',
            },
          ],
        },
      ],
    }))

    const screen = render(() => <AgentSkillsControl />)

    await waitFor(() => {
      expect(screen.getByTestId('agent-skill-state-leapmux-review')).toBeTruthy()
    })

    expect(screen.getByTestId('agent-skill-state-leapmux')).toHaveTextContent('Installed')
    expect(screen.queryByTestId('agent-skill-targets-leapmux')).toBeNull()

    expect(screen.getByTestId('agent-skill-state-leapmux-review')).toHaveTextContent('Modified locally (claude)')
    expect(screen.queryByTestId('agent-skill-targets-leapmux-review')).toBeNull()
  })

  it('shows a per-target detail line when multiple targets diverge across non-installed states', async () => {
    mockGetWorkerSkills.mockResolvedValue(sampleState({
      targets: ['agents', 'claude', 'codex'],
      skills: [
        {
          name: 'leapmux-review',
          description: 'Read-only second opinion review',
          enabled: true,
          state: 'modified',
          installedVersion: '1.0.0',
          bundledVersion: '1.0.0',
          path: '/home/dev/.agents/skills/leapmux-review/SKILL.md',
          references: [],
          targets: [
            {
              target: 'agents',
              state: 'up_to_date',
              installedVersion: '1.0.0',
              path: '/home/dev/.agents/skills/leapmux-review/SKILL.md',
            },
            {
              target: 'claude',
              state: 'modified',
              installedVersion: '1.0.0',
              path: '/home/dev/.claude/skills/leapmux-review/SKILL.md',
            },
            {
              target: 'codex',
              state: 'outdated',
              installedVersion: '0.9.0',
              path: '/home/dev/.codex/skills/leapmux-review/SKILL.md',
            },
          ],
        },
      ],
    }))

    const screen = render(() => <AgentSkillsControl />)

    await waitFor(() => {
      expect(screen.getByTestId('agent-skill-state-leapmux-review')).toBeTruthy()
    })

    expect(screen.getByTestId('agent-skill-state-leapmux-review')).toHaveTextContent('Modified locally (claude)')
    expect(screen.getByTestId('agent-skill-targets-leapmux-review')).toHaveTextContent(
      'agents: Installed · codex: Update available',
    )
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
