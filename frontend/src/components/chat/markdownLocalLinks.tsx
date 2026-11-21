import type { ContextProviderComponent } from 'solid-js'
import type { PathFlavor } from '~/lib/paths'
import { useContext } from 'solid-js'
import { createStableContext } from '~/lib/createStableContext'

/**
 * Ambient context for Markdown rendered inside a surface tied to a worker
 * (agent tab, file viewer tab, terminal tab, or sidebar Goals & To-dos).
 *
 * Callers should populate every property with a reactive getter so changes to
 * a tab's `workingDir`, `filePath`, or `workerId` propagate to descendant
 * `MarkdownText` instances without remounting the pane.
 */
export interface MarkdownLocalLinkContextValue {
  readonly workerId: string
  readonly baseDir: string
  readonly workingDir: string
  readonly homeDir: string
  readonly flavor: PathFlavor
  readonly onActivate: (rawHref: string) => void
}

const MarkdownLocalLinkContext = createStableContext<MarkdownLocalLinkContextValue | undefined>(
  'chat/markdownLocalLinks',
  undefined,
)

/**
 * Provide local-link and local-image resolution context to descendant
 * `MarkdownText` components. Pass `value={undefined}` to opt a nested subtree
 * (such as a non-interactive hover tooltip) out of an ancestor provider.
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
