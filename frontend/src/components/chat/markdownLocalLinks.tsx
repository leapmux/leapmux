import type { ContextProviderComponent } from 'solid-js'
import type { LocalLinkResolveOptions } from '~/lib/localLinks'
import type { PathFlavor } from '~/lib/paths'
import { createMemo, useContext } from 'solid-js'
import { createStableContext } from '~/lib/createStableContext'
import { flavorFromOs } from '~/lib/paths'

/**
 * Snapshot passed to a local-link activation callback when a user activates a
 * local link or standalone local image in rendered Markdown.
 */
export interface MarkdownLocalLinkActivationSource extends LocalLinkResolveOptions {
  workerId: string
  /** Bring the originating tile/tab into focus before opening or revealing. */
  activateSource?: () => void
}

/**
 * Ambient context for Markdown rendered inside a surface tied to a worker
 * (agent tab, file viewer tab, terminal tab, or sidebar Goals & To-dos).
 *
 * Callers should populate every property with a reactive getter (via
 * {@link createMarkdownLocalLinkContext}) so changes to a tab's `workingDir`,
 * `filePath`, or `workerId` propagate to descendant `MarkdownText` instances
 * without remounting the pane or re-running effects on unrelated tab metadata.
 */
export interface MarkdownLocalLinkContextValue extends Readonly<LocalLinkResolveOptions> {
  readonly workerId: string
  readonly onActivate: (rawHref: string) => void
}

export interface CreateMarkdownLocalLinkContextSpec {
  workerId: () => string
  baseDir: (flavor: PathFlavor) => string
  workingDir: () => string
  homeDir: () => string
  workerOs: (workerId: string) => string | undefined
  getOnActivate: () => ((rawHref: string, source: MarkdownLocalLinkActivationSource) => void) | undefined
  activateSource?: () => void
}

/**
 * Build a stable context object backed by per-field `createMemo`s so unrelated
 * tab metadata updates (status flips, title renames, unread badges, MRU stamps)
 * do not notify descendant `MarkdownText` hydration effects, while changes to
 * `getOnActivate()` still toggle interactivity reactively without remounting.
 */
export function createMarkdownLocalLinkContext(
  spec: CreateMarkdownLocalLinkContextSpec,
): MarkdownLocalLinkContextValue {
  const hasActivator = createMemo(() => spec.getOnActivate() !== undefined)
  const workerId = createMemo(() => (hasActivator()
    ? spec.workerId()
    : ''))
  const flavor = createMemo<PathFlavor>(() => flavorFromOs(spec.workerOs(workerId())))
  const baseDir = createMemo(() => spec.baseDir(flavor()))
  const workingDir = createMemo(() => spec.workingDir())
  const homeDir = createMemo(() => spec.homeDir())

  return {
    get workerId() { return workerId() },
    get baseDir() { return baseDir() },
    get workingDir() { return workingDir() },
    get homeDir() { return homeDir() },
    get flavor() { return flavor() },
    onActivate: (rawHref: string) => {
      const handler = spec.getOnActivate()
      if (!handler)
        return
      const currentWorkerId = spec.workerId()
      const currentFlavor = flavorFromOs(spec.workerOs(currentWorkerId))
      handler(rawHref, {
        workerId: currentWorkerId,
        baseDir: spec.baseDir(currentFlavor),
        workingDir: spec.workingDir(),
        homeDir: spec.homeDir(),
        flavor: currentFlavor,
        ...(spec.activateSource
          ? { activateSource: spec.activateSource }
          : {}),
      })
    },
  }
}

/**
 * Wrap a parent local-link context so an overlay host (such as a popover) can
 * run a dismissal callback immediately before forwarding local-link activation,
 * while preserving every reactive field getter on `parent`.
 */
export function withMarkdownLocalLinkBeforeActivate(
  parent: MarkdownLocalLinkContextValue | undefined,
  beforeActivate: () => void,
): MarkdownLocalLinkContextValue | undefined {
  if (!parent)
    return undefined
  return {
    get workerId() { return parent.workerId },
    get baseDir() { return parent.baseDir },
    get workingDir() { return parent.workingDir },
    get homeDir() { return parent.homeDir },
    get flavor() { return parent.flavor },
    onActivate: (rawHref: string) => {
      beforeActivate()
      parent.onActivate(rawHref)
    },
  }
}

const MarkdownLocalLinkContext = createStableContext<MarkdownLocalLinkContextValue | undefined>(
  'chat/markdownLocalLinks',
  undefined,
)

/**
 * Provide local-link and local-image resolution context to descendant
 * `MarkdownText` components. Pass `value={undefined}` to opt a nested subtree
 * (such as a non-interactive hover tooltip or dot preview card) out of an
 * ancestor provider.
 */
export const MarkdownLocalLinkProvider: ContextProviderComponent<MarkdownLocalLinkContextValue | undefined>
  = MarkdownLocalLinkContext.Provider

/**
 * Read the nearest `MarkdownLocalLinkProvider` value, or `undefined` when
 * rendered outside any worker-associated surface.
 */
export function useMarkdownLocalLinks(): MarkdownLocalLinkContextValue | undefined {
  return useContext(MarkdownLocalLinkContext)
}
