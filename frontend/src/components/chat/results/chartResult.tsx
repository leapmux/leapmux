import type { JSX } from 'solid-js'
import type { ChartResult, ChartSeries, ChartShape } from '../model/chartResult'
import { createMemo, For, Show } from 'solid-js'
import { CHART_MAX_POINTS, CHART_MAX_SERIES, chartHasData, chartSeriesName, seriesPoints, seriesValues } from '../model/chartResult'
import {
  chartAxisLabel,
  chartAxisLine,
  chartBody,
  chartCanvas,
  chartDescription,
  chartHeading,
  chartLegend,
  chartLegendEntry,
  chartLegendSwatch,
  chartNotice,
  chartSeriesLine,
  chartTable,
  SERIES_COLORS,
} from './chartResult.css'

// The drawing area, in the SVG's own units. The element scales to the row's width,
// so these set the ASPECT and the density of the labels, never a pixel size.
const WIDTH = 320
const HEIGHT = 180
const PAD = { top: 8, right: 8, bottom: 18, left: 34 }
const PLOT = { w: WIDTH - PAD.left - PAD.right, h: HEIGHT - PAD.top - PAD.bottom }

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

function pointBlock(source: ChartResult): string[][] {
  const plotted = source.series.filter(series => seriesPoints(series).length > 0)
  if (plotted.length === 0)
    return []
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

export function chartCopyableText(source: ChartResult): string {
  if (source.error)
    return source.error
  return [valueBlock(source), pointBlock(source)]
    .filter(block => block.length > 0)
    .map(block => block.map(line => line.join('\t')).join('\n'))
    .join('\n\n')
}

/** The shapes drawn on an x/y plane. Everything else is a circle or a table. */
const CARTESIAN: ReadonlySet<ChartShape> = new Set(['bar', 'line', 'area', 'scatter', 'bubble'])
const CIRCULAR: ReadonlySet<ChartShape> = new Set(['pie', 'doughnut'])

function color(index: number): string {
  // The modulo keeps the index inside the table; `?? ''` is the type-level guard alone.
  return SERIES_COLORS[index % SERIES_COLORS.length] ?? ''
}

/**
 * The shape one series draws: its own when it states one, else the chart's.
 *
 * A series override only wins inside the chart's OWN family. The layout is chosen
 * once from `source.shape`, so a cartesian plot cannot draw a wedge and a circular
 * one cannot draw a bar -- a cross-family override matched no branch and the series
 * disappeared while the legend still listed it.
 */
function shapeOf(source: ChartResult, series: ChartSeries): ChartShape {
  if (series.shape === undefined)
    return source.shape
  const sameFamily = CARTESIAN.has(series.shape) === CARTESIAN.has(source.shape)
    && CIRCULAR.has(series.shape) === CIRCULAR.has(source.shape)
  return sameFamily ? series.shape : source.shape
}

/** A number as short as it can be read: no trailing zeros, thousands abbreviated. */
function axisNumber(value: number): string {
  const magnitude = Math.abs(value)
  if (magnitude >= 1_000_000)
    return `${Number((value / 1_000_000).toFixed(1))}M`
  if (magnitude >= 1000)
    return `${Number((value / 1000).toFixed(1))}k`
  return `${Number(value.toFixed(magnitude < 1 ? 2 : 0))}`
}

/**
 * The value range the vertical axis covers, always including zero.
 *
 * One running pass, and no spread. `Math.min(0, ...all)` passed every value of the
 * chart as a separate ARGUMENT, and V8 throws a `RangeError` past roughly 123,000 of
 * them -- so a large chart threw where it should have drawn. The `flatMap` beside it
 * also allocated a copy of the whole chart on each read, and the renderer reads this
 * several times per mark.
 */
function valueRange(source: ChartResult): { min: number, max: number } {
  // Zero is the floor and the ceiling of an empty chart, which is what the old
  // `Math.min(0, ...)` stated: the axis always covers zero.
  let min = 0
  let max = 0
  for (const series of source.series) {
    for (const value of seriesValues(series)) {
      if (value < min)
        min = value
      if (value > max)
        max = value
    }
    for (const point of seriesPoints(series)) {
      if (point.y < min)
        min = point.y
      if (point.y > max)
        max = point.y
    }
  }
  // A flat chart still needs a height, or every bar divides by zero.
  return max === min ? { min, max: max + 1 } : { min, max }
}

/** The horizontal range a point chart covers. One running pass, for the reason above. */
function pointRange(source: ChartResult): { min: number, max: number } {
  let min = Number.POSITIVE_INFINITY
  let max = Number.NEGATIVE_INFINITY
  for (const series of source.series) {
    for (const point of seriesPoints(series)) {
      if (point.x < min)
        min = point.x
      if (point.x > max)
        max = point.x
    }
  }
  // The two seeds cross only while no point has moved either of them, so this is the
  // empty case. A chart with no coordinates still needs a width to divide by.
  if (min > max)
    return { min: 0, max: 1 }
  return max === min ? { min, max: max + 1 } : { min, max }
}

/**
 * The legend of a pie or a doughnut, which identifies its SLICES.
 *
 * `CircularChart` colours each wedge by the value's index and draws no text of its
 * own, so the series legend below identifies neither the wedges nor their colours: a
 * three-region pie drew one swatch captioned with the dataset's name, in the first
 * region's colour. The slice labels live in `source.labels`, and this is the only
 * surface that states them.
 */
function CircularLegend(props: { source: ChartResult }): JSX.Element {
  const values = () => {
    const first = props.source.series[0]
    return first ? seriesValues(first) : []
  }
  return (
    <Show when={values().length > 0 && props.source.labels.length > 0}>
      <div class={chartLegend}>
        <For each={values()}>
          {(value, index) => (
            // A zero draws no wedge, so it earns no swatch either.
            <Show when={value > 0}>
              <span class={chartLegendEntry}>
                <span class={chartLegendSwatch} style={{ 'background-color': color(index()) }} />
                {props.source.labels[index()] ?? `#${index() + 1}`}
              </span>
            </Show>
          )}
        </For>
      </div>
    </Show>
  )
}

function ChartLegend(props: { source: ChartResult }): JSX.Element {
  return (
    <Show when={props.source.series.length > 1 || props.source.series.some(series => series.label)}>
      <div class={chartLegend}>
        <For each={props.source.series}>
          {(series, index) => (
            <Show when={series.label}>
              <span class={chartLegendEntry}>
                <span class={chartLegendSwatch} style={{ 'background-color': color(index()) }} />
                {series.label}
              </span>
            </Show>
          )}
        </For>
      </div>
    </Show>
  )
}

/**
 * A bar, line, area, scatter or bubble chart.
 *
 * One plane serves all five, because they differ only in what they put at each
 * coordinate: a rectangle, a vertex, a filled polygon or a dot.
 */
function CartesianChart(props: { source: ChartResult }): JSX.Element {
  // MEMOS, not plain functions. Every mark reads several of these, and a `<rect>`
  // read `valueRange` six times, `categories` three times and `barSeries` four times
  // -- each one a full walk of the chart. The cost was the point count times the
  // SQUARE of the series count, paid again on every reactive read and on every
  // remount the virtual list performs.
  const range = createMemo(() => valueRange(props.source))
  const xRange = createMemo(() => pointRange(props.source))
  const y = (value: number) => {
    const { min, max } = range()
    return PAD.top + PLOT.h - ((value - min) / (max - min)) * PLOT.h
  }
  const categories = createMemo(() => Math.max(
    props.source.labels.length,
    ...props.source.series.map(series => seriesValues(series).length),
    1,
  ))
  // The centre of category `index`, which every categorical shape shares.
  const centre = (index: number) => PAD.left + (PLOT.w / categories()) * (index + 0.5)
  const px = (value: number) => {
    const { min, max } = xRange()
    return PAD.left + ((value - min) / (max - min)) * PLOT.w
  }
  const barSeries = createMemo(() => props.source.series.filter(series => shapeOf(props.source, series) === 'bar'))
  // The position each bar series holds inside that group, by identity. A rectangle
  // needs its own offset, and `indexOf` answered it with a scan per rectangle.
  const barSlots = createMemo(() => new Map(barSeries().map((series, index) => [series, index] as const)))
  const barWidth = createMemo(() => (PLOT.w / categories()) * 0.7 / Math.max(barSeries().length, 1))
  // How many category names the axis draws, thinned so they never overlap.
  const labelStep = createMemo(() => Math.ceil(props.source.labels.length / 8) || 1)

  return (
    <svg class={chartCanvas} viewBox={`0 0 ${WIDTH} ${HEIGHT}`} role="img" aria-label={props.source.title || 'Chart'}>
      {/* The baseline sits at zero when the data crosses it, and at the floor otherwise. */}
      <line class={chartAxisLine} x1={PAD.left} y1={y(Math.max(range().min, 0))} x2={WIDTH - PAD.right} y2={y(Math.max(range().min, 0))} />
      <line class={chartAxisLine} x1={PAD.left} y1={PAD.top} x2={PAD.left} y2={PAD.top + PLOT.h} />
      <text class={chartAxisLabel} x={PAD.left - 4} y={PAD.top + 4} text-anchor="end">{axisNumber(range().max)}</text>
      <text class={chartAxisLabel} x={PAD.left - 4} y={PAD.top + PLOT.h} text-anchor="end">{axisNumber(range().min)}</text>

      <For each={props.source.series}>
        {(series, index) => {
          const shape = () => shapeOf(props.source, series)
          const stroke = () => color(index())
          const barIndex = () => barSlots().get(series) ?? 0
          return (
            <>
              <Show when={shape() === 'bar'}>
                <For each={seriesValues(series)}>
                  {(value, category) => {
                    const left = () => centre(category()) - (barWidth() * barSeries().length) / 2 + barWidth() * barIndex()
                    const zero = () => y(Math.max(range().min, 0))
                    return (
                      <rect
                        x={left()}
                        y={Math.min(y(value), zero())}
                        width={Math.max(barWidth() - 1, 1)}
                        height={Math.max(Math.abs(zero() - y(value)), 1)}
                        fill={stroke()}
                      />
                    )
                  }}
                </For>
              </Show>

              <Show when={shape() === 'line' || shape() === 'area'}>
                <Show when={shape() === 'area' && seriesValues(series).length > 0}>
                  <polygon
                    fill={stroke()}
                    fill-opacity="0.18"
                    points={[
                      `${centre(0)},${y(Math.max(range().min, 0))}`,
                      ...seriesValues(series).map((value, category) => `${centre(category)},${y(value)}`),
                      `${centre(seriesValues(series).length - 1)},${y(Math.max(range().min, 0))}`,
                    ].join(' ')}
                  />
                </Show>
                <polyline
                  class={chartSeriesLine}
                  stroke={stroke()}
                  points={seriesValues(series).map((value, category) => `${centre(category)},${y(value)}`).join(' ')}
                />
              </Show>

              <Show when={shape() === 'scatter' || shape() === 'bubble'}>
                <For each={seriesPoints(series)}>
                  {point => (
                    <circle
                      cx={px(point.x)}
                      cy={y(point.y)}
                      r={shape() === 'bubble' ? Math.max(Math.min(point.r ?? 3, 20), 2) : 3}
                      fill={stroke()}
                      fill-opacity={shape() === 'bubble' ? 0.55 : 1}
                    />
                  )}
                </For>
              </Show>
            </>
          )
        }}
      </For>

      {/* Category names, thinned so they never overlap at a narrow width. */}
      <For each={props.source.labels}>
        {(label, index) => (
          <Show when={index() % labelStep() === 0}>
            <text class={chartAxisLabel} x={centre(index())} y={HEIGHT - 6} text-anchor="middle">{label}</text>
          </Show>
        )}
      </For>
    </svg>
  )
}

/**
 * Every slice of one circle, walked from twelve o'clock.
 *
 * A plain function over plain numbers, so it captures no accessor: a reactive read
 * held in a closure here would be a value the chart never refreshes.
 */
function arcsOf(values: number[]): { index: number, start: number, end: number, sweep: number }[] {
  const whole = values.reduce((sum, value) => sum + value, 0)
  let angle = -Math.PI / 2
  return values.map((value, index) => {
    const sweep = whole > 0 ? (value / whole) * Math.PI * 2 : 0
    const start = angle
    angle += sweep
    return { index, start, end: angle, sweep }
  })
}

/** A pie or a doughnut, drawn from the FIRST series -- the only one either shape shows. */
function CircularChart(props: { source: ChartResult }): JSX.Element {
  const values = () => {
    const first = props.source.series[0]
    return (first ? seriesValues(first) : []).map(value => Math.max(value, 0))
  }
  const radius = 70
  const inner = () => (props.source.shape === 'doughnut' ? radius * 0.55 : 0)
  const centre = { x: WIDTH / 2, y: HEIGHT / 2 }

  const arcs = () => arcsOf(values())

  const point = (angle: number, r: number) => `${centre.x + Math.cos(angle) * r} ${centre.y + Math.sin(angle) * r}`

  return (
    <svg class={chartCanvas} viewBox={`0 0 ${WIDTH} ${HEIGHT}`} role="img" aria-label={props.source.title || 'Chart'}>
      <For each={arcs()}>
        {arc => (
          <Show when={arc.sweep > 0}>
            <path
              fill={color(arc.index)}
              d={
                // A full circle has no arc: its start and end land on the same point,
                // and the path would collapse to nothing.
                arc.sweep >= Math.PI * 2 - 1e-6
                  // The inner circle winds the other way (sweep flag 0), so the
                  // default fill rule cuts it out. Without it a doughnut whose whole
                  // total sits in one category draws as a filled pie.
                  ? [
                      `M ${centre.x} ${centre.y - radius} A ${radius} ${radius} 0 1 1 ${centre.x - 0.01} ${centre.y - radius} Z`,
                      ...(inner() > 0 ? [`M ${centre.x} ${centre.y - inner()} A ${inner()} ${inner()} 0 1 0 ${centre.x - 0.01} ${centre.y - inner()} Z`] : []),
                    ].join(' ')
                  : [
                      `M ${point(arc.start, inner())}`,
                      `L ${point(arc.start, radius)}`,
                      `A ${radius} ${radius} 0 ${arc.sweep > Math.PI ? 1 : 0} 1 ${point(arc.end, radius)}`,
                      `L ${point(arc.end, inner())}`,
                      ...(inner() > 0 ? [`A ${inner()} ${inner()} 0 ${arc.sweep > Math.PI ? 1 : 0} 0 ${point(arc.start, inner())}`] : []),
                      'Z',
                    ].join(' ')
              }
            />
          </Show>
        )}
      </For>
    </svg>
  )
}

/**
 * The values as a table.
 *
 * The answer for a shape this build draws no picture for -- a radar and a polar area
 * both plot on angular axes, which share nothing with the two planes above. The
 * reader still gets every number the model charted, which is what the raw
 * configuration dump buried.
 */
function ChartTable(props: { source: ChartResult }): JSX.Element {
  const rows = () => {
    const count = Math.max(props.source.labels.length, ...props.source.series.map(series => seriesValues(series).length), 0)
    return Array.from({ length: count }, (_, index) => index)
  }
  return (
    <table class={chartTable}>
      <thead>
        <tr>
          <th>{' '}</th>
          <For each={props.source.series}>{(series, index) => <th>{chartSeriesName(series, index())}</th>}</For>
        </tr>
      </thead>
      <tbody>
        <For each={rows()}>
          {index => (
            <tr>
              <td>{props.source.labels[index] ?? `#${index + 1}`}</td>
              <For each={props.source.series}>{series => <td>{seriesValues(series)[index] ?? ''}</td>}</For>
            </tr>
          )}
        </For>
      </tbody>
    </table>
  )
}

/**
 * One chart, as the provider's configuration described it.
 *
 * The three layouts below are chosen by SHAPE, and a shape with no picture falls to
 * the table rather than to the configuration itself: a reader who asked for a chart
 * is owed the numbers, not the JSON that would have produced one.
 */
export function ChartResultBody(props: { source: ChartResult }): JSX.Element {
  return (
    <div class={chartBody}>
      <Show when={props.source.title}>{title => <div class={chartHeading}>{title()}</div>}</Show>
      <Show when={props.source.description}>{text => <div class={chartDescription}>{text()}</div>}</Show>
      <Show
        when={!props.source.error}
        fallback={<div class={chartNotice.error}>{props.source.error}</div>}
      >
        {/* The parser caps what it keeps, so the reader must hear that the picture,
            the table and the copied text all hold a part of the chart. Without this a
            complete-looking chart of the first slice says nothing about the rest. */}
        <Show when={props.source.truncated}>
          <div class={chartNotice.plain}>
            {`This chart carries more than the row draws. It shows at most ${CHART_MAX_SERIES} series, and at most ${CHART_MAX_POINTS} points of each.`}
          </div>
        </Show>
        <Show
          when={chartHasData(props.source)}
          fallback={<div class={chartNotice.plain}>The chart carries no data.</div>}
        >
          <Show when={CARTESIAN.has(props.source.shape)}>
            <CartesianChart source={props.source} />
          </Show>
          <Show when={CIRCULAR.has(props.source.shape)}>
            <CircularChart source={props.source} />
          </Show>
          <Show when={!CARTESIAN.has(props.source.shape) && !CIRCULAR.has(props.source.shape)}>
            <div class={chartNotice.plain}>{`A ${props.source.shape} chart draws on angular axes, which this build does not plot. Its values:`}</div>
            <ChartTable source={props.source} />
          </Show>
          <Show when={CIRCULAR.has(props.source.shape)} fallback={<ChartLegend source={props.source} />}>
            <CircularLegend source={props.source} />
          </Show>
        </Show>
      </Show>
    </div>
  )
}
