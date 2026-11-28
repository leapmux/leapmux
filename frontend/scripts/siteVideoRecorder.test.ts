import { describe, expect, it } from 'vitest'
import {
  buildConcatManifest,
  buildVideoFilterGraph,
  buildZoompanExpressions,
  computeTargetCameraFrame,
  evaluateCameraAt,
  fullCameraFrame,
} from './siteVideoRecorder'

const VIEWPORT = { width: 1280, height: 800 }

describe('computeTargetCameraFrame', () => {
  it('centers on an interior target and preserves exact aspect ratio', () => {
    const frame = computeTargetCameraFrame(
      VIEWPORT,
      {
        x: 540,
        y: 300,
        width: 200,
        height: 120,
      },
      { padding: 40, maxZoom: 2.0 },
    )
    expect(frame.width / frame.height).toBeCloseTo(VIEWPORT.width / VIEWPORT.height, 6)
    // Fit zoom is min(1280/280, 800/200) = 4.0, capped to maxZoom 2.0.
    expect(frame.width).toBeCloseTo(640, 4)
    expect(frame.height).toBeCloseTo(400, 4)
    expect(frame.x).toBeCloseTo(640 - 320, 4)
    expect(frame.y).toBeCloseTo(360 - 200, 4)
  })

  it('clamps corner targets inside the viewport without distorting aspect ratio', () => {
    const topLeft = computeTargetCameraFrame(
      VIEWPORT,
      { x: 10, y: 8, width: 120, height: 40 },
      { padding: 32, maxZoom: 1.8 },
    )
    expect(topLeft.x).toBe(0)
    expect(topLeft.y).toBe(0)
    expect(topLeft.width / topLeft.height).toBeCloseTo(VIEWPORT.width / VIEWPORT.height, 6)

    const bottomRight = computeTargetCameraFrame(
      VIEWPORT,
      {
        x: 1180,
        y: 740,
        width: 90,
        height: 50,
      },
      { padding: 32, maxZoom: 1.8 },
    )
    expect(bottomRight.x + bottomRight.width).toBeCloseTo(VIEWPORT.width, 4)
    expect(bottomRight.y + bottomRight.height).toBeCloseTo(VIEWPORT.height, 4)
    expect(bottomRight.width / bottomRight.height).toBeCloseTo(VIEWPORT.width / VIEWPORT.height, 6)
  })

  it('falls back to full viewport when the target exceeds viewport dimensions', () => {
    const frame = computeTargetCameraFrame(
      VIEWPORT,
      { x: 0, y: 0, width: 1400, height: 900 },
      { padding: 40, maxZoom: 2.0 },
    )
    expect(frame).toEqual(fullCameraFrame(VIEWPORT))
  })
})

describe('evaluateCameraAt', () => {
  it('interpolates smoothly between keyframes while keeping every frame inside the viewport', () => {
    const full = fullCameraFrame(VIEWPORT)
    const zoomed = computeTargetCameraFrame(
      VIEWPORT,
      {
        x: 960,
        y: 60,
        width: 220,
        height: 180,
      },
      { padding: 48, maxZoom: 1.8 },
    )
    const keyframes = [
      { tMs: 0, frame: full },
      { tMs: 500, frame: full },
      { tMs: 1000, frame: zoomed },
      { tMs: 1500, frame: zoomed },
    ]

    expect(evaluateCameraAt(keyframes, VIEWPORT, -100)).toEqual(full)
    expect(evaluateCameraAt(keyframes, VIEWPORT, 250)).toEqual(full)
    expect(evaluateCameraAt(keyframes, VIEWPORT, 1200)).toEqual(zoomed)
    expect(evaluateCameraAt(keyframes, VIEWPORT, 3000)).toEqual(zoomed)

    // Smoothstep at midpoint u = 0.5 evaluates to exactly 0.5.
    const mid = evaluateCameraAt(keyframes, VIEWPORT, 750)
    expect(mid.x).toBeCloseTo((full.x + zoomed.x) / 2, 5)
    expect(mid.y).toBeCloseTo((full.y + zoomed.y) / 2, 5)
    expect(mid.width).toBeCloseTo((full.width + zoomed.width) / 2, 5)
    expect(mid.height).toBeCloseTo((full.height + zoomed.height) / 2, 5)

    for (let t = 500; t <= 1000; t += 25) {
      const f = evaluateCameraAt(keyframes, VIEWPORT, t)
      expect(f.x).toBeGreaterThanOrEqual(-1e-6)
      expect(f.y).toBeGreaterThanOrEqual(-1e-6)
      expect(f.x + f.width).toBeLessThanOrEqual(VIEWPORT.width + 1e-6)
      expect(f.y + f.height).toBeLessThanOrEqual(VIEWPORT.height + 1e-6)
      expect(f.width / f.height).toBeCloseTo(VIEWPORT.width / VIEWPORT.height, 5)
    }
  })
})

