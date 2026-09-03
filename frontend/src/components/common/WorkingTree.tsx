import type { LucideProps } from 'lucide-solid'
import type { Component, JSX } from 'solid-js'
import type { IconSizeName } from '~/components/common/Icon'
import type { PathFlavor } from '~/lib/paths'
import type { DiffStats } from '~/stores/repoGit'
import GitBranch from 'lucide-solid/icons/git-branch'
import { Show, splitProps } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { SvgIconFrame } from '~/components/common/SvgIconFrame'
import { Tooltip } from '~/components/common/Tooltip'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { tildify } from '~/lib/paths'
import * as styles from './WorkingTree.css'

/**
 * One vocabulary for the two kinds of checkout LeapMux opens tabs in.
 *
 * Git's own terms: a repository has ONE main working tree -- the repository's
 * own directory -- and zero or more LINKED working trees that `git worktree
 * add` creates. `isWorktree` is true for a linked one. The words this app shows
 * the user are `Worktree` for a linked working tree and `Branch` for the main
 * one, because "branch" is what a user perceives when a repository has only the
 * one directory.
 *
 * Every surface that names a checkout reads the icon, the noun, the delete
 * label and the rows from here: the sidebar branch row, the composer chip, the
 * composer `[+]` menu, the branch context menu, both branch dialogs and the
 * agent info card. A second spelling anywhere is how one surface starts calling
 * a worktree a branch while its neighbour does not.
 */

/** True when the checkout is a linked worktree rather than the main working tree. */
export interface WorkingTreeKind {
  isWorktree: boolean
}

/**
 * The noun for the kind of checkout, on its own.
 *
 * The glyph's accessible name and the delete label are built from this. It is
 * NOT the label of a row whose value is a branch name — see
 * `workingTreeBranchRowLabel`.
 *
 * Title case, because every call site starts a label or a heading with it. A
 * mid-sentence noun is NOT a `.toLowerCase()` of this: ask the module for the
 * label you need, so one edit changes every surface.
 */
export function workingTreeKindLabel(isWorktree: boolean): string {
  return isWorktree ? 'Worktree' : 'Branch'
}

/**
 * The label for a row whose VALUE is the branch name.
 *
 * It states the kind AND what the value is, because those are two different
 * facts and the bare kind stated only one. `Worktree` against `feature/auth`
 * reads as though the value were the worktree — a directory name — and the
 * agent info card is the one surface with no `Directory` row beneath it to
 * settle the question.
 *
 * A worktree has a branch checked out like any other checkout, so the row is
 * `Worktree branch` there and plain `Branch` in the main checkout. The
 * asymmetry is the point: it still tells the two kinds apart at a glance,
 * which is what the glyph beside it repeats.
 */
export function workingTreeBranchRowLabel(isWorktree: boolean): string {
  return isWorktree ? 'Worktree branch' : 'Branch'
}

/**
 * The delete action's label, named after what it removes.
 *
 * The menu item, the dialog title and the red submit button all read this, so
 * the three cannot drift apart. Deleting a worktree removes a whole directory
 * and deleting a branch does not, which is why the noun is part of the label
 * rather than a detail inside the dialog.
 */
export function workingTreeDeleteLabel(isWorktree: boolean): string {
  return isWorktree ? 'Delete worktree' : 'Delete branch'
}

export interface WorkingTreeIconProps extends WorkingTreeKind {
  size: IconSizeName
  class?: string
  /**
   * The kind in words, for a call site where no text states it.
   *
   * Omit it wherever the noun is already on screen beside the glyph, which is
   * every site but the sidebar branch row. That row is a plain `div` with no
   * `tabindex`, so its tooltip opens under a pointer alone and the kind would
   * otherwise reach a screen-reader user nowhere.
   *
   * `role="img"` travels with the label because `aria-label` alone, on an
   * element with no role, maps to ARIA's `generic` and a screen reader may drop
   * it. `StatusDot` states the same rule for the same reason.
   */
  label?: string
}

