import { describe, expect, it } from 'vitest'
import { CHART_MAX_POINTS, CHART_MAX_SERIES, chartCopyableText, chartHasData, chartResultFromSpec, seriesPoints, seriesValues } from './chartResult'

// The shapes here are the ones Kilo's own `chart` tool declares: bar, bubble, pie,
// doughnut, line (with `fill:true` for an area), mixed, polarArea, radar and scatter.
// It answers with the configuration it normalized -- `type:"area"` is rewritten to a
// filled line before it replies -- so both spellings are read.

describe('chartResultFromSpec', () => {
  it('reads a categorical chart', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bar',
      data: { labels: ['A', 'B'], datasets: [{ label: 'Hits', data: [1, 2] }] },
    }), { title: 'Weekly hits', description: 'per region' })

    expect(source.shape).toBe('bar')
    expect(source.title).toBe('Weekly hits')
    expect(source.description).toBe('per region')
    expect(source.labels).toEqual(['A', 'B'])
    // A categorical series carries VALUES and no `points` key at all: one dataset
    // holds one array, so the half it does not plot is absent rather than empty.
    expect(source.series).toEqual([{ label: 'Hits', values: [1, 2], shape: undefined }])
    // The KEYS, because `toEqual` ignores an undefined-valued property -- so a series
    // that grew a field back would still match the expectation above.
    expect(Object.keys(source.series[0]!).sort()).toEqual(['label', 'shape', 'values'])
    expect(chartHasData(source)).toBe(true)
  })

  it('reads a filled line as an area, whichever half declares the fill', () => {
    const spec = { type: 'line', data: { labels: ['A'], datasets: [{ data: [1], fill: true }] } }
    expect(chartResultFromSpec(JSON.stringify(spec)).shape).toBe('area')
    // Kilo rewrites `type:"area"` itself, so the row must not depend on that pass.
    expect(chartResultFromSpec(JSON.stringify({ ...spec, type: 'area' })).shape).toBe('area')
  })

  // Chart.js v4 refuses a fill on `false` alone. Every other spelling asks for one,
  // and `'origin'` is what the library's own area samples emit -- so a test for
  // `true` drew those samples as bare lines. `0` is the index of the first dataset,
  // which is why the test cannot be one for truthiness either.
  it.each([
    ['true', true],
    ['the boundary word origin', 'origin'],
    ['the boundary word start', 'start'],
    ['the boundary word end', 'end'],
    ['the stacked-value word stack', 'stack'],
    ['the dataset index 0', 0],
    ['the dataset index 1', 1],
    ['the relative dataset index "+1"', '+1'],
    ['an object target', { value: 0 }],
  ])('promotes a line whose fill states %s', (_label, fill) => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'line',
      data: { labels: ['A'], datasets: [{ data: [1], fill }] },
    }))
    expect(source.shape).toBe('area')
  })

  it.each([
    ['false', false],
    ['null', null],
    ['no fill at all', undefined],
  ])('leaves a line whose fill states %s a line', (_label, fill) => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'line',
      data: { labels: ['A'], datasets: [{ data: [1], ...(fill === undefined ? {} : { fill }) }] },
    }))
    expect(source.shape).toBe('line')
    expect(source.series[0]?.shape).toBeUndefined()
  })

  it('keeps a series that overrides the chart shape', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bar',
      data: { labels: ['A'], datasets: [{ label: 'Bars', data: [1] }, { label: 'Trend', type: 'line', data: [2] }] },
    }))
    expect(source.shape).toBe('bar')
    expect(source.series.map(series => series.shape)).toEqual([undefined, 'line'])
  })

  // The override and the fill are two facts about one dataset. Reading the override
  // first and then asking the CHART's shape lost the second: a `line` dataset that
  // filled inside a `bar` chart answered `line`, and the row drew no band under it.
  it('keeps the fill of a series that also overrides the chart shape', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bar',
      data: {
        labels: ['A'],
        datasets: [{ label: 'Bars', data: [1] }, { label: 'Trend', type: 'line', fill: true, data: [2] }],
      },
    }))
    expect(source.shape).toBe('bar')
    expect(source.series.map(series => series.shape)).toEqual([undefined, 'area'])
  })

  // A dataset that overrides the type to the chart's OWN shape states nothing, so
  // `shapeOf` reads the chart's. A stated shape equal to the chart's would be a
  // second copy of one fact.
  it('states no shape for a series that overrides the type to the chart\'s own', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bar',
      data: { labels: ['A'], datasets: [{ type: 'bar', data: [1] }] },
    }))
    expect(source.series[0]?.shape).toBeUndefined()
  })

  it('reads coordinates apart from values', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bubble',
      data: { datasets: [{ data: [{ x: 1, y: 2, r: 5 }, { x: 3, y: 4 }] }] },
    }))
    const series = source.series[0]!
    expect(series.values).toBeUndefined()
    expect(seriesValues(series)).toEqual([])
    expect(seriesPoints(series)).toEqual([{ x: 1, y: 2, r: 5 }, { x: 3, y: 4 }])
  })

  it('drops a coordinate that states no axis, and flattens a gap to zero', () => {
    const points = chartResultFromSpec(JSON.stringify({ type: 'scatter', data: { datasets: [{ data: [{ x: 1, y: 2 }, { x: 'a', y: 2 }, { y: 3 }] }] } }))
    expect(points.series[0]?.points).toEqual([{ x: 1, y: 2 }])
    const values = chartResultFromSpec(JSON.stringify({ type: 'bar', data: { labels: ['A', 'B', 'C'], datasets: [{ data: [1, null, 'x'] }] } }))
    expect(values.series[0]?.values).toEqual([1, 0, 0])
  })

  it('states a readable reason for a configuration it cannot read', () => {
    // The three the tool itself rejects, plus a type from no release.
    expect(chartResultFromSpec('not json').error).toBe('The chart configuration is not readable JSON.')
    expect(chartResultFromSpec('[1,2]').error).toBe('The chart configuration is not an object.')
    expect(chartResultFromSpec('{"data":{}}').error).toBe('The chart configuration states no type.')
    expect(chartResultFromSpec('{"type":"treemap","data":{}}').error).toBe('Unknown chart type: treemap')
  })

  it('keeps the heading on a configuration it cannot read', () => {
    // The row has already committed to being a chart, so the reason replaces the
    // picture and the title above it stays.
    const source = chartResultFromSpec('not json', { title: 'Weekly hits' })
    expect(source.title).toBe('Weekly hits')
    expect(chartHasData(source)).toBe(false)
  })

  it('separates a chart with no data from one it could not read', () => {
    const empty = chartResultFromSpec(JSON.stringify({ type: 'pie', data: { labels: [], datasets: [] } }))
    expect(empty.error).toBeUndefined()
    expect(chartHasData(empty)).toBe(false)
  })

  // The cap lives HERE, not in the renderer, so the picture, the table and the copied
  // text read one set of numbers. The flag is what lets the row say the rest was cut.
  it('caps the points of one series and states that it did', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'line',
      data: {
        labels: Array.from({ length: CHART_MAX_POINTS + 5 }, (_, index) => `L${index}`),
        datasets: [{ data: Array.from({ length: CHART_MAX_POINTS + 5 }, (_, index) => index) }],
      },
    }))
    expect(seriesValues(source.series[0]!)).toHaveLength(CHART_MAX_POINTS)
    expect(source.labels).toHaveLength(CHART_MAX_POINTS)
    expect(source.truncated).toBe(true)
  })

  it('caps the coordinates of one series and states that it did', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'scatter',
      data: { datasets: [{ data: Array.from({ length: CHART_MAX_POINTS + 1 }, (_, index) => ({ x: index, y: index })) }] },
    }))
    expect(seriesPoints(source.series[0]!)).toHaveLength(CHART_MAX_POINTS)
    expect(source.truncated).toBe(true)
  })

  it('caps the series count and states that it did', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bar',
      data: { labels: ['A'], datasets: Array.from({ length: CHART_MAX_SERIES + 3 }, () => ({ data: [1] })) },
    }))
    expect(source.series).toHaveLength(CHART_MAX_SERIES)
    expect(source.truncated).toBe(true)
  })

  it('states no truncation for a chart that fits', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bar',
      data: { labels: ['A', 'B'], datasets: [{ data: [1, 2] }] },
    }))
    expect(source.truncated).toBeUndefined()
  })
})

