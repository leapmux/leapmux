import type { SectionDefContext } from './buildSectionDef'
import type { MarkdownLocalLinkContextValue } from '~/components/chat/markdownLocalLinks'
import type { FilesSectionProps } from '~/components/tree/FilesSection'
import { create } from '@bufbuild/protobuf'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useMarkdownLocalLinks } from '~/components/chat/markdownLocalLinks'
import { SectionSchema, SectionType } from '~/generated/proto/leapmux/v1/section_pb'
import { buildSectionDef, SECTION_DEFAULT_SIZES } from './buildSectionDef'

const captured = vi.hoisted(() => ({
  todosMounts: 0,
  localLinkCtx: undefined as MarkdownLocalLinkContextValue | undefined,
  filesProps: undefined as FilesSectionProps | undefined,
}))

vi.mock('~/components/todo/GoalsAndTodos', () => ({
  GoalsAndTodos: () => {
    captured.todosMounts += 1
    captured.localLinkCtx = useMarkdownLocalLinks()
    const el = document.createElement('div')
    el.dataset.testid = 'mock-goals-and-todos'
    return el
  },
}))

vi.mock('~/components/tree/FilesSection', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/components/tree/FilesSection')>()
  return {
    ...actual,
    FilesSection: (props: FilesSectionProps) => {
      captured.filesProps = props
      const el = document.createElement('div')
      el.dataset.testid = 'mock-files-section'
      return el
    },
  }
})

/** A context with only the fields the section under test reads. Preserves getter descriptors on `over`. */
function context(over: Partial<SectionDefContext> = {}): SectionDefContext {
  const base = {
    activeTodos: [],
    activeGoal: {
      progress: {},
      actions: ['set'],
    },
    showGoalsAndTodos: true,
    showBackgroundTasks: true,
    activeBackgroundTasks: [],
    activeBackgroundTasksFailed: false,
  }
  return Object.defineProperties(base, Object.getOwnPropertyDescriptors(over)) as unknown as SectionDefContext
}

function sectionOf(sectionType: SectionType, name: string) {
  return create(SectionSchema, { id: `s-${sectionType}`, name, sectionType })
}

describe('section default sizes', () => {
  /**
   * The definition each sidebar actually reads, not the table's own literals.
   * Asserting the constant against the numbers it declares three lines away
   * exercises nothing: deleting every `defaultSize:` line still passed.
   */
  it('puts the declared size of each section type on its definition', () => {
    expect(buildSectionDef(sectionOf(SectionType.TODOS, 'Goals & To-dos'), context()).defaultSize)
      .toBe(SECTION_DEFAULT_SIZES[SectionType.TODOS])
    expect(buildSectionDef(sectionOf(SectionType.BACKGROUND_TASKS, 'Background tasks'), context()).defaultSize)
      .toBe(SECTION_DEFAULT_SIZES[SectionType.BACKGROUND_TASKS])
  })

  it('uses the requested 0.55:0.20:0.25 right-sidebar ratio', () => {
    expect([
      SECTION_DEFAULT_SIZES[SectionType.FILES],
      SECTION_DEFAULT_SIZES[SectionType.TODOS],
      SECTION_DEFAULT_SIZES[SectionType.BACKGROUND_TASKS],
    ]).toEqual([0.55, 0.2, 0.25])
  })

  // A workspace section declares nothing, and distributeSectionSizes gives it a
  // share of what the declared ones leave. A number here would over-subscribe
  // the sidebar the moment a second workspace section appeared.
  it('declares no size for a workspace section', () => {
    expect(SECTION_DEFAULT_SIZES[SectionType.WORKSPACES_CUSTOM]).toBeUndefined()
  })

  it('renders no 0/0 rail badge for a goal-only section', () => {
    const definition = buildSectionDef(sectionOf(SectionType.TODOS, 'Goals & To-dos'), context())
    const { container } = render(() => definition.railBadge?.())
    expect(container.textContent).toBe('')
  })
})