/**
 * lucide's `git-branch`, with both nodes solid.
 *
 * It is redrawn rather than imported because the fill goes on the two circles,
 * and a lucide icon takes no attribute for one element of its own path list.
 * The path data is lucide's, unchanged, so the two glyphs align exactly: filled
 * nodes are the ONLY difference a reader sees between them.
 *
 * The stroke and the fill read one paint, because `SvgIconFrame` resolves
 * `color` for the stroke alone. Two spellings of the same colour here are a
 * divergence that shows up only under a caller that passes `color`, and that
 * caller would see an outline in the colour it asked for around nodes in the
 * inherited text colour.
 */
function GitBranchFilled(props: LucideProps) {
  const [local, rest] = splitProps(props, ['color'])
  const paint = () => local.color ?? 'currentColor'
  return (
    <SvgIconFrame color={paint()} {...rest}>
      <path d="M15 6a9 9 0 0 0-9 9V3" />
      <circle cx="18" cy="6" r="3" fill={paint()} />
      <circle cx="6" cy="18" r="3" fill={paint()} />
    </SvgIconFrame>
  )
}

/**
 * The glyph that tells the two kinds apart at a glance.
 *
 * One silhouette, two weights: `GitBranch` for the main working tree, and the
 * same branch with solid nodes for a linked worktree. A worktree IS that branch
 * -- checked out a second time, in a directory of its own -- so the pair reads
 * as one thing in two states, and a reader learns no second shape.
 *
 * The pair must separate at 14px in the sidebar branch row and at 12px in the
 * composer chip, which is what rules the alternatives out. A mark added to the
 * branch tip -- a chevron, a square, a small folder, a dashed trunk -- vanishes
 * at both sizes. Any folder shape repeats the `FolderGit` silhouette that the
 * repo group row one level up already draws, which is the defect `FolderSymlink`
 * had here. Fill is the one difference that survives 12px.
 *
 * `GitBranchPlus` is not this glyph, although it looks like the obvious choice.
 * A plus means "create": lucide ships `GitBranchPlus` and `GitBranchMinus` as
 * the create/delete pair, this app has a Create worktree action that wants it
 * (`GitMode.CreateWorktree`), and `ComposerPlusMenu` renders this glyph beside
 * a literal `Plus`. The plus is also a smudge at 14px.
 */
export const WorkingTreeIcon: Component<WorkingTreeIconProps> = props => (
  <Icon
    icon={props.isWorktree ? GitBranchFilled : GitBranch}
    size={props.size}
    class={props.class}
    data-testid={props.isWorktree ? 'worktree-icon' : 'branch-icon'}
    // A CONDITIONAL SPREAD, never `role={props.label ? 'img' : undefined}`.
    // Both glyphs decide `aria-hidden` by testing whether a `role`/`aria-*` key
    // is PRESENT, not by its value -- lucide-solid for `GitBranch`, and
    // `SvgIconFrame` for the filled fork, which copies lucide's rule so the two
    // behave alike. A `role: undefined` key drops the automatic
    // `aria-hidden="true"` and puts every decorative glyph into the
    // accessibility tree with no name.
    {...(props.label ? { 'role': 'img', 'aria-label': props.label } : {})}
  />
)

/**
 * Everything a surface needs to name one checkout.
 *
 * The five facts travel together through the composer's two forwarding layers,
 * so they move as one value: a sixth fact is added here rather than at five
 * prop declarations across four files.
 */
