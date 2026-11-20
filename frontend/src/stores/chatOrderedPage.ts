interface OrderedChatRow {
  id: string
  seq: bigint
}

/** An append-only page transaction over the current ordered window. */
export class OrderedChatPage<Row extends OrderedChatRow> {
  private readonly working: Row[]
  private readonly indexById: Map<string, number>
  private readonly initial: Row[]
  private savedPrefix: Row[] | undefined
  private finished = false

  constructor(initial: Row[]) {
    this.initial = initial
    this.working = initial.slice()
    this.indexById = new Map()
    for (let index = 0; index < initial.length; index++)
      this.indexById.set(initial[index]!.id, index)
    this.savedPrefix = initial
  }

  get rows(): readonly Row[] {
    return this.working
  }

  indexOf(id: string): number {
    return this.indexById.get(id) ?? -1
  }

  /** A snapshot handed to an observer keeps its membership as the page grows. */
  snapshot(): Row[] {
    return this.savedPrefix ??= this.working.slice()
  }

  append(row: Row): boolean {
    if (this.finished || row.seq <= (this.working.at(-1)?.seq ?? 0n) || this.indexById.has(row.id))
      return false
    this.indexById.set(row.id, this.working.length)
    this.working.push(row)
    this.savedPrefix = undefined
    return true
  }

  /** Transfer the array to the store. A completed page cannot write into it again. */
  finish(): Row[] {
    this.finished = true
    // Solid can unwrap references in the array it receives. Keep observer snapshots detached.
    return this.working.length === this.initial.length
      ? this.initial
      : this.working
  }
}
