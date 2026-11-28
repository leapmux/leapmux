import type { CDPSession, Locator, Page } from '@playwright/test'
import { Buffer } from 'node:buffer'
import { execFile } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)

export interface ViewportSize {
  width: number
  height: number
}

export interface Rect {
  x: number
  y: number
  width: number
  height: number
}

/**
 * Camera crop rectangle in CSS viewport coordinates.
 *
 * Invariants:
 *   0 <= x <= viewport.width - width
 *   0 <= y <= viewport.height - height
 *   width / height === viewport.width / viewport.height
 *   zoom = viewport.width / width >= 1
 */
export interface CameraFrame {
  x: number
  y: number
  width: number
  height: number
}

export interface CameraKeyframe {
  tMs: number
  frame: CameraFrame
}

export interface ZoomOptions {
  /** Margin in CSS pixels around the target box (default: 56). */
  padding?: number
  /** Upper limit on zoom factor so zoomed crops stay sharp on 2x sources (default: 1.85). */
  maxZoom?: number
  /** Lower limit on zoom factor (default: 1.0). */
  minZoom?: number
  /** Smooth pan/zoom transition duration in milliseconds (default: 450). */
  transitionMs?: number
  /** Wait for the transition duration before returning (default: true). */
  waitForTransition?: boolean
}

export interface RecorderOptions {
  /** Temporary directory for captured lossless PNG frames and concat manifest. */
  workDir: string
  /** Logical CSS viewport size. */
  viewport: ViewportSize
  /** Device pixel ratio of the recording browser (default: 2). */
  scaleFactor?: number
  /** Constant output framerate in frames per second (default: 30). */
  fps?: number
  /** H.264 Constant Rate Factor for visually lossless encoding (default: 17). */
  crf?: number
  /** x264 preset (default: 'slow'). */
  preset?: string
  /**
   * Duration in milliseconds of the smooth crossfade appended at the end of the
   * clip back to frame 0 so HTML `<video loop>` loops seamlessly (default: 1000;
   * pass 0 to disable).
   */
  loopCrossfadeMs?: number
  /** Inject the crisp vector demo cursor and click/wheel pulse overlay (default: true). */
  showCursor?: boolean
}

interface CapturedFrame {
  path: string
  tMs: number
}

export interface ZoompanExpressions {
  z: string
  x: string
  y: string
}

export interface VideoFilterGraphOptions {
  fps: number
  width: number
  height: number
  durationSec: number
  loopCrossfadeSec: number
  zoompan: ZoompanExpressions
}

const EPSILON = 1e-4

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

function formatNum(value: number): string {
  const rounded = Math.abs(value) < 1e-6
    ? 0
    : Number(value.toFixed(4))
  return String(rounded)
}

/** Return the full-viewport camera frame (zoom 1.0, origin 0,0). */
export function fullCameraFrame(viewport: ViewportSize): CameraFrame {
  return {
    x: 0,
    y: 0,
    width: viewport.width,
    height: viewport.height,
  }
}

/**
 * Compute a clamped camera frame focused on `target` that preserves the exact
 * viewport aspect ratio and stays within `[0, viewport.width] x [0, viewport.height]`.
 */
export function computeTargetCameraFrame(
  viewport: ViewportSize,
  target: Rect,
  options: Pick<ZoomOptions, 'padding' | 'maxZoom' | 'minZoom'> = {},
): CameraFrame {
  const padding = Math.max(0, options.padding ?? 56)
  const minZoom = Math.max(1, options.minZoom ?? 1)
  const maxZoom = Math.max(minZoom, options.maxZoom ?? 1.85)

  const paddedWidth = Math.max(1, target.width + padding * 2)
  const paddedHeight = Math.max(1, target.height + padding * 2)
  const fitZoom = Math.min(viewport.width / paddedWidth, viewport.height / paddedHeight)
  const zoom = clamp(fitZoom, minZoom, maxZoom)

  const width = viewport.width / zoom
  const height = viewport.height / zoom
  const centerX = target.x + target.width / 2
  const centerY = target.y + target.height / 2

  return {
    x: clamp(centerX - width / 2, 0, Math.max(0, viewport.width - width)),
    y: clamp(centerY - height / 2, 0, Math.max(0, viewport.height - height)),
    width,
    height,
  }
}