describe('local link context and directory reveal forwarding', () => {
  beforeEach(() => {
    captured.todosMounts = 0
    captured.localLinkCtx = undefined
    captured.filesProps = undefined
  })

  it('provides a reactive MarkdownLocalLinkProvider to GoalsAndTodos when onActivateLocalLink is present', () => {
    const [workerId, setWorkerId] = createSignal('w-1')
    const [workingDir, setWorkingDir] = createSignal('/repo-a')
    const [homeDir, setHomeDir] = createSignal('/home/alice')
    const [os, setOs] = createSignal('linux')
    const onActivateLocalLink = vi.fn()

    const ctx = context({
      get workerId() { return workerId() },
      get workingDir() { return workingDir() },
      get homeDir() { return homeDir() },
      workerInfoFn: () => ({
        os: os(),
      } as ReturnType<SectionDefContext['workerInfoFn']>),
      onActivateLocalLink,
    })

    const definition = buildSectionDef(sectionOf(SectionType.TODOS, 'Goals & To-dos'), ctx)
    render(() => definition.content())

    expect(captured.todosMounts).toBe(1)
    expect(captured.localLinkCtx).toBeDefined()
    expect(captured.localLinkCtx?.workerId).toBe('w-1')
    expect(captured.localLinkCtx?.baseDir).toBe('/repo-a')
    expect(captured.localLinkCtx?.workingDir).toBe('/repo-a')
    expect(captured.localLinkCtx?.homeDir).toBe('/home/alice')
    expect(captured.localLinkCtx?.flavor).toBe('posix')

    // Reactive updates propagate through getters without remounting GoalsAndTodos.
    setWorkerId('w-2')
    setWorkingDir('C:\\RepoB')
    setHomeDir('C:\\Users\\Alice')
    setOs('windows')

    expect(captured.todosMounts).toBe(1)
    expect(captured.localLinkCtx?.workerId).toBe('w-2')
    expect(captured.localLinkCtx?.baseDir).toBe('C:\\RepoB')
    expect(captured.localLinkCtx?.workingDir).toBe('C:\\RepoB')
    expect(captured.localLinkCtx?.homeDir).toBe('C:\\Users\\Alice')
    expect(captured.localLinkCtx?.flavor).toBe('win32')

    captured.localLinkCtx?.onActivate('./notes.md')
    expect(onActivateLocalLink).toHaveBeenCalledWith('./notes.md', {
      workerId: 'w-2',
      baseDir: 'C:\\RepoB',
      workingDir: 'C:\\RepoB',
      homeDir: 'C:\\Users\\Alice',
      flavor: 'win32',
    })
  })

  it('provides undefined to GoalsAndTodos when onActivateLocalLink is absent', () => {
    const ctx = context({
      workerId: 'w-1',
      workingDir: '/repo',
      homeDir: '/home/alice',
      workerInfoFn: () => ({ os: 'linux' } as ReturnType<SectionDefContext['workerInfoFn']>),
      onActivateLocalLink: undefined,
    })

    const definition = buildSectionDef(sectionOf(SectionType.TODOS, 'Goals & To-dos'), ctx)
    render(() => definition.content())

    expect(captured.todosMounts).toBe(1)
    expect(captured.localLinkCtx).toBeUndefined()
  })

  it('forwards directoryReveal and onDirectoryRevealHandled to FilesSection', () => {
    const [reveal, setReveal] = createSignal<{ path: string, nonce: number } | undefined>({
      path: '/repo/docs',
      nonce: 3,
    })
    const onDirectoryRevealHandled = vi.fn()
    const ctx = context({
      workerId: 'w-1',
      workingDir: '/repo',
      homeDir: '/home/alice',
      fileTreePath: '/repo',
      onFileSelect: () => {},
      activeTabReady: true,
      workerInfoFn: () => ({ os: 'linux' } as ReturnType<SectionDefContext['workerInfoFn']>),
      setFilesSectionHandle: () => {},
      get directoryReveal() { return reveal() },
      onDirectoryRevealHandled,
    })

    const definition = buildSectionDef(sectionOf(SectionType.FILES, 'Files'), ctx)
    render(() => definition.content())

    expect(captured.filesProps?.directoryReveal).toEqual({ path: '/repo/docs', nonce: 3 })
    expect(captured.filesProps?.onDirectoryRevealHandled).toBe(onDirectoryRevealHandled)

    setReveal({ path: '/repo/src', nonce: 4 })
    expect(captured.filesProps?.directoryReveal).toEqual({ path: '/repo/src', nonce: 4 })
  })
})
