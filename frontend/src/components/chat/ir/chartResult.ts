import { isObject, pickString } from '~/lib/jsonPick'

/**
 * The shapes a chart row can draw.
 *
 * These are Chart.js's own words, because the provider sends a Chart.js v4 config and
 * a translation here would only make the narrowing miss a type. `area` is the one
 * addition: Chart.js spells it as a `line` whose datasets set `fill`, and the two draw
 * differently enough that the renderer needs them apart.
 */
export const CHART_SHAPES = [
  'bar',
  'line',
  'area',
  'pie',
  'doughnut',
  'scatter',
  'bubble',
  'radar',
  'polarArea',
] as const

export type ChartShape = (typeof CHART_SHAPES)[number]

const KNOWN_SHAPES: ReadonlySet<string> = new Set(CHART_SHAPES)

/** One point of a chart whose data is coordinates rather than values. */
export interface ChartPoint {
  x: number
  y: number
  /** The bubble's radius. Absent for a scatter. */
  r?: number
}

/**
 * What one series PLOTS: values against the shared labels, or coordinates.
 *
 * Never both. One dataset carries one `data` array, and the reader of that array
 * decides which shape it holds -- so a series that stated both described a dataset
 * that cannot exist. `chartCopyableText` reads the values FIRST and drops every
 * coordinate when it finds any, so such a series silently lost its points on Copy.
 */
export type ChartSeriesData
  = | { values: number[], points?: never }
    | { values?: never, points: ChartPoint[] }

/** One series of a chart. */
export type ChartSeries = ChartSeriesFacts & ChartSeriesData

interface ChartSeriesFacts {
  label?: string
  /**
   * The shape THIS series draws, when it differs from the chart's own.
   *
   * Chart.js calls that a mixed chart: the config states one type and a dataset
   * overrides it. The row draws each series with its own shape, so a bar chart with
   * one line series reads as the model intended.
   */
  shape?: ChartShape
}

/** The categorical values of one series. Empty for a series that plots coordinates. */
export function seriesValues(series: ChartSeries): number[] {
  return series.values ?? []
}

/** The coordinates of one series. Empty for a categorical series. */
export function seriesPoints(series: ChartSeries): ChartPoint[] {
  return series.points ?? []
}

/** The name one series carries on a legend, a table heading or a copied row. */
export function chartSeriesName(series: ChartSeries, index: number): string {
  return series.label || `Series ${index + 1}`
}

/** One chart, after its provider read the config it was handed. */
export interface ChartResult {
  shape: ChartShape
  /** The chart's own heading, which the tool asks the model for. */
  title?: string
  description?: string
  /** The category of each value, shared by every categorical series. */
  labels: string[]
  series: ChartSeries[]
  /**
   * Why the chart cannot be drawn, when it cannot.
   *
   * The row states this instead of a picture. It is NOT the same as an empty chart:
   * a config with no data is a chart the model drew badly, and this is a config the
   * row could not read at all.
   */
  error?: string
  /**
   * The chart carries more than the caps below admit, so this holds part of it.
   *
   * The row states this beside the picture. Without it a reader sees a complete-looking
   * chart of the first slice of the data and has nothing that says the rest was cut.
   */
  truncated?: boolean
}

/**
 * The most points one series keeps, and the most series one chart keeps.
 *
 * A model can ask for a chart of a hundred thousand points. The plane below is 320
 * units wide, so every point past a few hundred draws on top of another one, and the
 * row still pays to lay out an SVG node for each. The table and the copied text pay
 * the same cost for rows nobody reads.
 *
 * The cap applies HERE, in the parser, so the picture, the table and the copied text
 * all read the same numbers. A cap in the renderer alone would draw one chart and copy
 * a different one.
 */
export const CHART_MAX_POINTS = 2000
export const CHART_MAX_SERIES = 24

/** Narrow a wire type to one shape, or null for a word no release declared. */
function chartShape(value: string): ChartShape | null {
  return KNOWN_SHAPES.has(value) ? value as ChartShape : null
}

/**
 * Whether one dataset's `fill` asks for an area under its line.
 *
 * Chart.js v4 accepts several spellings, and `false` is the only one that refuses:
 *
 * - `true`.
 * - A boundary word: `'origin'`, `'start'` or `'end'`. The library's own area samples
 *   emit `'origin'`, so a test for `true` alone drew them as bare lines.
 * - `'stack'` or `'shape'`.
 * - An ABSOLUTE dataset index, as a NUMBER: `0`, `1`.
 * - A RELATIVE dataset index, as a STRING: `'+1'`, `'-2'`.
 * - An object target, such as `{ value: 0 }`.
 *
 * The test is for `false`, `undefined` and `null`, and never for truthiness: `fill: 0`
 * is the index of the first dataset, so a truthy test reads a real fill as no fill.
 */