function smoothstep(u: number): number {
  const c = clamp(u, 0, 1)
  return c * c * (3 - 2 * c)
}

/**
 * Evaluate the interpolated camera frame at time `tMs` using cubic smoothstep
 * interpolation on `(x, y, width, height)`.
 *
 * Because every endpoint lies inside `[0, W] x [0, H]` with exact aspect ratio
 * `W / H`, every convex combination preserves both limits and aspect ratio.
 */
export function evaluateCameraAt(
  keyframes: readonly CameraKeyframe[],
  viewport: ViewportSize,
  tMs: number,
): CameraFrame {
  if (keyframes.length === 0)
    return fullCameraFrame(viewport)
  const first = keyframes[0]!
  if (tMs <= first.tMs || keyframes.length === 1)
    return first.frame
  const last = keyframes[keyframes.length - 1]!
  if (tMs >= last.tMs)
    return last.frame

  for (let i = 0; i < keyframes.length - 1; i++) {
    const a = keyframes[i]!
    const b = keyframes[i + 1]!
    if (tMs >= a.tMs && tMs <= b.tMs) {
      const span = b.tMs - a.tMs
      if (span <= 0)
        return b.frame
      const s = smoothstep((tMs - a.tMs) / span)
      return {
        x: a.frame.x + (b.frame.x - a.frame.x) * s,
        y: a.frame.y + (b.frame.y - a.frame.y) * s,
        width: a.frame.width + (b.frame.width - a.frame.width) * s,
        height: a.frame.height + (b.frame.height - a.frame.height) * s,
      }
    }
  }
  return last.frame
}

function buildPiecewiseScalarExpr(
  points: ReadonlyArray<{ frameIndex: number, value: number }>,
): string {
  if (points.length === 0)
    return '0'
  if (points.length === 1)
    return formatNum(points[0]!.value)

  // Collapse consecutive identical-time or no-op points before building the
  // nested if(...) chain so ffmpeg expressions stay compact.
  const normalized: Array<{ frameIndex: number, value: number }> = []
  for (const pt of points) {
    const prev = normalized[normalized.length - 1]
    if (prev && pt.frameIndex <= prev.frameIndex) {
      normalized[normalized.length - 1] = { frameIndex: prev.frameIndex, value: pt.value }
    }
    else {
      normalized.push(pt)
    }
  }

  const allEqual = normalized.every(pt => Math.abs(pt.value - normalized[0]!.value) < EPSILON)
  if (allEqual)
    return formatNum(normalized[0]!.value)

  let expr = formatNum(normalized[normalized.length - 1]!.value)
  for (let i = normalized.length - 2; i >= 0; i--) {
    const a = normalized[i]!
    const b = normalized[i + 1]!
    const span = b.frameIndex - a.frameIndex
    let segmentExpr: string
    if (span <= 0 || Math.abs(b.value - a.value) < EPSILON) {
      segmentExpr = formatNum(a.value)
    }
    else {
      const u = `((on-${a.frameIndex})/${span})`
      const delta = b.value - a.value
      segmentExpr = `(${formatNum(a.value)}+(${formatNum(delta)})*${u}*${u}*(3-2*${u}))`
    }
    expr = `if(lt(on,${b.frameIndex}),${segmentExpr},${expr})`
  }
  return expr
}

/**
 * Build ffmpeg `zoompan` expressions (`z`, `x`, `y`) indexed by 0-based output
 * frame counter `on`.
 */
