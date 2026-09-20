import { describe, expect, it } from 'vitest'
import { chartResultFromSpec } from '../model/chartResult'
import { chartCopyableText } from './chartResult'

describe('chart series shapes', () => {
  // Chart.js reads `fill` per dataset. Promoting the whole chart to `area` because
  // ONE dataset fills made every series that stated no shape of its own inherit it,
  // so a plain line drew as a second filled band and the overlap hid the first.
  it('keeps a mixed fill/no-fill line chart a line chart', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'line',
      data: {
        labels: ['Q1', 'Q2'],
        datasets: [
          { label: 'Budget', data: [10, 20], fill: true },
          { label: 'Actual', data: [8, 18] },
        ],
      },
    }))
    expect(source?.shape).toBe('line')
    expect(source?.series[1]?.shape).toBeUndefined()
  })

  it('promotes a chart whose every series fills', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'line',
      data: { labels: ['Q1'], datasets: [{ label: 'Budget', data: [10], fill: true }] },
    }))
    expect(source?.shape).toBe('area')
  })
})

describe('chartCopyableText', () => {
  // A scatter or bubble config may state `data.labels` beside its points. Asking
  // the LABEL count whether the chart is categorical then chose the category grid,
  // whose every cell is empty for point data -- so Copy handed over a header and N
  // rows of empty tabs with every coordinate gone.
  it('copies the coordinates of a point chart that also states labels', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bubble',
      data: {
        labels: ['Q1', 'Q2'],
        datasets: [{ label: 'Deals', data: [{ x: 1, y: 2, r: 5 }, { x: 3, y: 4, r: 7 }] }],
      },
    }))
    const text = chartCopyableText(source!)
    expect(text).toContain('Deals')
    expect(text).toContain('1')
    expect(text).toContain('2')
    expect(text).toContain('5')
    expect(text).not.toMatch(/Q1\t\s*$/m)
  })

  it('still copies a categorical chart as a grid', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bar',
      data: { labels: ['Q1'], datasets: [{ label: 'Revenue', data: [42] }] },
    }))
    expect(chartCopyableText(source!)).toContain('42')
  })
})