function fillsArea(value: unknown): boolean {
  return value !== undefined && value !== null && value !== false
}

/** Every finite number of a value array. A null or a gap becomes 0, which draws flat. */
function numbers(raw: unknown): number[] {
  if (!Array.isArray(raw))
    return []
  return raw.slice(0, CHART_MAX_POINTS).map(entry => (typeof entry === 'number' && Number.isFinite(entry) ? entry : 0))
}

/** Every readable coordinate of a point array. A point missing either axis is dropped. */
function points(raw: unknown): ChartPoint[] {
  if (!Array.isArray(raw))
    return []
  return raw.slice(0, CHART_MAX_POINTS).flatMap((entry) => {
    if (!isObject(entry))
      return []
    const { x, y, r } = entry as { x?: unknown, y?: unknown, r?: unknown }
    if (typeof x !== 'number' || typeof y !== 'number' || !Number.isFinite(x) || !Number.isFinite(y))
      return []
    return [{ x, y, ...(typeof r === 'number' && Number.isFinite(r) && r > 0 ? { r } : {}) }]
  })
}

/** True when the data array carries coordinates rather than plain values. */
function isPointData(raw: unknown): boolean {
  return Array.isArray(raw) && raw.length > 0 && raw.every(isObject)
}

/** How many entries one dataset's own `data` array holds, before the cap applies. */
function dataLength(entry: Record<string, unknown>): number {
  return Array.isArray(entry.data) ? entry.data.length : 0
}

/**
 * Read one Chart.js v4 configuration into the shared chart source.
 *
 * `spec` is the JSON the provider returned, which for Kilo is the config it already
 * normalized -- it rewrites `area` into a filled `line` before it answers. This reads
 * both spellings anyway, because the row must not depend on a normalization that one
 * provider happens to run.
 *
 * Every failure answers a SOURCE carrying an `error`, never null. The row has already
 * committed to being a chart by the time this runs, and a null would send it back to
 * the raw-JSON dump this exists to replace.
 */
export function chartResultFromSpec(
  spec: string,
  meta: { title?: string, description?: string } = {},
): ChartResult {
  const base = { shape: 'bar' as ChartShape, labels: [], series: [], ...meta }
  let config: unknown
  try {
    config = JSON.parse(spec)
  }
  catch {
    return { ...base, error: 'The chart configuration is not readable JSON.' }
  }
  if (!isObject(config))
    return { ...base, error: 'The chart configuration is not an object.' }
  const declared = pickString(config, 'type')
  const shape = chartShape(declared)
  if (!shape)
    return { ...base, error: declared ? `Unknown chart type: ${declared}` : 'The chart configuration states no type.' }
  const data = isObject(config.data) ? config.data as Record<string, unknown> : undefined
  const declaredSeries = Array.isArray(data?.datasets) ? data.datasets.filter(isObject) : []
  const rawSeries = declaredSeries.slice(0, CHART_MAX_SERIES)
  const series: ChartSeries[] = rawSeries.map((entry) => {
    const own = chartShape(pickString(entry, 'type'))
    const fill = fillsArea(entry.fill)
    const raw = (entry as { data?: unknown }).data
    // The shape this series DRAWS: its own override when it carries one, else the
    // chart's. Resolved BEFORE the fill is read, because an override used to win
    // outright and the fill test then asked the CHART's shape -- so a `line` dataset
    // that filled inside a `bar` chart lost its fill and drew as a bare line.
    const effective = own ?? shape
    const drawn = fill && effective === 'line' ? 'area' : effective
    const label = pickString(entry, 'label')
    // `label` and `shape` stay ABSENT when the series states neither: an absent label
    // falls back to `Series N`, and `shapeOf` reads the chart's own for an absent one.
    // `area` is a filled line, so a filled series says so rather than leaving the
    // renderer to re-derive it from two fields.
    return {
      ...(label !== '' ? { label } : {}),
      // One dataset, one array: its own shape picks which half this series carries.
      ...(isPointData(raw) ? { points: points(raw) } : { values: numbers(raw) }),
      ...(drawn !== shape ? { shape: drawn } : {}),
    }
  })
  // The chart-level promotion reads the RAW datasets, not a `fill` field on the
  // series. Carrying one there put the same fact in two places -- `shape: area` and
  // `fill: true` -- where nothing outside this function ever read the second.
  const everyDatasetFills = rawSeries.length > 0 && rawSeries.every(entry => fillsArea(entry.fill))
  const declaredLabels = Array.isArray(data?.labels) ? data.labels : []
  const labels = declaredLabels
    .slice(0, CHART_MAX_POINTS)
    .map(label => (typeof label === 'string' ? label : String(label ?? '')))
  // What the caps above cut, stated once. The labels count too: the table draws one
  // row per label, so an uncapped label list rebuilt the cost the point cap removed.
  const truncated = declaredSeries.length > CHART_MAX_SERIES
    || declaredLabels.length > CHART_MAX_POINTS
    || rawSeries.some(entry => dataLength(entry) > CHART_MAX_POINTS)
  // A filled line at the CHART level is an area, which the renderer draws
  // differently. EVERY series must fill for the promotion to apply: a series that
  // states no shape of its own inherits the chart's, so promoting on one filled
  // dataset painted the unfilled ones as areas too, and the overlap hid them.
  const resolved = shape === 'line' && everyDatasetFills ? 'area' : shape
  return { ...base, shape: resolved, labels, series, ...(truncated ? { truncated } : {}) }
}