export function buildZoompanExpressions(
  keyframes: readonly CameraKeyframe[],
  viewport: ViewportSize,
  scaleFactor: number,
  fps: number,
): ZoompanExpressions {
  const full = fullCameraFrame(viewport)
  const list = keyframes.length > 0
    ? keyframes
    : [
        { tMs: 0, frame: full },
      ]

  const toFrameIndex = (tMs: number) => Math.max(0, Math.round((tMs * fps) / 1000))
  const widthPoints = list.map(k => ({
    frameIndex: toFrameIndex(k.tMs),
    value: k.frame.width * scaleFactor,
  }))
  const xPoints = list.map(k => ({
    frameIndex: toFrameIndex(k.tMs),
    value: k.frame.x * scaleFactor,
  }))
  const yPoints = list.map(k => ({
    frameIndex: toFrameIndex(k.tMs),
    value: k.frame.y * scaleFactor,
  }))

  const sourceWidth = viewport.width * scaleFactor
  const widthExpr = buildPiecewiseScalarExpr(widthPoints)
  const z = widthExpr === formatNum(sourceWidth)
    ? '1'
    : `(${formatNum(sourceWidth)}/${widthExpr})`

  return {
    z,
    x: buildPiecewiseScalarExpr(xPoints),
    y: buildPiecewiseScalarExpr(yPoints),
  }
}

/**
 * Build an ffmpeg concat demuxer manifest mapping timestamped PNG frames onto a
 * continuous timeline of `totalMs` milliseconds.
 */
export function buildConcatManifest(
  frames: readonly CapturedFrame[],
  totalMs: number,
  minFrameDurationSec = 1 / 120,
): string {
  if (frames.length === 0)
    throw new Error('Cannot build concat manifest without captured frames')

  const escapePath = (filePath: string) => filePath.replace(/'/g, '\'\\\'\'')
  const lines: string[] = []
  for (let i = 0; i < frames.length; i++) {
    const current = frames[i]!
    const nextTMs = i + 1 < frames.length
      ? frames[i + 1]!.tMs
      : totalMs
    const durationSec = Math.max(minFrameDurationSec, (nextTMs - current.tMs) / 1000)
    lines.push(`file '${escapePath(current.path)}'`)
    lines.push(`duration ${durationSec.toFixed(6)}`)
  }
  // ffmpeg's concat demuxer requires a trailing file entry so the last frame's
  // duration directive takes effect. The encode command trims to exact totalMs
  // with `-t`.
  const last = frames[frames.length - 1]!
  lines.push(`file '${escapePath(last.path)}'`)
  return `${lines.join('\n')}\n`
}

/**
 * Build the ffmpeg filtergraph for 2x RGB pan/zoom plus an optional smooth
 * crossfade from the held final frame back to frame 0 for seamless looping.
 */
export function buildVideoFilterGraph(options: VideoFilterGraphOptions): {
  filterComplex: string
  outputMap: string
  totalOutputSec: number
} {
  const { fps, width, height, durationSec, loopCrossfadeSec, zoompan } = options
  const mainDuration = Math.max(0.1, durationSec)
  const crossfade = Math.max(0, loopCrossfadeSec)
  const zoompanFilter = `zoompan=z='${zoompan.z}':x='${zoompan.x}':y='${zoompan.y}':d=1:fps=${fps}:s=${width}x${height}`

  if (crossfade <= 0) {
    return {
      filterComplex: [
        `[0:v]fps=${fps}`,
        'format=gbrp',
        zoompanFilter,
        `trim=duration=${mainDuration.toFixed(3)}`,
        'setpts=PTS-STARTPTS',
        'scale=out_color_matrix=bt709:flags=lanczos',
        'format=yuv420p[v]',
      ].join(','),
      outputMap: '[v]',
      totalOutputSec: mainDuration,
    }
  }

  const fadeStr = crossfade.toFixed(3)
  const mainStr = mainDuration.toFixed(3)
  const filterComplex = [
    `[0:v]fps=${fps},format=gbrp,${zoompanFilter},trim=duration=${mainStr},setpts=PTS-STARTPTS,format=rgb24,tpad=stop_mode=clone:stop_duration=${fadeStr}[main]`,
    `[1:v]format=rgb24,scale=${width}:${height}:flags=lanczos,fps=${fps},trim=duration=${fadeStr},setpts=PTS-STARTPTS[first]`,
    `[main][first]xfade=transition=fade:duration=${fadeStr}:offset=${mainStr},scale=out_color_matrix=bt709:flags=lanczos,format=yuv420p[v]`,
  ].join(';')

  return {
    filterComplex,
    outputMap: '[v]',
    totalOutputSec: mainDuration + crossfade,
  }
}

