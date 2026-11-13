import type { HeldDiffWorker } from '~/test-support/heldDiffWorker'
import { render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { workerFile } from '~/api/workerRpc'
import { diffSplitContainer } from '~/components/chat/diff/diffStyles.css'
import { GitFileRef } from '~/generated/proto/leapmux/v1/git_pb'
import { answerHeldDiffRequest, installHeldDiffWorker, settleDiffReplies } from '~/test-support/heldDiffWorker'
import { platformBridgeFileSaveStubs } from '~/test-support/saveActionsMocks'
import { installControlledSlices } from '~/test-support/sliceSchedulers'
import { readResp } from '~/test-support/workerRpcMocks'
import { content as scrollContainerClass } from './FileViewer.css'
import { versionDiffMounting } from './VersionDiffView.css'

// ---------------------------------------------------------------------------
// The scroll position of a diff on the browser path
//
// A Worker exists here, so the real diff client sends each diff to the stand-in
// worker, and a reply re-runs only the consumers that wait for one. A viewer
// whose diff is ready must keep the position that the user scrolled to when the
// diff of another consumer arrives.
// ---------------------------------------------------------------------------

const readGitFileImpl = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  workerFile: {
    statFile: vi.fn(),
    readFile: vi.fn(async () => readResp(new TextEncoder().encode('working'))),
  },
  workerGit: {
    readGitFile: (...args: unknown[]) => readGitFileImpl(...args),
  },
}))

vi.mock('~/lib/fileDownload', () => ({
  downloadFileFromWorker: vi.fn(),
  saveFileAs: vi.fn(),
  saveFileToDownloads: vi.fn(),
}))

vi.mock('~/context/PreferencesContext', () => ({
  usePreferences: () => ({
    revealAfterDownload: () => false,
    setRevealAfterDownload: () => {},
  }),
}))

vi.mock('~/api/platformBridge', () => ({
  isTauriApp: () => false,
  platformBridge: {
    revealInFileManager: () => Promise.resolve(),
    ...platformBridgeFileSaveStubs(),
  },
}))

// The client spawns its worker at the first request, so the stand-in must be in
// place before the viewer asks for a diff. The stub stays for the whole file:
// the client keeps the worker that it spawned.
const spawned = installHeldDiffWorker()
const { FileViewer } = await import('~/components/fileviewer/FileViewer')
const { requestComputedDiff } = await import('~/components/chat/diff/diffWorkerClient')

/** The one stand-in that holds diff requests: other workers may spawn a stand-in of their own. */
function diffWorker(): HeldDiffWorker {
  const workers = spawned.filter(worker => worker.messages.some(request => request.type === 'diff'))
  expect(workers).toHaveLength(1)
  return workers[0]!
}

/** Lines `row 1` to `row <count>`, each with its newline. */
function rows(count: number): string {
  return Array.from({ length: count }, (_, i) => `row ${i + 1}\n`).join('')
}