describe('chartCopyableText', () => {
  it('writes the numbers a spreadsheet takes, not the configuration', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bar',
      data: { labels: ['A', 'B'], datasets: [{ label: 'Hits', data: [1, 2] }, { data: [3, 4] }] },
    }))
    expect(chartCopyableText(source)).toBe('\tHits\tSeries 2\nA\t1\t3\nB\t2\t4')
  })

  it('writes a point chart as its coordinates', () => {
    const source = chartResultFromSpec(JSON.stringify({ type: 'scatter', data: { datasets: [{ label: 'P', data: [{ x: 1, y: 2 }] }] } }))
    expect(chartCopyableText(source)).toBe('\tx\ty\nP\t1\t2')
  })

  // One Chart.js config can plot one dataset against the labels and another as
  // coordinates. Reading the values first sent the WHOLE chart down the categorical
  // branch, where every cell of the point series is empty -- so Copy handed over a
  // grid with every coordinate gone.
  it('writes both halves of a chart that carries values and coordinates', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bar',
      data: {
        labels: ['A', 'B'],
        datasets: [
          { label: 'Hits', data: [1, 2] },
          { label: 'Deals', type: 'scatter', data: [{ x: 3, y: 4 }] },
        ],
      },
    }))
    expect(chartCopyableText(source)).toBe('\tHits\nA\t1\nB\t2\n\n\tx\ty\nDeals\t3\t4')
  })

  // The fallback name states the series' position in the CHART, not its position
  // inside the block it writes. A point series between two value series would
  // otherwise be copied as "Series 2" under both headings.
  it('names an unlabelled series by its position in the chart', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bar',
      data: {
        labels: ['A'],
        datasets: [
          { type: 'scatter', data: [{ x: 9, y: 9 }] },
          { data: [1] },
        ],
      },
    }))
    expect(chartCopyableText(source)).toBe('\tSeries 2\nA\t1\n\n\tx\ty\nSeries 1\t9\t9')
  })

  // A bubble states a radius and a scatter does not. One radius anywhere adds the
  // column, so a row without one needs a blank cell or the grid goes ragged.
  it('pads the radius column of a point that states none', () => {
    const source = chartResultFromSpec(JSON.stringify({
      type: 'bubble',
      data: { datasets: [{ label: 'P', data: [{ x: 1, y: 2, r: 5 }, { x: 3, y: 4 }] }] },
    }))
    expect(chartCopyableText(source)).toBe('\tx\ty\tr\nP\t1\t2\t5\nP\t3\t4\t')
  })

  it('writes the reason for a chart it could not read', () => {
    expect(chartCopyableText(chartResultFromSpec('not json'))).toBe('The chart configuration is not readable JSON.')
  })
})