async function installDemoCursor(page: Page): Promise<void> {
  await page.evaluate(() => {
    const existing = document.getElementById('__lm_demo_cursor_root')
    if (existing)
      existing.remove()

    const style = document.createElement('style')
    style.id = '__lm_demo_cursor_style'
    style.textContent = `
      #__lm_demo_cursor_root {
        position: fixed;
        inset: 0;
        pointer-events: none;
        z-index: 2147483647;
        overflow: hidden;
      }
      #__lm_demo_cursor {
        position: fixed;
        left: 0;
        top: 0;
        width: 22px;
        height: 22px;
        opacity: 0;
        transform: translate3d(-100px, -100px, 0);
        filter: drop-shadow(0 2px 4px rgba(0, 0, 0, 0.35));
        transition: opacity 120ms ease-out;
        will-change: transform;
      }
      .__lm_demo_ripple {
        position: fixed;
        width: 28px;
        height: 28px;
        margin-left: -14px;
        margin-top: -14px;
        border-radius: 9999px;
        border: 2px solid #2563eb;
        background: rgba(37, 99, 235, 0.16);
        transform: scale(0.35);
        opacity: 0.95;
        animation: __lm_demo_ripple_anim 340ms cubic-bezier(0.22, 1, 0.36, 1) forwards;
      }
      .__lm_demo_wheel {
        position: fixed;
        width: 22px;
        height: 22px;
        margin-left: -11px;
        margin-top: -11px;
        border-radius: 9999px;
        border: 2px solid #0ea5e9;
        background: rgba(14, 165, 233, 0.14);
        transform: scale(0.5);
        opacity: 0.85;
        animation: __lm_demo_ripple_anim 280ms ease-out forwards;
      }
      @keyframes __lm_demo_ripple_anim {
        to {
          transform: scale(1.45);
          opacity: 0;
        }
      }
    `
    document.head.appendChild(style)

    const root = document.createElement('div')
    root.id = '__lm_demo_cursor_root'

    const cursor = document.createElement('div')
    cursor.id = '__lm_demo_cursor'
    cursor.innerHTML = `<svg width="22" height="22" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
      <path d="M4.5 3.2L19.2 12.1L12.6 13.7L9.3 19.8L4.5 3.2Z" fill="#0f172a" stroke="#ffffff" stroke-width="1.8" stroke-linejoin="round"/>
    </svg>`
    root.appendChild(cursor)
    document.body.appendChild(root)

    let lastX = -100
    let lastY = -100
    const place = (x: number, y: number) => {
      lastX = x
      lastY = y
      cursor.style.opacity = '1'
      cursor.style.transform = `translate3d(${x - 3}px, ${y - 2}px, 0)`
    }
    const pulse = (cls: string, x: number, y: number) => {
      const el = document.createElement('div')
      el.className = cls
      el.style.left = `${x}px`
      el.style.top = `${y}px`
      root.appendChild(el)
      setTimeout(() => el.remove(), 400)
    }

    const onMove = (e: MouseEvent) => place(e.clientX, e.clientY)
    const onDown = (e: MouseEvent) => {
      place(e.clientX, e.clientY)
      pulse('__lm_demo_ripple', e.clientX, e.clientY)
    }
    const onWheel = (e: WheelEvent) => {
      const x = Number.isFinite(e.clientX) && e.clientX > 0
        ? e.clientX
        : lastX
      const y = Number.isFinite(e.clientY) && e.clientY > 0
        ? e.clientY
        : lastY
      if (x >= 0 && y >= 0)
        pulse('__lm_demo_wheel', x, y)
    }

    window.addEventListener('pointermove', onMove, { capture: true, passive: true })
    window.addEventListener('mousemove', onMove, { capture: true, passive: true })
    window.addEventListener('mousedown', onDown, { capture: true, passive: true })
    window.addEventListener('wheel', onWheel, { capture: true, passive: true })
  })
}

/**
 * Records a settled Playwright page as 2x Retina lossless PNG frames via CDP
 * `Page.startScreencast`, tracks smooth pan/zoom camera keyframes, and encodes
 * a visually lossless BT.709 H.264 MP4 with zero loading-screen prefix.
 */
