import type { ChartResult } from '../model/chartResult'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { CHART_MAX_POINTS, CHART_MAX_SERIES, chartResultFromSpec } from '../model/chartResult'
import { ChartResultBody } from './chartResult'

function draw(spec: Record<string, unknown>, meta?: { title?: string, description?: string }) {
  return render(() => <ChartResultBody source={chartResultFromSpec(JSON.stringify(spec), meta)} />).container
}

describe('the chart body (ChartResultBody)', () => {
  it('draws a bar for each value', () => {
    const container = draw({ type: 'bar', data: { labels: ['A', 'B', 'C'], datasets: [{ data: [1, 2, 3] }] } })
    expect(container.querySelectorAll('rect')).toHaveLength(3)
    expect(container.querySelector('svg')?.getAttribute('role')).toBe('img')
  })

  it('draws one polyline per line series and fills an area', () => {
    const line = draw({ type: 'line', data: { labels: ['A', 'B'], datasets: [{ data: [1, 2] }, { data: [3, 4] }] } })
    expect(line.querySelectorAll('polyline')).toHaveLength(2)
    expect(line.querySelectorAll('polygon')).toHaveLength(0)

    const area = draw({ type: 'line', data: { labels: ['A', 'B'], datasets: [{ data: [1, 2], fill: true }] } })
    expect(area.querySelectorAll('polygon')).toHaveLength(1)
  })

  it('draws an arc per slice, and one closed path for a single full slice', () => {
    const pie = draw({ type: 'pie', data: { labels: ['A', 'B'], datasets: [{ data: [3, 1] }] } })
    expect(pie.querySelectorAll('path')).toHaveLength(2)

    // A single slice sweeps the whole circle. Its start and end land on the same
    // point, so the ordinary arc would collapse to nothing.
    const whole = draw({ type: 'pie', data: { labels: ['A'], datasets: [{ data: [5] }] } })
    const path = whole.querySelector('path')?.getAttribute('d') ?? ''
    expect(whole.querySelectorAll('path')).toHaveLength(1)
    expect(path).toContain('A 70 70')
    expect(path.endsWith('Z')).toBe(true)
  })

  it('draws a dot per point, and sizes a bubble by its radius', () => {
    const scatter = draw({ type: 'scatter', data: { datasets: [{ data: [{ x: 1, y: 2 }, { x: 3, y: 4 }] }] } })
    expect(scatter.querySelectorAll('circle')).toHaveLength(2)

    const bubble = draw({ type: 'bubble', data: { datasets: [{ data: [{ x: 1, y: 2, r: 12 }] }] } })
    expect(bubble.querySelector('circle')?.getAttribute('r')).toBe('12')
  })

  // A radar and a polar area plot on angular axes, which neither plane above shares.
  // The reader still gets every number, which the configuration dump buried.
  it('falls back to the values for a shape it draws no picture for', () => {
    const container = draw({ type: 'radar', data: { labels: ['Speed', 'Range'], datasets: [{ label: 'Now', data: [7, 3] }] } })
    expect(container.querySelector('svg')).toBeNull()
    expect(container.querySelector('table')).toBeTruthy()
    expect(container.textContent).toContain('angular axes')
    expect(container.textContent).toContain('Speed')
    expect(container.textContent).toContain('Now')
  })

  it('states the heading and the note the tool asked the model for', () => {
    const container = draw(
      { type: 'bar', data: { labels: ['A'], datasets: [{ data: [1] }] } },
      { title: 'Weekly hits', description: 'per region' },
    )
    expect(container.textContent).toContain('Weekly hits')
    expect(container.textContent).toContain('per region')
  })

  it('names each series once it carries more than one', () => {
    const container = draw({ type: 'bar', data: { labels: ['A'], datasets: [{ label: 'Hits', data: [1] }, { label: 'Misses', data: [2] }] } })
    expect(container.textContent).toContain('Hits')
    expect(container.textContent).toContain('Misses')
  })

  it('states the reason instead of a picture when the configuration is unreadable', () => {
    const container = render(() => <ChartResultBody source={chartResultFromSpec('not json', { title: 'Weekly hits' })} />).container
    expect(container.querySelector('svg')).toBeNull()
    expect(container.textContent).toContain('Weekly hits')
    expect(container.textContent).toContain('not readable JSON')
  })

  it('says so when a readable configuration carries no data', () => {
    const container = draw({ type: 'pie', data: { labels: [], datasets: [] } })
    expect(container.querySelector('svg')).toBeNull()
    expect(container.textContent).toContain('carries no data')
  })

  // A flat series divides by zero without this: every value maps to the same row of
  // the plane, so the range has no height to scale against.
  it('draws a flat series without dividing by zero', () => {
    const container = draw({ type: 'bar', data: { labels: ['A', 'B'], datasets: [{ data: [0, 0] }] } })
    for (const rect of container.querySelectorAll('rect'))
      expect(Number(rect.getAttribute('height'))).toBeGreaterThan(0)
  })

  // The parser caps what it keeps, so the reader must hear that the picture and the
  // copied text hold one part of the chart. A complete-looking picture of the first
  // slice says nothing about the rest.
  it('says so when the chart carries more than the row draws', () => {
    const container = draw({
      type: 'bar',
      data: { labels: ['A'], datasets: Array.from({ length: CHART_MAX_SERIES + 1 }, () => ({ data: [1] })) },
    })
    expect(container.textContent).toContain(`at most ${CHART_MAX_SERIES} series`)
    expect(container.textContent).toContain(`at most ${CHART_MAX_POINTS} points`)
  })

  it('says nothing about truncation for a chart that fits', () => {
    const container = draw({ type: 'bar', data: { labels: ['A'], datasets: [{ data: [1] }] } })
    expect(container.textContent).not.toContain('at most')
  })

  /**
   * The vertical range walks the data; it never SPREADS it.
   *
   * `Math.min(0, ...values)` passed every value as a separate argument, and V8 throws
   * a `RangeError` past roughly 123,000 of them -- so a chart this size threw where
   * it should have drawn. The source is built here rather than parsed, because the
   * parser caps a series long before this count; a caller that builds the model itself
   * has no such cap.
   *
   * A `scatter` whose series carries plain VALUES is the cheap way to reach the range
   * walk: `chartResultFromSpec` answers exactly this for `type:"scatter"` with a
   * number array, and the scatter branch plots `seriesPoints`, which is empty -- so
   * the walk runs and no mark is laid out.
   */
  it('draws a chart of more values than Math.min takes arguments', () => {
    const source: ChartResult = {
      shape: 'scatter',
      labels: [],
      series: [{ values: Array.from({ length: 130_000 }, (_, index) => index) }],
    }
    const container = render(() => <ChartResultBody source={source} />).container
    expect(container.querySelector('svg')).not.toBeNull()
    expect(container.textContent).not.toContain('carries no data')
  })
})