describe('FileViewer diff mode', () => {
  it('keeps the scroll position when the diff of another consumer arrives', async () => {
    const head = rows(60)
    const staged = head.replace('row 30\n', 'row 30 changed\n')
    readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
      const request = args[1] as { ref: number }
      const content = new TextEncoder().encode(request.ref === GitFileRef.HEAD
        ? head
        : staged)
      return {
        exists: true,
        content,
        totalSize: BigInt(content.length),
      }
    })
    const { container } = render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/notes.txt"
        fileViewMode="unified-diff"
        fileDiffBase="head-vs-staged"
        gitFileStatus={{ path: 'notes.txt' } as never}
      />
    ))
    // The scroll container holds the diff. Record each position that the viewer sets.
    const scrolledTo: number[] = []
    let scrollTop = 0
    const scroller = container.querySelector<HTMLElement>(`.${scrollContainerClass}`)!
    Object.defineProperty(scroller, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value: number) => {
        scrollTop = value
        scrolledTo.push(value)
      },
    })

    await waitFor(() => expect(readGitFileImpl).toHaveBeenCalledTimes(2))
    await settleDiffReplies()
    // Answer the request for the two versions only. Any other request of the
    // viewer stays pending, so a viewer that still waits for replies re-runs
    // when the other diff arrives, and the test sees it.
    const viewerRequest = diffWorker().messages.find(request => request.input.kind === 'texts' && request.input.oldText === head)
    expect(viewerRequest, 'the viewer asked for the diff of the two versions').toBeDefined()
    answerHeldDiffRequest(diffWorker(), viewerRequest!.input)
    await settleDiffReplies()
    await waitFor(() => expect(scroller.querySelector('[data-file-diff]')).not.toBeNull())
    await new Promise(resolve => requestAnimationFrame(resolve))
    expect(scrolledTo, 'the viewer scrolls to the first change once its diff is ready').toHaveLength(1)

    // The user scrolls, and another consumer's diff arrives.
    scrollTop = 480
    const other = { kind: 'texts' as const, oldText: 'other\n', newText: 'another\n' }
    expect(requestComputedDiff(other)).toBeUndefined()
    await settleDiffReplies()
    answerHeldDiffRequest(diffWorker(), other)
    await settleDiffReplies()
    expect(requestComputedDiff(other), 'the other diff arrived').toBeDefined()
    await new Promise(resolve => requestAnimationFrame(resolve))

    expect(scrolledTo).toHaveLength(1)
    expect(scroller.scrollTop).toBe(480)
  })

  it('shows its loading state, and restores the scroll, only after every line of the diff mounts', async () => {
    const slices = installControlledSlices(10)
    // Every row changes, so the diff draws 120 lines: 60 removals and 60 additions.
    const head = rows(60)
    const staged = head.replaceAll('row', 'line')
    readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
      const request = args[1] as { ref: number }
      const content = new TextEncoder().encode(request.ref === GitFileRef.HEAD
        ? head
        : staged)
      return {
        exists: true,
        content,
        totalSize: BigInt(content.length),
      }
    })
    const { container } = render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/rewritten.txt"
        fileViewMode="unified-diff"
        fileDiffBase="head-vs-staged"
        gitFileStatus={{ path: 'rewritten.txt' } as never}
      />
    ))
    const scrolledTo: number[] = []
    const scroller = container.querySelector<HTMLElement>(`.${scrollContainerClass}`)!
    Object.defineProperty(scroller, 'scrollTop', {
      configurable: true,
      get: () => 0,
      set: (value: number) => {
        scrolledTo.push(value)
      },
    })

    await waitFor(() => expect(readGitFileImpl).toHaveBeenCalledTimes(2))
    await settleDiffReplies()
    const viewerRequest = diffWorker().messages.find(request => request.input.kind === 'texts'
      && request.input.oldText === head
      && request.input.newText === staged)
    expect(viewerRequest, 'the viewer asked for the diff of the two versions').toBeDefined()
    answerHeldDiffRequest(diffWorker(), viewerRequest!.input)
    await settleDiffReplies()

    // The first slice mounted ten lines. The diff stays hidden behind the loading state.
    const diff = scroller.querySelector<HTMLElement>('[data-file-diff]')
    expect(diff).not.toBeNull()
    expect(diff!.closest(`.${versionDiffMounting}[aria-busy="true"]`)).not.toBeNull()
    expect(screen.getByText('Loading...')).toBeInTheDocument()
    expect(scrolledTo).toEqual([])

    slices.runAllSlices()

    expect(diff!.closest(`.${versionDiffMounting}, [aria-busy]`)).toBeNull()
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
    expect(scrolledTo).toHaveLength(1)
  })

  it('keeps the layout that the reader leaves in view while the next layout mounts, and restores the scroll of each layout only once it shows', async () => {
    const slices = installControlledSlices(10)
    const head = rows(80)
    const staged = head.replaceAll('row', 'entry')
    readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
      const request = args[1] as { ref: number }
      const content = new TextEncoder().encode(request.ref === GitFileRef.HEAD
        ? head
        : staged)
      return {
        exists: true,
        content,
        totalSize: BigInt(content.length),
      }
    })
    const [mode, setMode] = createSignal<'unified-diff' | 'split-diff'>('unified-diff')
    const { container } = render(() => (
      <FileViewer
        workerId="w1"
        filePath="/repo/switched.txt"
        fileViewMode={mode()}
        fileDiffBase="head-vs-staged"
        gitFileStatus={{ path: 'switched.txt' } as never}
      />
    ))
    let scrollTop = 0
    const scrolledTo: number[] = []
    const scroller = container.querySelector<HTMLElement>(`.${scrollContainerClass}`)!
    Object.defineProperty(scroller, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value: number) => {
        scrollTop = value
        scrolledTo.push(value)
      },
    })
    /** Whether the diff that the viewer shows draws the split layout. */
    const showsSplit = () => scroller.querySelector('[data-file-diff]')!.classList.contains(diffSplitContainer)
    await waitFor(() => expect(readGitFileImpl).toHaveBeenCalledTimes(2))
    await settleDiffReplies()
    const viewerRequest = diffWorker().messages.find(request => request.input.kind === 'texts'
      && request.input.oldText === head
      && request.input.newText === staged)
    expect(viewerRequest, 'the viewer asked for the diff of the two versions').toBeDefined()
    answerHeldDiffRequest(diffWorker(), viewerRequest!.input)
    await settleDiffReplies()
    slices.runAllSlices()

    // The reader scrolls the unified layout, then switches to the split layout.
    // The unified layout stays in view while the split one mounts behind it,
    // and the viewer restores no scroll until the split layout shows.
    scrollTop = 480
    scrolledTo.length = 0
    setMode('split-diff')
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
    expect(showsSplit()).toBe(false)
    expect(scrolledTo).toEqual([])

    slices.runAllSlices()

    expect(showsSplit()).toBe(true)
    // The split layout has no saved position, so the viewer scrolls to its first change.
    expect(scrolledTo).toHaveLength(1)

    setMode('unified-diff')
    expect(showsSplit()).toBe(true)
    expect(scrolledTo).toHaveLength(1)
    slices.runAllSlices()

    expect(showsSplit()).toBe(false)
    expect(scroller.scrollTop).toBe(480)
  })

  // The tile passes each prop of the viewer through a getter over the whole tab
  // object (see TileRenderer), and a patch of one field replaces that object. So
  // a switch of the view mode re-reads the worker and the path too, with the
  // same values, and so does every later patch of the tab.
  it('reads no version again, shows no loading state, and keeps the reader\'s position, when the tab that holds it changes', async () => {
    const slices = installControlledSlices(10)
    const head = rows(80)
    const staged = head.replaceAll('row', 'item')
    readGitFileImpl.mockImplementation(async (...args: unknown[]) => {
      const request = args[1] as { ref: number }
      const content = new TextEncoder().encode(request.ref === GitFileRef.HEAD
        ? head
        : staged)
      return {
        exists: true,
        content,
        totalSize: BigInt(content.length),
      }
    })
    const tabWith = (fileViewMode: 'unified-diff' | 'split-diff') => ({
      workerId: 'w1',
      filePath: '/repo/tabbed.txt',
      fileViewMode,
    })
    const [tab, setTab] = createSignal(tabWith('unified-diff'))
    const { container } = render(() => (
      <FileViewer
        workerId={tab().workerId}
        filePath={tab().filePath}
        fileViewMode={tab().fileViewMode}
        fileDiffBase="head-vs-staged"
        gitFileStatus={{ path: 'tabbed.txt' } as never}
      />
    ))
    let scrollTop = 0
    const scrolledTo: number[] = []
    const scroller = container.querySelector<HTMLElement>(`.${scrollContainerClass}`)!
    Object.defineProperty(scroller, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value: number) => {
        scrollTop = value
        scrolledTo.push(value)
      },
    })
    const showsSplit = () => scroller.querySelector('[data-file-diff]')!.classList.contains(diffSplitContainer)
    await waitFor(() => expect(readGitFileImpl).toHaveBeenCalledTimes(2))
    await settleDiffReplies()
    const viewerRequest = diffWorker().messages.find(request => request.input.kind === 'texts'
      && request.input.oldText === head
      && request.input.newText === staged)
    expect(viewerRequest, 'the viewer asked for the diff of the two versions').toBeDefined()
    answerHeldDiffRequest(diffWorker(), viewerRequest!.input)
    await settleDiffReplies()
    slices.runAllSlices()
    await new Promise(resolve => requestAnimationFrame(resolve))
    const readsBefore = {
      file: vi.mocked(workerFile.readFile).mock.calls.length,
      git: readGitFileImpl.mock.calls.length,
    }

    scrollTop = 480
    scrolledTo.length = 0
    setTab(tabWith('split-diff'))
    await settleDiffReplies()

    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
    expect(showsSplit()).toBe(false)
    slices.runAllSlices()
    expect(showsSplit()).toBe(true)
    await new Promise(resolve => requestAnimationFrame(resolve))
    // The split layout has no saved position, so the viewer scrolls to its first change once.
    expect(scrolledTo).toHaveLength(1)

    // A later patch of the tab, with the same values, restores no position again.
    scrollTop = 120
    scrolledTo.length = 0
    setTab(tabWith('split-diff'))
    await settleDiffReplies()
    await new Promise(resolve => requestAnimationFrame(resolve))

    expect(scrolledTo).toEqual([])
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
    expect({
      file: vi.mocked(workerFile.readFile).mock.calls.length,
      git: readGitFileImpl.mock.calls.length,
    }).toEqual(readsBefore)
  })
})