export class SiteVideoRecorder {
  private readonly page: Page
  private readonly cdp: CDPSession
  private readonly workDir: string
  private readonly viewport: ViewportSize
  private readonly scaleFactor: number
  private readonly fps: number
  private readonly crf: number
  private readonly preset: string
  private readonly loopCrossfadeMs: number
  private readonly expectedWidth: number
  private readonly expectedHeight: number
  private readonly startPerfMs: number
  private readonly frames: CapturedFrame[] = []
  private readonly keyframes: CameraKeyframe[] = []
  private currentCamera: CameraFrame
  private writeQueue: Promise<void> = Promise.resolve()
  private stopped = false
  private pointerX: number
  private pointerY: number

  private constructor(
    page: Page,
    cdp: CDPSession,
    options: Required<Pick<RecorderOptions, 'workDir' | 'viewport' | 'scaleFactor' | 'fps' | 'crf' | 'preset' | 'loopCrossfadeMs'>>,
    startPerfMs: number,
  ) {
    this.page = page
    this.cdp = cdp
    this.workDir = options.workDir
    this.viewport = options.viewport
    this.scaleFactor = options.scaleFactor
    this.fps = options.fps
    this.crf = options.crf
    this.preset = options.preset
    this.loopCrossfadeMs = options.loopCrossfadeMs
    this.expectedWidth = Math.round(options.viewport.width * options.scaleFactor)
    this.expectedHeight = Math.round(options.viewport.height * options.scaleFactor)
    this.startPerfMs = startPerfMs
    this.currentCamera = fullCameraFrame(options.viewport)
    this.keyframes.push({ tMs: 0, frame: this.currentCamera })
    this.pointerX = options.viewport.width / 2
    this.pointerY = options.viewport.height / 2
  }

  /**
   * Begin recording AFTER the page has finished loading, hydrating, and settling
   * its theme so frame 0 is guaranteed to have no loading screen.
   */
  static async start(page: Page, options: RecorderOptions): Promise<SiteVideoRecorder> {
    const scaleFactor = options.scaleFactor ?? 2
    const fps = options.fps ?? 30
    const crf = options.crf ?? 17
    const preset = options.preset ?? 'slow'
    const loopCrossfadeMs = Math.max(0, options.loopCrossfadeMs ?? 1000)
    const showCursor = options.showCursor ?? true

    mkdirSync(options.workDir, { recursive: true })
    if (showCursor)
      await installDemoCursor(page)

    const cdp = await page.context().newCDPSession(page)
    // Capture frame 0 before starting the screencast so the timeline begins at
    // t = 0 with a crisp full-resolution PNG of the settled workspace.
    const initialPng = await page.screenshot({ type: 'png' })
    const startPerfMs = performance.now()

    const recorder = new SiteVideoRecorder(
      page,
      cdp,
      {
        workDir: options.workDir,
        viewport: options.viewport,
        scaleFactor,
        fps,
        crf,
        preset,
        loopCrossfadeMs,
      },
      startPerfMs,
    )
    recorder.recordPngSync(initialPng, 0)

    cdp.on('Page.screencastFrame', (event) => {
      if (recorder.stopped) {
        void cdp.send('Page.screencastFrameAck', { sessionId: event.sessionId }).catch(() => {})
        return
      }
      const arrivalMs = recorder.nowMs()
      const buf = Buffer.from(event.data, 'base64')
      recorder.writeQueue = recorder.writeQueue.then(async () => {
        if (!recorder.stopped)
          await recorder.recordPngAsync(buf, arrivalMs)
        await cdp.send('Page.screencastFrameAck', { sessionId: event.sessionId }).catch(() => {})
      })
    })

    await cdp.send('Page.startScreencast', {
      format: 'png',
      maxWidth: recorder.expectedWidth,
      maxHeight: recorder.expectedHeight,
      everyNthFrame: 1,
    })
    return recorder
  }

  /** Elapsed recording time in milliseconds since frame 0. */
  nowMs(): number {
    return Math.max(0, performance.now() - this.startPerfMs)
  }

  /** Wait while keeping recording active. */
  async wait(ms: number): Promise<void> {
    if (ms > 0)
      await this.page.waitForTimeout(ms)
  }