describe('buildZoompanExpressions', () => {
  it('emits constant identity expressions when the camera stays static at 1x', () => {
    const full = fullCameraFrame(VIEWPORT)
    const exprs = buildZoompanExpressions(
      [
        { tMs: 0, frame: full },
        { tMs: 3000, frame: full },
      ],
      VIEWPORT,
      2,
      30,
    )
    expect(exprs).toEqual({ z: '1', x: '0', y: '0' })
  })

  it('emits piecewise smoothstep expressions scaled by deviceScaleFactor', () => {
    const full = fullCameraFrame(VIEWPORT)
    const zoomed = {
      x: 320,
      y: 200,
      width: 640,
      height: 400,
    }
    const exprs = buildZoompanExpressions(
      [
        { tMs: 0, frame: full },
        { tMs: 500, frame: full },
        { tMs: 1000, frame: zoomed },
        { tMs: 2000, frame: zoomed },
      ],
      VIEWPORT,
      2,
      30,
    )
    expect(exprs.z).toContain('2560/')
    expect(exprs.z).toContain('if(lt(on,15),2560,if(lt(on,30),')
    expect(exprs.x).toContain('if(lt(on,15),0,if(lt(on,30),')
    expect(exprs.x).toContain('640')
    expect(exprs.y).toContain('400')
  })
})

describe('buildConcatManifest', () => {
  it('writes monotonic frame durations and escapes single quotes in paths', () => {
    const manifest = buildConcatManifest(
      [
        { path: '/tmp/a\'b/frame_00000.png', tMs: 0 },
        { path: '/tmp/a\'b/frame_00001.png', tMs: 400 },
        { path: '/tmp/a\'b/frame_00002.png', tMs: 1200 },
      ],
      1800,
    )
    expect(manifest).toBe([
      'file \'/tmp/a\'\\\'\'b/frame_00000.png\'',
      'duration 0.400000',
      'file \'/tmp/a\'\\\'\'b/frame_00001.png\'',
      'duration 0.800000',
      'file \'/tmp/a\'\\\'\'b/frame_00002.png\'',
      'duration 0.600000',
      'file \'/tmp/a\'\\\'\'b/frame_00002.png\'',
      '',
    ].join('\n'))
  })

  it('rejects an empty frame list', () => {
    expect(() => buildConcatManifest([], 1000)).toThrow(/without captured frames/i)
  })
})

describe('buildVideoFilterGraph', () => {
  it('appends a 1-second xfade to frame 0 when loopCrossfadeSec is positive', () => {
    const graph = buildVideoFilterGraph({
      fps: 30,
      width: 2560,
      height: 1600,
      durationSec: 9.5,
      loopCrossfadeSec: 1,
      zoompan: { z: '1', x: '0', y: '0' },
    })
    expect(graph.outputMap).toBe('[v]')
    expect(graph.totalOutputSec).toBeCloseTo(10.5, 5)
    expect(graph.filterComplex).toContain('tpad=stop_mode=clone:stop_duration=1.000[main]')
    expect(graph.filterComplex).toContain('[main][first]xfade=transition=fade:duration=1.000:offset=9.500')
    expect(graph.filterComplex).toContain('scale=out_color_matrix=bt709:flags=lanczos,format=yuv420p[v]')
  })

  it('omits the xfade branch when loopCrossfadeSec is zero', () => {
    const graph = buildVideoFilterGraph({
      fps: 30,
      width: 2560,
      height: 1600,
      durationSec: 8,
      loopCrossfadeSec: 0,
      zoompan: { z: '1', x: '0', y: '0' },
    })
    expect(graph.totalOutputSec).toBeCloseTo(8, 5)
    expect(graph.filterComplex).not.toContain('xfade')
    expect(graph.filterComplex).toContain('scale=out_color_matrix=bt709:flags=lanczos,format=yuv420p[v]')
  })
})