export interface WorkingTreeInfo extends WorkingTreeKind {
  /**
   * The BRANCH name checked out here, or the caller's own placeholder for a
   * checkout with no branch (the sidebar's `(no branch)` bucket). Empty renders
   * an empty value rather than dropping the row, so the label column still
   * states the kind.
   */
  name: string
  /** Absolute working-tree root. Rendered tilde-compressed against `homeDir`. */
  directory: string
  /**
   * The worker's home directory, for the tilde compression. Absent or empty
   * leaves the path absolute, which is what `tildify` already does -- so a
   * worker with no system info yet shows a correct long path rather than a
   * wrong short one.
   */
  homeDir?: string
  /**
   * The worker's path flavor. Absent lets `tildify` sniff it from the path.
   *
   * Pass `flavorFromOs(os)` only where the worker's OS is known. An
   * unconditional `flavorFromOs(undefined)` returns `'posix'`, which would
   * force posix on a caller that has no OS yet and stop `C:\Users\u\repo` from
   * compressing at all.
   */
  flavor?: PathFlavor
  /**
   * The worker that hosts this checkout, for a surface where two rows can
   * otherwise read alike.
   *
   * Set it only where the worker DISAMBIGUATES. Two workers with the same
   * branch checked out at the same path under each home directory produce two
   * identical rows and two identical directories, and the delete beside one of
   * them removes a different machine's work. Where one worker owns everything
   * on screen the row is noise, so the caller leaves it out.
   */
  worker?: string
  /** Diff badge beside the name. Omit it where the caller has no stats. */
  stats?: DiffStats | null
}

export type WorkingTreeRowsProps = WorkingTreeInfo

/**
 * The labelled rows that state which kind of checkout this is, which branch it
 * has checked out, and where it lives:
 *
 *     Worktree branch   feature/auth              +38 -12
 *     Directory         ~/Workspaces/leapmux-worktrees/feature-auth
 *
 * The first row's VALUE is the branch name, not a directory name, and its
 * label says so: a linked worktree has a branch checked out like any other
 * checkout. In the main checkout the label is plain `Branch`. The directory
 * is the row below.
 *
 * It renders NO interactive child and NO nested `Tooltip`. Most callers pass it
 * as `Tooltip`'s `content`, and a tooltip's portal sets `pointer-events: none`
 * over everything inside it -- so a button or a nested tooltip there can never
 * be reached. A caller that wants a copy affordance builds its own rows; see
 * `AgentInfoCard`, which keeps its three-column grid for exactly that reason.
 */
export const WorkingTreeRows: Component<WorkingTreeRowsProps> = props => (
  <div class={styles.rows} data-testid="working-tree-rows">
    <span class={styles.label}>{workingTreeBranchRowLabel(props.isWorktree)}</span>
    <span class={styles.kindValue}>
      {/* The icon repeats what the label column already says, on purpose: this
          is where a reader learns which glyph the row it hovered uses. It
          needs no `label`, because the column beside it prints the noun. */}
      <WorkingTreeIcon isWorktree={props.isWorktree} size="xs" />
      <span class={styles.nameValue} data-testid="working-tree-name">{props.name}</span>
      <Show when={props.stats}>
        {s => <DiffStatsBadge stats={s()} />}
      </Show>
    </span>
    <span class={styles.label}>Directory</span>
    <span class={styles.pathValue} data-testid="working-tree-directory">
      {tildify(props.directory, props.homeDir, props.flavor)}
    </span>
    <Show when={props.worker}>
      {name => (
        <>
          <span class={styles.label}>Worker</span>
          <span class={styles.nameValue} data-testid="working-tree-worker">{name()}</span>
        </>
      )}
    </Show>
  </div>
)

export interface WorkingTreeTooltipProps {
  /** The checkout the rows describe. */
  info: WorkingTreeInfo
  /**
   * Why the surface's branch actions are unusable, or undefined when usable.
   * It REPLACES the rows: for a user who never opens the menu, the tooltip is
   * the only route to that reason.
   */
  disabledReason?: string
  children: JSX.Element
}

/**
 * The hover body that the composer chip and the composer `[+]` menu's branch
 * row both carry.
 *
 * One owner of the precedence rule. `Tooltip` renders `content` in place of
 * `text` whenever `content` is set, so a reason and the rows cannot both be
 * passed -- and two call sites that each spelled that out could start answering
 * differently. The user toggles between these two surfaces with one preference,
 * so they must state the same thing.
 */
export const WorkingTreeTooltip: Component<WorkingTreeTooltipProps> = props => (
  <Tooltip
    text={props.disabledReason}
    content={props.disabledReason ? undefined : <WorkingTreeRows {...props.info} />}
  >
    {props.children}
  </Tooltip>
)