/** Whether the chart carries anything at all to draw. */
export function chartHasData(source: ChartResult): boolean {
  return !source.error && source.series.some(entry => seriesValues(entry).length > 0 || seriesPoints(entry).length > 0)
}

/**
 * The categorical half of a chart: the series across, the shared labels down.
 *
 * Empty when no series carries values, so a pure point chart writes no grid. A series
 * carries ONE array, so the one that holds coordinates has no column here at all --
 * and it keeps the position it holds in `source.series`, because the fallback name
 * states that position.
 */
function valueBlock(source: ChartResult): string[][] {
  const columns = source.series
    .map((series, index) => ({ series, index }))
    .filter(column => column.series.values !== undefined)
  if (columns.length === 0)
    return []
  const rows = Math.max(source.labels.length, ...columns.map(column => seriesValues(column.series).length), 0)
  return [
    ['', ...columns.map(column => chartSeriesName(column.series, column.index))],
    ...Array.from({ length: rows }, (_, row) => [
      source.labels[row] ?? `#${row + 1}`,
      ...columns.map((column) => {
        const value = seriesValues(column.series)[row]
        return value === undefined ? '' : String(value)
      }),
    ]),
  ]
}

/**
 * The coordinate half of a chart: one row per point, each named by its series.
 *
 * A coordinate has no category to sit under, so it cannot share the grid above.
 * Empty when no series carries points.
 */
function pointBlock(source: ChartResult): string[][] {
  const plotted = source.series.filter(series => seriesPoints(series).length > 0)
  if (plotted.length === 0)
    return []
  // A bubble states a radius and a scatter does not. One radius anywhere adds the
  // column to EVERY row, blank where the point states none, or the grid goes ragged
  // and a spreadsheet reads the next cell under the wrong heading.
  const hasRadius = plotted.some(series => seriesPoints(series).some(point => point.r !== undefined))
  return [
    ['', 'x', 'y', ...(hasRadius ? ['r'] : [])],
    ...source.series.flatMap((series, index) => seriesPoints(series).map(point => [
      chartSeriesName(series, index),
      String(point.x),
      String(point.y),
      ...(hasRadius ? [point.r === undefined ? '' : String(point.r)] : []),
    ])),
  ]
}

/**
 * The chart as text a reader can paste elsewhere.
 *
 * Tab-separated, with the series names as a header row, because that is what a
 * spreadsheet takes. The configuration is deliberately NOT what Copy writes: a reader
 * who copies a chart wants its numbers, and the JSON that produced it is what this
 * body exists to replace.
 *
 * One chart can carry BOTH halves: a Chart.js config may plot one dataset against the
 * labels while another plots coordinates. Each half writes its own block, and a blank
 * line separates them. Reading the values first and taking the whole chart down that
 * branch wrote a grid whose every coordinate cell was empty, so Copy lost every point
 * of a mixed chart.
 */
export function chartCopyableText(source: ChartResult): string {
  if (source.error)
    return source.error
  return [valueBlock(source), pointBlock(source)]
    .filter(block => block.length > 0)
    .map(block => block.map(line => line.join('\t')).join('\n'))
    .join('\n\n')
}