  /** Capture an explicit lossless PNG snapshot at the current timestamp. */
  async captureFrame(): Promise<void> {
    if (this.stopped)
      return
    const png = await this.page.screenshot({ type: 'png' })
    const tMs = this.nowMs()
    this.writeQueue = this.writeQueue.then(() => this.recordPngAsync(png, tMs))
    await this.writeQueue
  }

  /** Smoothly transition camera to focus on a CSS-coordinate rectangle. */
  async zoomToRect(target: Rect, options: ZoomOptions = {}): Promise<void> {
    const transitionMs = Math.max(0, options.transitionMs ?? 450)
    const nextFrame = computeTargetCameraFrame(this.viewport, target, options)
    this.pushCameraTransition(nextFrame, transitionMs)
    if (options.waitForTransition ?? true)
      await this.wait(transitionMs)
  }

  /** Smoothly transition camera to focus on a visible Playwright locator. */
  async zoomToLocator(locator: Locator, options: ZoomOptions = {}): Promise<void> {
    const box = await locator.boundingBox()
    if (!box || box.width <= 0 || box.height <= 0)
      return
    await this.zoomToRect(box, options)
  }

  /** Smoothly zoom back out to the full 1.0x viewport. */
  async zoomOut(options: Pick<ZoomOptions, 'transitionMs' | 'waitForTransition'> = {}): Promise<void> {
    const transitionMs = Math.max(0, options.transitionMs ?? 450)
    this.pushCameraTransition(fullCameraFrame(this.viewport), transitionMs)
    if (options.waitForTransition ?? true)
      await this.wait(transitionMs)
  }

  /** Move the pointer smoothly from its previous position to `(x, y)`. */
  async movePointer(x: number, y: number, steps = 12, durationMs = 200): Promise<void> {
    const startX = this.pointerX
    const startY = this.pointerY
    const count = Math.max(1, steps)
    const stepDelay = durationMs / count
    for (let i = 1; i <= count; i++) {
      const s = smoothstep(i / count)
      const nx = startX + (x - startX) * s
      const ny = startY + (y - startY) * s
      await this.page.mouse.move(nx, ny)
      this.pointerX = nx
      this.pointerY = ny
      if (stepDelay >= 8)
        await this.wait(Math.round(stepDelay))
    }
  }

  /** Glide pointer smoothly to the center of `locator` and hover it. */
  async hoverLocator(locator: Locator, durationMs = 200): Promise<void> {
    const box = await locator.boundingBox()
    if (!box || box.width <= 0 || box.height <= 0) {
      await locator.hover()
      return
    }
    await this.movePointer(box.x + box.width / 2, box.y + box.height / 2, 12, durationMs)
  }

  /** Glide pointer smoothly to the center of `locator`, pause briefly, and click. */
  async clickLocator(locator: Locator, durationMs = 200): Promise<void> {
    const box = await locator.boundingBox()
    if (!box || box.width <= 0 || box.height <= 0) {
      await locator.click()
      return
    }
    const cx = box.x + box.width / 2
    const cy = box.y + box.height / 2
    await this.movePointer(cx, cy, 12, durationMs)
    await this.wait(70)
    await this.page.mouse.click(cx, cy)
  }

  /** Glide pointer to `(fromX, fromY)` and drag smoothly to `(toX, toY)`. */
  async drag(fromX: number, fromY: number, toX: number, toY: number, durationMs = 360): Promise<void> {
    await this.movePointer(fromX, fromY, 10, 180)
    await this.wait(60)
    await this.page.mouse.down()
    await this.movePointer(toX, toY, 18, durationMs)
    await this.page.mouse.up()
  }

