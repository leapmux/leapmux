import type { Message } from '@bufbuild/protobuf'
import type {
  FloatingWindowRecord,
  NodeRecord,
  TabRecord,
  UserCrdtState,
} from '~/generated/leapmux/v1/user_crdt_pb'
import type { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { NodeKind } from '~/generated/leapmux/v1/user_crdt_pb'
import { shallowEqual, shallowEqualArrays } from '~/lib/shallowEqual'
import { hlcIsZero } from './hlc'

/**
 * The tiling tree the client draws.
 *
 * Client-only: the hub's projection deliberately carries no render tree (see
 * `Projection` in `backend/internal/hub/crdt/project.go`), because layout
 * belongs to whoever draws it and a second unexercised copy of the tiling rules
 * only gave the two implementations somewhere to drift. What the two sides DO
 * share is tab ownership/rendering, pinned by
 * `testdata/crdt_projection_conformance.json`.
 *
 * Immutable once `project()` has returned it, and the type says so: a
 * `ProjectionCache` shares whole subtrees across calls, so a mutation would
 * reach back through every earlier projection that still holds the node.
 */
export interface RenderTree {
  readonly nodeId: string
  readonly kind: NodeKind
  readonly direction: number
  readonly ratios: readonly number[]
  readonly rows: number
  readonly cols: number
  readonly rowRatios: readonly number[]
  readonly colRatios: readonly number[]
  readonly children: readonly RenderTree[]
}

/**
 * Immutable once returned, for the reason {@link RenderTree} gives.
 *
 * No `user_id`, unlike the hub's twin: the hub keys its tab index by
 * `TabKey{UserID, ...}` and genuinely reads it, while nothing on this side ever
 * did. The two structs already diverge by design -- the hub's `Projection`
 * carries no render tree at all -- so mirroring it here bought a field to keep
 * in step, not a contract. `testdata/crdt_projection_conformance.json` compares
 * tabType/tabId/workspaceId/tileId/workerId/position, and none of that moved.
 */
export interface RenderedTab {
  readonly workspaceId: string
  readonly tabType: TabType
  readonly tabId: string
  readonly workerId: string
  readonly tileId: string
  readonly position: string
}

/** Immutable once returned, for the reason {@link RenderTree} gives. */
export interface RenderedFloatingWindow {
  readonly windowId: string
  readonly x: number
  readonly y: number
  readonly width: number
  readonly height: number
  readonly opacity: number
  readonly innerTree: RenderTree
}

/** Immutable once returned, for the reason {@link RenderTree} gives. */
export interface WorkspaceProjection {
  readonly workspaceId: string
  readonly mainTree: RenderTree
  readonly floatingWindows: readonly RenderedFloatingWindow[]
}

/** Immutable once returned, for the reason {@link RenderTree} gives. */
export interface Projection {
  readonly userId: string
  readonly workspaces: ReadonlyMap<string, WorkspaceProjection>
  readonly ownedTabs: readonly RenderedTab[]
  readonly renderedTabs: readonly RenderedTab[]
}

// --- Reuse cache ---

/**
 * Per-node reuse entry: the inputs the cached subtree was derived from.
 *
 * The registers are held BY REFERENCE rather than by value, which is the whole
 * mechanism -- see {@link ProjectionCache}.
 */
interface CachedNode extends CachedNodeRegisters {
  /**
   * The child subtrees the tree was built over. Identity is the exact test: a
   * child that was itself reused is the same object, and a child that was
   * rebuilt is a new one, so a change anywhere below this node reaches it.
   */
  children: readonly RenderTree[]
  tree: RenderTree
}

/**
 * Which `NodeRecord` fields a {@link CachedNode} tracks -- DERIVED from the
 * proto rather than listed, so adding a register lands here automatically and
 * `rebuildTree`'s snapshot literal stops compiling until the new register is
 * handled. A hand-written list is one a future register silently falls out of,
 * and the dangerous direction is a register that reaches the snapshot but not
 * the COMPARE: `tree()` then hits and hands back `prev.tree` carrying the old
 * value, which is a stale answer, not a missed reuse.
 *
 * `nodeId`/`parentId` are identity, not registers. `position` is read by the
 * PARENT's child sort and never by this node's build, so a change to it must
 * reach the tree through the parent's `children` array -- tracking it here
 * would rebuild the node itself for nothing.
 */
export type NodeRegisterKey = Exclude<keyof NodeRecord, keyof Message | 'nodeId' | 'parentId' | 'position'>

/**
 * `-?` is load-bearing: an absent record snapshots as `undefined`, so every key
 * must be REQUIRED-but-nullable or `rebuildTree`'s literal could omit one and
 * still type-check.
 */
type CachedNodeRegisters = { [K in NodeRegisterKey]-?: NodeRecord[K] | undefined }

/**
 * Per-tab reuse entry: every input the `RenderedTab` is built from, and nothing
 * else. That is the rule for all four entry shapes here and it is what makes the
 * cache checkable by reading `build` beside it.
 *
 * So no `tombstone_at` (the loop skips tombstoned records before it reaches the
 * cache -- a tombstoned tab is simply absent from the next generation) and no
 * leaf-ness: which of the two output arrays a row lands in is decided per call
 * from `tileIsLeaf`, not from the row, so remembering it here could not change an
 * answer.
 */
interface CachedTab {
  tabType: TabType
  tileId: TabRecord['tileId']
  position: TabRecord['position']
  workerId: TabRecord['workerId']
  /**
   * The resolved workspace, which depends on the node topology rather than on the
   * tab's own registers: a floating window moving between workspaces, or a peer
   * tombstoning the tile's chain, changes this while every register stays put.
   */
  workspaceId: string
  row: RenderedTab
}

/**
 * Per-floating-window reuse entry. No `tombstone_at`, for the reason
 * {@link CachedTab} gives; no `workspace_id` either, because it does not appear
 * on a `RenderedFloatingWindow` -- a window that moves between workspaces keeps
 * its own shape and changes both workspaces' window lists instead.
 */
interface CachedWindow {
  rootNodeId: string
  x: FloatingWindowRecord['x']
  y: FloatingWindowRecord['y']
  width: FloatingWindowRecord['width']
  height: FloatingWindowRecord['height']
  opacity: FloatingWindowRecord['opacity']
  innerTree: RenderTree
  window: RenderedFloatingWindow
}

/**
 * Per-workspace reuse entry. `root_node_id` is deliberately absent: it can only
 * change the projection through `mainTree`, whose identity is compared here, so
 * carrying it too would be a test that can never fail on its own.
 */
interface CachedWorkspace {
  mainTree: RenderTree
  floatingWindows: readonly RenderedFloatingWindow[]
  projection: WorkspaceProjection
}

/**
 * Optional reuse cache for {@link project}, owned by the caller and passed on
 * every call.
 *
 * WHY this can exist at all: `apply.ts` never mutates a register in place. Every
 * write REPLACES the register object (`rec.ratios = lwwDoubles(...)`), a
 * tombstone replaces the whole record, and `cloneStateForBatches` deep-clones
 * exactly the records a batch will touch. So **register-object identity is a
 * sound change detector**: same reference implies same value, and the cache
 * re-derives that judgement from the live state on every call rather than
 * trusting a remembered dirty set. It cannot serve a stale answer, and it needs
 * to know nothing about which ops landed -- local, remote, bootstrap, commit and
 * revert paths all look the same to it.
 *
 * Register identity is one-directional, though: an LWW write that stores the
 * SAME value under a newer HLC produces a fresh register object, so the inputs
 * say "changed" when nothing did. That is not a rare shape -- it is what a
 * `BatchCommitted` echo looks like whenever a local edit's optimistic submit is
 * confirmed by the hub's canonical version (same value, newer HLC), which
 * happens on every confirmed mutation. So a miss falls through to a second
 * question: the rebuilt object is compared BY VALUE against the previous one,
 * and the previous one is kept when they match (see `rebuildTree`). Inputs
 * decide whether to rebuild; the output decides whether anyone downstream hears
 * about it.
 *
 * WHAT it buys, and what it does NOT. The TRAVERSAL is unchanged -- validating a
 * node needs its children's subtrees, so there is nothing to skip, and
 * `seen`-based cycle breaking keeps its exact semantics. For the tree walk the
 * comparison costs about what the allocation it replaces did, so that part of
 * the call is no faster; the value-compare above is a further ~6-10% ON TOP,
 * paid on every tick to make the no-op-echo ticks free downstream. The one
 * arithmetic win is `sortedTabs`: ordering both tab arrays by id is over half a
 * settled tick at 300 tabs, and identity makes it skippable. Net, on a
 * 10-workspace / 300-tab account, a settled call goes ~0.09ms -> ~0.05ms.
 *
 * What it buys is IDENTITY, and therefore the absence of downstream work. The
 * caller is a memo over an `{ equals: false }` state accessor, so it runs on
 * every CRDT tick -- twice per confirmed mutation, because each optimistic
 * `submit` is followed by the `BatchCommitted` echo that triggers
 * `recomputeSpeculative`. Every one of those used to hand the whole app a fresh
 * graph:
 *
 *   - an untouched workspace now keeps its `WorkspaceProjection`, `mainTree` and
 *     every subtree object, so `renderTreeToLocal`'s memo and
 *     `layout.store`'s `sameLayoutNode` both settle on `===`;
 *   - an untouched tab keeps its `RenderedTab`, so `tabView`'s `placedTabs`
 *     short-circuits and the join does not run;
 *   - a tick that changes nothing observable returns the IDENTICAL `Projection`
 *     object, so no projection-derived memo in the app re-runs at all.
 *
 * End to end (same account, sidebar-shaped consumers over every workspace) that
 * is 0.26ms -> 0.11ms per confirmed-mutation tick.
 *
 * Reuse is expressed as memoize-style methods that take the build thunk, so
 * consulting an entry without writing the next generation's -- which would
 * silently disable the cache from that node down -- is not expressible.
 *
 * THE RULE each entry follows: carry every input its `build` reads, plus every
 * input to the decision of WHICH build runs, and nothing else. That is what makes
 * an entry checkable by reading the `build` next to it, and it is why leaf-ness
 * and most `tombstone_at`s are absent (see {@link CachedTab}).
 *
 * Three of those comparisons cannot fail today. Not because the fields are
 * surplus -- each is genuinely an input, which is why the rule keeps them -- but
 * because a second fact somewhere else already forces a miss first:
 *
 *   - `CachedWindow.rootNodeId` is set-once and `""` projects to a different
 *     tree object than any real root, so `innerTree` moves with it;
 *   - `CachedTab.tabType` cannot change on a live record (`applySetTabRegister`
 *     returns early on a mismatch), and a re-materialized one arrives with a
 *     fresh `tile_id` register;
 *   - `CachedNode.tombstoneAt` is only reachable via `buildTreeFromRoot`, and
 *     `applyTombstoneNode` REPLACES the record, so the root loses every register
 *     it had.
 *
 * Each of those lives in a different file, none is load-bearing for anything
 * else, and none can be pinned from here -- the outputs are identical either
 * way, so no test can hold them in place (do not write one that pretends to).
 * Drop a comparison and its correctness moves from "read the `build` beside it"
 * to a three-file argument nothing checks. One `===` is the cheaper guarantee.
 */
export class ProjectionCache {
  /** Tenant the current entries were built for; a change drops all of them. */
  private userId = ''
  private nodes = new Map<string, CachedNode>()
  private tabs = new Map<string, CachedTab>()
  private windows = new Map<string, CachedWindow>()
  private workspaces = new Map<string, CachedWorkspace>()
  private nextNodes = new Map<string, CachedNode>()
  private nextTabs = new Map<string, CachedTab>()
  private nextWindows = new Map<string, CachedWindow>()
  private nextWorkspaces = new Map<string, CachedWorkspace>()
  /** Last generation's sorted tab arrays, keyed by the unsorted input. */
  private tabOrder = new Map<TabSlot, { raw: readonly RenderedTab[], sorted: readonly RenderedTab[] }>()
  private last: Projection | null = null

  /**
   * Open a generation. Entries are read from the previous one and written to a
   * fresh set of maps, so an entity that left the state is simply absent from
   * the next generation -- replacing the maps wholesale IS the eviction, the
   * same shape `layout.store`'s `previousLocalTrees` uses.
   */
  begin(userId: string): void {
    if (this.userId !== userId)
      this.clear(userId)
    this.nextNodes = new Map()
    this.nextTabs = new Map()
    this.nextWindows = new Map()
    this.nextWorkspaces = new Map()
  }

  /**
   * Drop every entry and the retained `Projection`.
   *
   * `begin` evicts by generation, so eviction only ever happens on a call to
   * `project()`. A caller that STOPS projecting -- the runtime does exactly that
   * on sign-out, where `crdtState()` goes null and the memo short-circuits
   * before `project()` -- would otherwise pin the whole last graph: every
   * `RenderTree`, `RenderedTab` and `RenderedFloatingWindow`, plus every
   * `LayoutNodeLocal` those trees still key in `renderTreeToLocal`'s `WeakMap`.
   * Call this when the state goes away.
   */
  clear(userId = ''): void {
    this.userId = userId
    this.nodes = new Map()
    this.tabs = new Map()
    this.windows = new Map()
    this.workspaces = new Map()
    // The `next` twins go too, and they are the half that actually releases the
    // graph: `commit` ASSIGNS them onto the four above, so between calls each
    // pair is the SAME map object. Replacing only `nodes` therefore left
    // `nextNodes` holding the last generation's entries -- and with them every
    // `RenderTree`, `RenderedTab` and `RenderedFloatingWindow` this method
    // exists to drop. Only `begin` replaced them, and `begin` runs from
    // `project()`, which is exactly what a cleared cache has stopped calling.
    this.nextNodes = new Map()
    this.nextTabs = new Map()
    this.nextWindows = new Map()
    this.nextWorkspaces = new Map()
    this.tabOrder = new Map()
    // `last` goes too, and it is the entry that MAKES this necessary rather than
    // tidy: `commit` deliberately does not compare `userId` (see it), so two
    // tenants whose projections happen to match structurally -- both empty, say
    // -- would otherwise get the previous tenant's `Projection` object back,
    // reporting the wrong `userId`.
    this.last = null
  }

  /**
   * Reuse or build the subtree for one node.
   *
   * ONE node id can reach this twice in a single generation: `project()` walks
   * from every workspace root and every live window root with a fresh `seen`, so
   * a node reachable from two of them is built once per walk and the second
   * `nextNodes.set` wins. Normally the two walks agree -- a node has one
   * `parent_id` and one child list -- and under a parent CYCLE they truncate at
   * different places and genuinely disagree.
   *
   * Either way the answer stays correct, because an entry carries its COMPLETE
   * input set: eight register refs plus the exact `children` array, which is
   * everything `buildNodeTree` reads. A hit therefore returns what `build` would
   * have, no matter which walk wrote the entry. What the double write costs is
   * reuse, not correctness -- each walk compares against the entry the other
   * left, so both miss for as long as the collision lasts.
   */
  tree(nodeId: string, rec: NodeRecord | undefined, children: readonly RenderTree[], build: () => RenderTree): RenderTree {
    const prev = this.nodes.get(nodeId)
    if (prev
      && prev.kind === rec?.kind
      && prev.direction === rec?.direction
      && prev.ratios === rec?.ratios
      && prev.rows === rec?.rows
      && prev.cols === rec?.cols
      && prev.rowRatios === rec?.rowRatios
      && prev.colRatios === rec?.colRatios
      && prev.tombstoneAt === rec?.tombstoneAt
      && shallowEqualArrays(prev.children, children)) {
      this.nextNodes.set(nodeId, prev)
      return prev.tree
    }
    return this.rebuildTree(nodeId, rec, children, build, prev)
  }

  /**
   * The miss path, kept out of {@link tree} so the hot method stays small enough
   * for the engine to inline -- `tree` is called once per node per tick, and
   * folding this back in measured ~2% on a 600-node account.
   */
  private rebuildTree(
    nodeId: string,
    rec: NodeRecord | undefined,
    children: readonly RenderTree[],
    build: () => RenderTree,
    prev: CachedNode | undefined,
  ): RenderTree {
    const fresh = build()
    // The inputs moved but the OUTPUT may not have: see {@link ProjectionCache}
    // on the same-value rewrite. Keeping the previous object costs one shallow
    // compare on a miss and saves every consumer downstream from re-running.
    const tree = prev && sameRenderTree(prev.tree, fresh) ? prev.tree : fresh
    this.nextNodes.set(nodeId, {
      kind: rec?.kind,
      direction: rec?.direction,
      ratios: rec?.ratios,
      rows: rec?.rows,
      cols: rec?.cols,
      rowRatios: rec?.rowRatios,
      colRatios: rec?.colRatios,
      tombstoneAt: rec?.tombstoneAt,
      children,
      tree,
    })
    return tree
  }

  tab(rec: TabRecord, workspaceId: string, build: () => RenderedTab): RenderedTab {
    const prev = this.tabs.get(rec.tabId)
    if (prev
      && prev.tabType === rec.tabType
      && prev.tileId === rec.tileId
      && prev.position === rec.position
      && prev.workerId === rec.workerId
      && prev.workspaceId === workspaceId) {
      this.nextTabs.set(rec.tabId, prev)
      return prev.row
    }
    return this.rebuildTab(rec, workspaceId, build, prev)
  }

  /** The miss path, extracted for the reason {@link rebuildTree} gives. */
  private rebuildTab(rec: TabRecord, workspaceId: string, build: () => RenderedTab, prev: CachedTab | undefined): RenderedTab {
    const fresh = build()
    const row = prev && shallowEqual(prev.row, fresh) ? prev.row : fresh
    this.nextTabs.set(rec.tabId, {
      tabType: rec.tabType,
      tileId: rec.tileId,
      position: rec.position,
      workerId: rec.workerId,
      workspaceId,
      row,
    })
    return row
  }

  /**
   * Carry a LIVE tab's entry into the next generation without projecting it.
   *
   * `project()` skips a tab whose tile it cannot resolve -- an unknown tile id,
   * a tombstoned chain, a workspace record that is gone -- so the tab is absent
   * from the output for as long as that lasts. Eviction is by generation, so
   * without this the entry would be dropped on the FIRST such tick, and the tab
   * would come back with a brand-new `RenderedTab` on the tick it resolves
   * again even though nothing about it moved. `tabView` keeps rendering that
   * tab throughout (see its `lastResolved`), and `mapArray` keys the per-tab
   * computation by row REFERENCE -- so the fresh object throws that computation
   * away and re-assembles the tab for nothing.
   *
   * Sound for the same reason a hit is: the entry carries the COMPLETE input
   * set, so {@link tab}'s comparison re-derives the answer from the live record
   * whenever the tab is projected again, however many generations the entry sat
   * out. A register that moved meanwhile misses on identity, and `rebuildTab`'s
   * value compare settles it from there -- a retained entry cannot serve a
   * stale row any more than a one-generation-old one can.
   *
   * Only ever reached for a tab still live in `state.tabs`: `project()` skips a
   * tombstoned tab before this point and never visits a removed one, so the map
   * stays bounded by the live tab set. Deliberately not mirrored for floating
   * windows -- nothing holds a dropped window's identity across the gap.
   */
  retainTab(tabId: string): void {
    const prev = this.tabs.get(tabId)
    if (prev)
      this.nextTabs.set(tabId, prev)
  }

  window(rec: FloatingWindowRecord, innerTree: RenderTree, build: () => RenderedFloatingWindow): RenderedFloatingWindow {
    const prev = this.windows.get(rec.windowId)
    if (prev
      && prev.rootNodeId === rec.rootNodeId
      && prev.x === rec.x
      && prev.y === rec.y
      && prev.width === rec.width
      && prev.height === rec.height
      && prev.opacity === rec.opacity
      && prev.innerTree === innerTree) {
      this.nextWindows.set(rec.windowId, prev)
      return prev.window
    }
    return this.rebuildWindow(rec, innerTree, build, prev)
  }

  /** The miss path, extracted for the reason {@link rebuildTree} gives. */
  private rebuildWindow(
    rec: FloatingWindowRecord,
    innerTree: RenderTree,
    build: () => RenderedFloatingWindow,
    prev: CachedWindow | undefined,
  ): RenderedFloatingWindow {
    const fresh = build()
    const window = prev && shallowEqual(prev.window, fresh) ? prev.window : fresh
    this.nextWindows.set(rec.windowId, {
      rootNodeId: rec.rootNodeId,
      x: rec.x,
      y: rec.y,
      width: rec.width,
      height: rec.height,
      opacity: rec.opacity,
      innerTree,
      window,
    })
    return window
  }

  /**
   * Reuse the SORTED tab array when the same rows were collected in the same
   * order.
   *
   * Both arrays are collected in `Object.values(state.tabs)` order -- record-
   * creation order, which `cloneStateForBatches` preserves by shallow-copying
   * the map -- and are then ordered by tab id. Real tab ids are nanoids, so the
   * collected order is effectively random and the sort is a full O(n log n) of
   * string compares: measured 52us of a 92us settled tick at 300 tabs, more than
   * everything else in `project()` put together.
   *
   * The unsorted array is the ENTIRE input. Tab ids key `state.tabs`, so they
   * are unique, `cmpStr` over them is a total order, and the sorted array is a
   * pure function of the unsorted one. Element identity is the exact test again
   * -- 300 reference compares instead of the sort.
   */
  sortedTabs(
    slot: TabSlot,
    raw: readonly RenderedTab[],
    build: (rows: readonly RenderedTab[]) => readonly RenderedTab[],
  ): readonly RenderedTab[] {
    const prev = this.tabOrder.get(slot)
    if (prev && shallowEqualArrays(prev.raw, raw))
      return prev.sorted
    const sorted = build(raw)
    this.tabOrder.set(slot, { raw, sorted })
    return sorted
  }

  /**
   * `build` receives the window list to carry, which may be the PREVIOUS
   * generation's array: the list is rebuilt per call, so a workspace that has to
   * be rebuilt for an unrelated reason -- a ratio drag on its main tree -- would
   * otherwise hand its consumers a fresh array of identical windows.
   */
  workspace(
    workspaceId: string,
    mainTree: RenderTree,
    floatingWindows: readonly RenderedFloatingWindow[],
    build: (windows: readonly RenderedFloatingWindow[]) => WorkspaceProjection,
  ): WorkspaceProjection {
    const prev = this.workspaces.get(workspaceId)
    const windows = reuseArray(prev?.floatingWindows, floatingWindows)
    if (prev && prev.mainTree === mainTree && windows === prev.floatingWindows) {
      this.nextWorkspaces.set(workspaceId, prev)
      return prev.projection
    }
    const projection = build(windows)
    this.nextWorkspaces.set(workspaceId, { mainTree, floatingWindows: windows, projection })
    return projection
  }

  /**
   * Close the generation: swap this call's entries in, reuse each of the three
   * parts the previous `Projection` still matches, and hand back that whole
   * `Projection` when all three were reused. The last step is what keeps a no-op
   * tick from propagating -- the caller's memo sees the same object and stops.
   *
   * Deciding the parts here rather than in `project()` keeps the top-level
   * `Projection` on the same memoize shape as the other four entities: a caller
   * cannot consult the previous generation without writing the next one, so
   * "reuse the parts" and "reuse the whole" cannot fall out of step.
   *
   * `userId` is deliberately not compared -- `build` simply carries the current
   * one. `begin` owns the tenant rule and already dropped `last` on a change, so
   * a second check here would be a second place that decides the same thing --
   * and the one that could silently disagree.
   */
  commit(
    workspaces: ReadonlyMap<string, WorkspaceProjection>,
    ownedTabs: readonly RenderedTab[],
    renderedTabs: readonly RenderedTab[],
    build: (parts: ProjectionParts) => Projection,
  ): Projection {
    this.nodes = this.nextNodes
    this.tabs = this.nextTabs
    this.windows = this.nextWindows
    this.workspaces = this.nextWorkspaces
    const prev = this.last
    const parts: ProjectionParts = {
      workspaces: reuseMap(prev?.workspaces, workspaces),
      ownedTabs: reuseArray(prev?.ownedTabs, ownedTabs),
      renderedTabs: reuseArray(prev?.renderedTabs, renderedTabs),
    }
    if (prev
      && parts.workspaces === prev.workspaces
      && parts.ownedTabs === prev.ownedTabs
      && parts.renderedTabs === prev.renderedTabs) {
      return prev
    }
    this.last = build(parts)
    return this.last
  }
}

/** Which of the two tab arrays a {@link ProjectionCache.sortedTabs} entry is for. */
type TabSlot = 'owned' | 'rendered'

/** Everything a `Projection` carries except the tenant it was built for. */
interface ProjectionParts {
  readonly workspaces: ReadonlyMap<string, WorkspaceProjection>
  readonly ownedTabs: readonly RenderedTab[]
  readonly renderedTabs: readonly RenderedTab[]
}

/**
 * Every `RenderTree` field, and how it has to be compared.
 *
 * The mapped type over `keyof RenderTree` is the point: add a field to
 * `RenderTree` and this object stops type-checking until the field is
 * classified. A hand-written `&&` chain -- which this replaces -- had no such
 * tripwire, and the failure was silent AND wrong in the dangerous direction:
 * `rebuildTree` keeps `prev.tree` when the compare says "same", so an
 * uncompared field means the cache hands back the OLD subtree carrying the OLD
 * value. That is a stale answer, not a missed reuse, and it would contradict
 * this cache's central claim that it cannot serve one.
 */
const RENDER_TREE_FIELDS: { readonly [K in keyof RenderTree]-?: 'scalar' | 'array' } = {
  nodeId: 'scalar',
  kind: 'scalar',
  direction: 'scalar',
  rows: 'scalar',
  cols: 'scalar',
  ratios: 'array',
  rowRatios: 'array',
  colRatios: 'array',
  children: 'array',
}

// Split once at module load so the per-call cost is two walks over a small
// frozen array rather than a re-derivation of the classification.
// Not frozen: these are module-private and only ever iterated, and a frozen
// array measures ~4x slower to walk in JSC (the engine WKWebView ships), which
// is the engine this runs in.
const RENDER_TREE_SCALAR_KEYS = (Object.keys(RENDER_TREE_FIELDS) as (keyof RenderTree)[]).filter(k => RENDER_TREE_FIELDS[k] === 'scalar')
const RENDER_TREE_ARRAY_KEYS = (Object.keys(RENDER_TREE_FIELDS) as (keyof RenderTree)[]).filter(k => RENDER_TREE_FIELDS[k] === 'array')

/**
 * Do two `RenderTree` nodes carry the same VALUE?
 *
 * Not `shallowEqual`: the four array fields are rebuilt per call (`[...rawRatios]`,
 * a fresh `children`), so reference equality would always say no. Children are
 * compared by element identity, which is exact -- a child that reused its own
 * object is the same object, and one that genuinely changed is not.
 */
function sameRenderTree(a: RenderTree, b: RenderTree): boolean {
  for (const k of RENDER_TREE_SCALAR_KEYS) {
    if (!Object.is(a[k], b[k]))
      return false
  }
  for (const k of RENDER_TREE_ARRAY_KEYS) {
    if (!shallowEqualArrays(a[k] as readonly unknown[], b[k] as readonly unknown[]))
      return false
  }
  return true
}

/** `prev` when it holds the same elements as `next`, so the identity survives. */
function reuseArray<T>(prev: readonly T[] | undefined, next: readonly T[]): readonly T[] {
  return prev && shallowEqualArrays(prev, next) ? prev : next
}

/**
 * `prev` when it maps every key of `next` to the same value.
 *
 * Equal sizes plus `next` being a subset makes the two equal as MAPPINGS; only
 * iteration order could still differ, and nothing reads these positionally
 * (consumers `get`, build a `Set` of the keys, or rebuild their own map). In
 * practice it cannot differ either: `state.workspaces` is only appended to and
 * deleted from, never reordered.
 */
function reuseMap<K, V>(prev: ReadonlyMap<K, V> | undefined, next: ReadonlyMap<K, V>): ReadonlyMap<K, V> {
  if (!prev || prev.size !== next.size)
    return next
  for (const [k, v] of next) {
    if (prev.get(k) !== v)
      return next
  }
  return prev
}

/** A bare leaf under `nodeId` -- the shape every degenerate tree case renders as. */
function leafOnly(nodeId: string): RenderTree {
  return { nodeId, kind: NodeKind.LEAF, direction: 0, ratios: [], rows: 0, cols: 0, rowRatios: [], colRatios: [], children: [] }
}

/**
 * The `node_id: ""` placeholder: an unseeded workspace root, and a grid cell no
 * live child claims.
 *
 * Shared rather than allocated per use, so a grid whose cells are not all
 * filled is still identity-stable across ticks and `ProjectionCache` can reuse
 * it. Safe because a `RenderTree` is immutable once `project()` returns it, and
 * `renderTreeToLocal` names its own placeholder leaf after the PARENT node id
 * plus the cell index rather than after this object.
 */
const EMPTY_TREE: RenderTree = leafOnly('')

/**
 * The child list of every node that has none. Shared -- which is safe only
 * because `RenderTree.children` is `readonly`, so the leaf that carries it
 * cannot grow a child and hand it to every other leaf.
 */
const NO_CHILD_TREES: readonly RenderTree[] = []

/**
 * The window list of every workspace that has none. Shared for the reason
 * {@link NO_CHILD_TREES} gives -- `WorkspaceProjection.floatingWindows` is
 * `readonly`, so no workspace can push into the list every other one carries.
 */
const NO_WINDOWS: readonly RenderedFloatingWindow[] = []

/**
 * Map every registered root node_id (workspace + floating window roots) to its
 * owning workspace_id.
 */
function registeredRoots(state: UserCrdtState): Map<string, string> {
  const roots = new Map<string, string>()
  // Ascending id order, FIRST claim wins -- on both sides.
  //
  // Two workspaces can name the same root node id. The hub's commit path
  // rejects that, but the projection is total and still has to answer for
  // speculative state, and the conformance harnesses apply ops raw with no
  // validator. Relying on `Object.entries` order looks deterministic here but
  // is not a property the two implementations can SHARE: Go's map has no
  // order, and these records arrive over the wire, where protobuf leaves map
  // field ordering unspecified -- so this object's order is decode order, not
  // anything two clients agree on. Ordering on a value IN the data is.
  //
  // `project.go:registeredRoots` sorts and first-wins identically.
  for (const wsId of Object.keys(state.workspaces).sort()) {
    const ws = state.workspaces[wsId]
    if (ws.rootNodeId !== '' && !roots.has(ws.rootNodeId))
      roots.set(ws.rootNodeId, wsId)
  }
  // Windows after workspaces, so a workspace root outranks a window root on
  // collision -- a workspace root is protected even from internal batches,
  // while a window root may be tombstoned by an internal sweep.
  for (const windowId of Object.keys(state.floatingWindows).sort()) {
    const fw = state.floatingWindows[windowId]
    // Only LIVE (non-tombstoned) floating windows contribute a root.
    // Mirrors `backend/internal/hub/crdt/project.go:registeredRoots`,
    // which skips tombstoned windows with the same guard. The prior
    // `!hlcIsZero` typo EXCLUDED live windows from the root set, which
    // meant the projection's `resolveTileWorkspace` could never reach
    // a floating-window root: any tab moved into a floating window
    // dropped out of `renderedTabs`, and `reconcileFromProjection`
    // then deleted it from the local tab store. Popped-out tabs
    // vanished immediately as a result.
    if (hlcIsZero(fw.tombstoneAt) && fw.rootNodeId !== '' && !roots.has(fw.rootNodeId)) {
      roots.set(fw.rootNodeId, fw.workspaceId?.value ?? '')
    }
  }
  return roots
}

/**
 * Walk parent_id chain to a registered root; return its workspace_id
 * and whether the chain is fully alive. A single walk covers both
 * cycle detection (`visited`) and tombstone-along-the-chain checking
 * — the previous shape walked the same chain twice (once here, once
 * in a separate `chainAlive` helper).
 *
 * An intermediate tombstoned ancestor returns `workspaceId: ''`; a
 * tombstoned resolved root returns `workspaceId, alive: false`. Both
 * now drop the tab from BOTH views, because `ownershipHolds` requires
 * a live chain — the two used to differ, which is what let a
 * tombstoned workspace root leave its tabs owned-but-unrendered while
 * any other tombstoned tile dropped them outright.
 *
 * `registeredRoots` indexes workspace roots by id only, so the
 * resolved root's own NodeRecord must be re-checked for a tombstone at
 * chain-end. An ABSENT root record counts as alive: the root is
 * registered, its NodeRecord simply has not materialised yet, and that
 * window is exactly what `tabView`'s hold-in-place exists for.
 */
function resolveTileWorkspace(state: UserCrdtState, tileId: string, roots: ReadonlyMap<string, string>): { workspaceId: string, alive: boolean } {
  if (tileId === '')
    return { workspaceId: '', alive: false }
  const visited = new Set<string>()
  let cur = tileId
  for (;;) {
    if (visited.has(cur))
      return { workspaceId: '', alive: false }
    visited.add(cur)
    const wsId = roots.get(cur)
    if (wsId !== undefined) {
      // Root reached. The chain up to here is alive (we'd have early-
      // returned otherwise). Workspace roots are registered without
      // checking their NodeRecord's `tombstoneAt`, so re-read the
      // root node and flag the chain dead if it was tombstoned.
      const rootNode = state.nodes[cur]
      const alive = !rootNode || hlcIsZero(rootNode.tombstoneAt)
      return { workspaceId: wsId, alive }
    }
    const node = state.nodes[cur]
    if (!node || !hlcIsZero(node.tombstoneAt))
      return { workspaceId: '', alive: false }
    if (node.parentId === '')
      return { workspaceId: '', alive: false }
    cur = node.parentId
  }
}

function tileIsLeaf(state: UserCrdtState, tileId: string): boolean {
  const rec = state.nodes[tileId]
  if (!rec)
    return false
  return rec.kind?.value === NodeKind.LEAF
}

/**
 * Build a parent_id → live children index in a single O(N) pass so
 * the recursive tree walk does O(fanout) per node instead of O(N).
 * Exported for `tileOps`, which asks the same question of the same
 * state and would otherwise re-walk it per call.
 */
export function buildChildIndex(state: UserCrdtState): Map<string, NodeRecord[]> {
  const idx = new Map<string, NodeRecord[]>()
  for (const n of Object.values(state.nodes)) {
    if (!hlcIsZero(n.tombstoneAt))
      continue
    const arr = idx.get(n.parentId)
    if (arr)
      arr.push(n)
    else idx.set(n.parentId, [n])
  }
  return idx
}

function buildTreeFromRoot(state: UserCrdtState, rootId: string, childIndex: Map<string, NodeRecord[]>, cache?: ProjectionCache): RenderTree {
  if (rootId === '')
    return EMPTY_TREE
  const rec = state.nodes[rootId]
  if (rec && hlcIsZero(rec.tombstoneAt))
    return buildTree(rec, childIndex, new Set(), cache)
  // A registered root whose NodeRecord is absent or tombstoned renders as a
  // bare leaf under the root's own id. Cached through the same entry shape as a
  // live node: an absent record snapshots as eight undefined register refs,
  // which can only collide with a live record that has none of them set, and
  // both produce exactly this tree.
  const build = (): RenderTree => leafOnly(rootId)
  return cache ? cache.tree(rootId, rec, NO_CHILD_TREES, build) : build()
}

/**
 * Build (or reuse) the subtree rooted at `rec`.
 *
 * Children are resolved BEFORE the cache is consulted, and that ordering is the
 * design: a node's tree is a function of its own registers plus its children's
 * subtrees, so the recursion has to have run before the reuse question can be
 * answered. The cache therefore removes allocation, not traversal.
 */
function buildTree(rec: NodeRecord, childIndex: Map<string, NodeRecord[]>, seen: Set<string>, cache?: ProjectionCache): RenderTree {
  if (seen.has(rec.nodeId)) {
    // Cycle. Deliberately NOT cached, and for a sharper reason than "the answer
    // is path-dependent": this tree is not a function of `rec`'s registers at
    // all. It ignores every one of them -- a LEAF carrying a non-zero
    // `direction` projects to that direction, while the stub is always
    // `direction: 0`. Filing it in `nodes` under `nodeId` would therefore hand
    // it to a later walk that reaches the node normally and matches on exactly
    // those registers. Anything that wants to make the stub reusable has to key
    // it somewhere `tree()` cannot reach.
    //
    // A fresh object each call also means every ancestor holding it misses and
    // rebuilds, so a cyclic graph never settles into whole-projection reuse.
    // That is accepted: `parent_id` is set-once client-side (`apply.ts`) and the
    // hub rejects a re-parent outright (`BATCH_REJECTION_PARENT_IMMUTABLE`), so
    // a cycle needs two nodes minted in one batch naming each other -- which
    // resolves to no workspace and is rejected again. No emitter can build one.
    return leafOnly(rec.nodeId)
  }
  seen.add(rec.nodeId)
  const kind = rec.kind?.value ?? NodeKind.LEAF
  const rows = rec.rows?.value ?? 0
  const cols = rec.cols?.value ?? 0
  let children: readonly RenderTree[] = NO_CHILD_TREES
  if (kind === NodeKind.SPLIT) {
    const sorted = (childIndex.get(rec.nodeId) ?? []).slice().sort((a, b) => {
      const pa = a.position?.value ?? ''
      const pb = b.position?.value ?? ''
      return pa !== pb ? cmpStr(pa, pb) : cmpStr(a.nodeId, b.nodeId)
    })
    const built = Array.from<RenderTree>({ length: sorted.length })
    for (let i = 0; i < sorted.length; i++)
      built[i] = buildTree(sorted[i], childIndex, seen, cache)
    // SPLIT with one live child renders as just that child (visual collapse),
    // under the CHILD's own node id.
    //
    // It used to re-key the child to the SPLIT's id. That gave one rendered
    // surface two identities -- the leaf's real, addressable, writable CRDT id
    // and the ancestor's -- and every consumer had to compensate: the view
    // aliased tab placements back onto the renamed id, `emitCloseTile` walked
    // up the collapse chain to migrate tabs to the topmost ancestor, and a
    // path that forgot to (`emitRemoveGrid`) left tabs alive on a tile the
    // tree no longer advertised, i.e. invisible. Rendering the child as
    // itself removes the second identity and all three compensations with it.
    //
    // Nothing to cache under this node: the tree it returns is the child's, and
    // the child cached it under its own id.
    if (built.length === 1)
      return built[0]
    children = built
  }
  else if (kind === NodeKind.GRID && rows > 0 && cols > 0) {
    const grid = new Map<string, NodeRecord>()
    for (const c of childIndex.get(rec.nodeId) ?? []) {
      const pos = c.position?.value ?? ''
      const existing = grid.get(pos)
      if (!existing || c.nodeId < existing.nodeId)
        grid.set(pos, c)
    }
    const cells: RenderTree[] = []
    for (let r = 0; r < rows; r++) {
      for (let col = 0; col < cols; col++) {
        const entry = grid.get(`${r},${col}`)
        cells.push(entry ? buildTree(entry, childIndex, seen, cache) : EMPTY_TREE)
      }
    }
    children = cells
  }
  const build = (): RenderTree => buildNodeTree(rec, kind, rows, cols, children)
  return cache ? cache.tree(rec.nodeId, rec, children, build) : build()
}

/**
 * The `RenderTree` for one node, with the ratio-repair rules applied.
 *
 * Every field is computed before the object is built, so a returned tree is
 * never mutated afterwards — the invariant `ProjectionCache` and
 * `renderTreeToLocal`'s memo both rest on. Ratio arrays are COPIED out of the
 * register rather than aliased: the register owns its array, and the projection
 * must not hand a reference to live CRDT state to its consumers.
 */
function buildNodeTree(rec: NodeRecord, kind: NodeKind, rows: number, cols: number, children: readonly RenderTree[]): RenderTree {
  const rawRatios = rec.ratios?.value?.values ?? []
  const rawRowRatios = rec.rowRatios?.value?.values ?? []
  const rawColRatios = rec.colRatios?.value?.values ?? []
  // A grid with a zero dimension renders no cells and repairs no ratios --
  // exactly the case the child walk above skipped.
  const isGrid = kind === NodeKind.GRID && rows > 0 && cols > 0
  return {
    nodeId: rec.nodeId,
    kind,
    direction: rec.direction?.value ?? 0,
    ratios: kind === NodeKind.SPLIT ? normalizeRatios(rawRatios, children.length) : [...rawRatios],
    rows,
    cols,
    rowRatios: isGrid && rawRowRatios.length !== rows ? normalizeRatios(rawRowRatios, rows) : [...rawRowRatios],
    colRatios: isGrid && rawColRatios.length !== cols ? normalizeRatios(rawColRatios, cols) : [...rawColRatios],
    children,
  }
}

/**
 * Is a tab whose tile resolved to `workspaceId` the user's tab at all?
 *
 * `renderedTabs` then narrows `ownedTabs` by leaf-ness alone, which makes it a
 * strict subset by construction. Two conditions, both of which used to be
 * applied unevenly:
 *
 *   - The CHAIN must be alive. `resolveTileWorkspace` consults the roots map
 *     before the tombstone check, so reaching a registered root short-circuits
 *     the walk even when that root node is tombstoned. Liveness therefore only
 *     gated rendering, and a tab on a tombstoned ROOT stayed owned while a tab
 *     on any other tombstoned tile dropped from both views. Same dead tile, two
 *     different answers.
 *
 *   - The WORKSPACE must still exist. A floating window carries its own
 *     `workspace_id` and registers its root from the window record, so deleting
 *     the `WorkspaceContentsRecord` left the window's tabs resolving to a
 *     workspace `project()` no longer contains — the window is dropped from the
 *     tree below, so those tabs claimed to be on screen with nothing to draw
 *     them in.
 *
 * Checked here rather than in `registeredRoots` so the hub can mirror it: its
 * root map is shared with batch validation and subscriber broadcast filtering,
 * and narrowing it there would change both.
 */
function ownershipHolds(state: UserCrdtState, workspaceId: string, chainAlive: boolean): boolean {
  return workspaceId !== '' && chainAlive && state.workspaces[workspaceId] !== undefined
}

/**
 * Stable ascending string compare, by code point. Used to sort by
 * id/tabId/windowId — and by `tabView`, whose visible order must agree with
 * this one. Deliberately not `localeCompare`: Intl collation reorders both
 * LexoRank positions (under lt/lv) and mixed-case nanoid ids (under every
 * locale).
 */
export function cmpStr(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0
}

function normalizeRatios(ratios: readonly number[], n: number): number[] {
  if (n <= 0)
    return []
  const out = Array.from<number>({ length: n })
  for (let i = 0; i < n; i++) out[i] = 1.0 / n
  for (let i = 0; i < n && i < ratios.length; i++) {
    if (ratios[i] >= 0)
      out[i] = ratios[i]
  }
  return out
}

/**
 * Project applies the deterministic repair rules and returns the
 * renderable projection. Rules: tombstoned skipped, orphans dropped,
 * cycles broken, single-child SPLIT rendered as the child, duplicate
 * grid cells tie-broken by lower node_id, missing grid cells render
 * as virtual empty leaves, bad ratio lengths normalized.
 *
 * Pure: the answer depends only on `state`. `cache` decides what gets
 * ALLOCATED, never what is returned — pass one and every unchanged workspace,
 * window, tab and subtree keeps its object identity (see {@link
 * ProjectionCache}); omit it and every call builds a fresh graph. That uncached
 * form is the test oracle: `conformance.test.ts` replays every shared fixture
 * case both ways after every op, and `project.test.ts`'s seeded fuzz does the
 * same over every register, so "reuse fired" and "reuse never fired" are pinned
 * to agree.
 *
 * The irreducible per-tick cost is `registeredRoots` + `buildChildIndex` + the
 * descent itself: all three are O(nodes) and none can be identity-validated
 * without doing the walk they exist to perform. Ordering the two tab arrays is
 * NOT in that set -- it is a pure function of the rows the walk collected, so
 * `ProjectionCache.sortedTabs` memoizes it on them.
 */
export function project(state: UserCrdtState, cache?: ProjectionCache): Projection {
  cache?.begin(state.userId)
  const roots = registeredRoots(state)
  const childIndex = buildChildIndex(state)
  // Memoize tile -> (workspaceId, alive) so multi-tab leaves don't re-walk
  // identical parent chains. `state` and `roots` are fixed for the duration of
  // this call and `resolveTileWorkspace` is pure, so the memo is exact.
  //
  // Mirrors the hub's `tileMemo` in `backend/internal/hub/crdt/project.go`
  // (`projectTabs`), which added it for the same reason -- except the client
  // needs it MORE: `project()` re-runs on every CRDT tick, where the hub runs
  // it once per commit. `tileIsLeaf` stays outside the memo: it is a single map
  // lookup, and the Go twin also calls it per tab.
  const tileMemo = new Map<string, { workspaceId: string, alive: boolean }>()
  const resolveTile = (tileId: string): { workspaceId: string, alive: boolean } => {
    let res = tileMemo.get(tileId)
    if (!res) {
      res = resolveTileWorkspace(state, tileId, roots)
      tileMemo.set(tileId, res)
    }
    return res
  }

  // Floating windows BEFORE workspaces: a workspace's projection is reusable
  // only when its window list is unchanged too, so the list has to exist before
  // the `WorkspaceProjection` that carries it is built.
  const windowsByWorkspace = new Map<string, RenderedFloatingWindow[]>()
  for (const fw of Object.values(state.floatingWindows)) {
    if (!hlcIsZero(fw.tombstoneAt))
      continue
    const wsId = fw.workspaceId?.value ?? ''
    // A window whose workspace record is gone is dropped rather than rendered
    // into a workspace the projection does not contain -- see `ownershipHolds`,
    // which drops its tabs for the same reason.
    if (state.workspaces[wsId] === undefined)
      continue
    const innerTree = buildTreeFromRoot(state, fw.rootNodeId, childIndex, cache)
    const build = (): RenderedFloatingWindow => ({
      windowId: fw.windowId,
      x: fw.x?.value ?? 0,
      y: fw.y?.value ?? 0,
      width: fw.width?.value ?? 0,
      height: fw.height?.value ?? 0,
      opacity: fw.opacity?.value ?? 0,
      innerTree,
    })
    const rendered = cache ? cache.window(fw, innerTree, build) : build()
    const list = windowsByWorkspace.get(wsId)
    if (list)
      list.push(rendered)
    else windowsByWorkspace.set(wsId, [rendered])
  }
  for (const list of windowsByWorkspace.values())
    list.sort((a, b) => cmpStr(a.windowId, b.windowId))

  const workspaces = new Map<string, WorkspaceProjection>()
  for (const [wsId, ws] of Object.entries(state.workspaces)) {
    const mainTree = buildTreeFromRoot(state, ws.rootNodeId, childIndex, cache)
    const floatingWindows = windowsByWorkspace.get(wsId) ?? NO_WINDOWS
    const build = (windows: readonly RenderedFloatingWindow[]): WorkspaceProjection =>
      ({ workspaceId: wsId, mainTree, floatingWindows: windows })
    workspaces.set(wsId, cache ? cache.workspace(wsId, mainTree, floatingWindows, build) : build(floatingWindows))
  }

  const ownedRaw: RenderedTab[] = []
  const renderedRaw: RenderedTab[] = []
  for (const t of Object.values(state.tabs)) {
    if (!hlcIsZero(t.tombstoneAt))
      continue
    const tile = t.tileId?.value ?? ''
    const { workspaceId, alive } = resolveTile(tile)
    if (!ownershipHolds(state, workspaceId, alive)) {
      // Still a live tab, just not placeable this tick. Keep its entry so the
      // row survives the gap -- see `ProjectionCache.retainTab`.
      cache?.retainTab(t.tabId)
      continue
    }
    const leaf = tileIsLeaf(state, tile)
    const build = (): RenderedTab => ({
      workspaceId,
      tabType: t.tabType,
      tabId: t.tabId,
      workerId: t.workerId?.value ?? '',
      tileId: tile,
      position: t.position?.value ?? '',
    })
    // Both views report the same `tile_id`, because the rendered tree now
    // advertises every leaf under its own node id -- see the SPLIT collapse in
    // `buildTree`. `renderedTabs` narrows `ownedTabs` to the tabs that are on a
    // live leaf; it does not relabel them, so both hold the SAME row object.
    const row = cache ? cache.tab(t, workspaceId, build) : build()
    ownedRaw.push(row)
    if (leaf)
      renderedRaw.push(row)
  }
  // A sorted COPY, never an in-place sort: `sortedTabs` memoizes on the unsorted
  // array it was handed, so reordering that array would rewrite the very key the
  // next tick compares against.
  const byTabId = (rows: readonly RenderedTab[]): readonly RenderedTab[] =>
    rows.slice().sort((a, b) => cmpStr(a.tabId, b.tabId))
  const ownedTabs = cache ? cache.sortedTabs('owned', ownedRaw, byTabId) : byTabId(ownedRaw)
  const renderedTabs = cache ? cache.sortedTabs('rendered', renderedRaw, byTabId) : byTabId(renderedRaw)

  const build = (parts: ProjectionParts): Projection => ({ userId: state.userId, ...parts })
  return cache
    ? cache.commit(workspaces, ownedTabs, renderedTabs, build)
    : build({ workspaces, ownedTabs, renderedTabs })
}

/**
 * Return the parent node_id of `nodeId` in `state`, or "" if the node
 * is a root or absent. Mirrors the CRDT model: parent_id is set-once
 * at creation, so the answer is stable for any live node.
 */
export function parentOf(state: UserCrdtState, nodeId: string): string {
  return state.nodes[nodeId]?.parentId ?? ''
}

/**
 * Enumerate every descendant of `nodeId` in `state` (including the
 * node itself), in leaves-first order. Used by close-grid / remove-
 * subtree paths to produce a tombstone batch where leaves are
 * tombstoned before their ancestors (the CRDT doesn't require this
 * order, but it keeps the validator's intermediate states clean).
 *
 * Tombstoned nodes are skipped. Cross-node cycles are broken on
 * `seen` membership.
 */
export function descendantsLeavesFirst(
  state: UserCrdtState,
  nodeId: string,
  childIndex?: Map<string, NodeRecord[]>,
): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  const idx = childIndex ?? buildChildIndex(state)
  visit(state, nodeId, idx, seen, out)
  return out
}

function visit(state: UserCrdtState, nodeId: string, childIndex: Map<string, NodeRecord[]>, seen: Set<string>, out: string[]): void {
  if (seen.has(nodeId))
    return
  seen.add(nodeId)
  const rec = state.nodes[nodeId]
  if (!rec || !hlcIsZero(rec.tombstoneAt))
    return
  for (const n of childIndex.get(nodeId) ?? []) {
    visit(state, n.nodeId, childIndex, seen, out)
  }
  out.push(nodeId)
}