  /**
   * Stop recording, write the concat manifest, apply smooth pan/zoom in RGB
   * (`gbrp`), and encode the final BT.709 H.264 MP4.
   */
  async finish(outMp4Path: string): Promise<void> {
    if (this.stopped)
      throw new Error('SiteVideoRecorder.finish() was already called')

    const finalPng = await this.page.screenshot({ type: 'png' })
    const totalMs = Math.max(100, this.nowMs())
    this.stopped = true

    await this.cdp.send('Page.stopScreencast').catch(() => {})
    await this.writeQueue
    await this.recordPngAsync(finalPng, totalMs)
    await this.cdp.detach().catch(() => {})

    // Hold the final camera state until the end of the clip.
    this.keyframes.push({ tMs: totalMs, frame: this.currentCamera })

    const manifestPath = join(this.workDir, 'concat.txt')
    writeFileSync(manifestPath, buildConcatManifest(this.frames, totalMs), 'utf8')

    const zoompan = buildZoompanExpressions(
      this.keyframes,
      this.viewport,
      this.scaleFactor,
      this.fps,
    )

    const outWidth = this.expectedWidth
    const outHeight = this.expectedHeight
    const durationSec = totalMs / 1000
    const loopCrossfadeSec = this.loopCrossfadeMs / 1000
    const { filterComplex, outputMap, totalOutputSec } = buildVideoFilterGraph({
      fps: this.fps,
      width: outWidth,
      height: outHeight,
      durationSec,
      loopCrossfadeSec,
      zoompan,
    })

    const ffmpegArgs: string[] = [
      '-y',
      '-f',
      'concat',
      '-safe',
      '0',
      '-i',
      manifestPath,
    ]
    if (loopCrossfadeSec > 0) {
      const firstFramePath = this.frames[0]!.path
      ffmpegArgs.push(
        '-loop',
        '1',
        '-t',
        loopCrossfadeSec.toFixed(3),
        '-framerate',
        String(this.fps),
        '-i',
        firstFramePath,
      )
    }
    ffmpegArgs.push(
      '-filter_complex',
      filterComplex,
      '-map',
      outputMap,
      '-t',
      totalOutputSec.toFixed(3),
      '-c:v',
      'libx264',
      '-preset',
      this.preset,
      '-crf',
      String(this.crf),
      '-pix_fmt',
      'yuv420p',
      '-colorspace',
      'bt709',
      '-color_primaries',
      'bt709',
      '-color_trc',
      'bt709',
      '-movflags',
      '+faststart',
      '-an',
      outMp4Path,
    )

    mkdirSync(dirname(outMp4Path), { recursive: true })
    await execFileAsync('ffmpeg', ffmpegArgs)
  }

  private pushCameraTransition(nextFrame: CameraFrame, transitionMs: number): void {
    const t0 = this.nowMs()
    const last = this.keyframes[this.keyframes.length - 1]
    // Hold the current camera steady until t0 unless a keyframe already lands at or after t0.
    const startMs = last && last.tMs > t0
      ? last.tMs
      : t0
    this.keyframes.push({ tMs: startMs, frame: this.currentCamera })
    this.keyframes.push({ tMs: startMs + transitionMs, frame: nextFrame })
    this.currentCamera = nextFrame
  }

  private isExpectedPngDimensions(png: Buffer): boolean {
    if (png.length < 24)
      return false
    // PNG magic signature + IHDR width/height at offsets 16..23.
    const width = png.readUInt32BE(16)
    const height = png.readUInt32BE(20)
    return width === this.expectedWidth && height === this.expectedHeight
  }

  private nextMonotonicMs(rawMs: number): number {
    const prev = this.frames[this.frames.length - 1]
    if (!prev)
      return Math.max(0, rawMs)
    return Math.max(prev.tMs + 1, rawMs)
  }

  private recordPngSync(png: Buffer, rawMs: number): void {
    if (!this.isExpectedPngDimensions(png)) {
      throw new Error(
        `Unexpected frame dimensions: got ${png.length >= 24
          ? `${png.readUInt32BE(16)}x${png.readUInt32BE(20)}`
          : 'invalid PNG'}, want ${this.expectedWidth}x${this.expectedHeight}`,
      )
    }
    const index = this.frames.length
    const framePath = join(this.workDir, `frame_${String(index).padStart(5, '0')}.png`)
    writeFileSync(framePath, png)
    this.frames.push({
      path: framePath,
      tMs: this.nextMonotonicMs(rawMs),
    })
  }

  private async recordPngAsync(png: Buffer, rawMs: number): Promise<void> {
    if (!this.isExpectedPngDimensions(png))
      return
    const index = this.frames.length
    const framePath = join(this.workDir, `frame_${String(index).padStart(5, '0')}.png`)
    await writeFile(framePath, png)
    this.frames.push({
      path: framePath,
      tMs: this.nextMonotonicMs(rawMs),
    })
  }
}
