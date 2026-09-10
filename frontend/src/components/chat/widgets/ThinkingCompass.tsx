import type { Component } from 'solid-js'
import * as styles from './ThinkingIndicator.css'

export const ThinkingCompass: Component<{ angleDeg: number }> = props => (
  <svg class={styles.compass} viewBox="0 0 401.294 401.294">
    <g transform={`translate(100.666,-852.275) rotate(${props.angleDeg},100,1052.922)`}>
      <g transform="matrix(0.41544,-0.17208,0.17208,0.41544,-122.740,632.706)">
        <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m100,852.362-30,170 30,30 0-200z" />
        <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m99.962,852.362 30,170-30,30 0-200z" />
        <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m99.962,1253.482 30-170-30-30 0,200z" />
        <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m100,1253.482-30-170 30-30 0,200z" />
        <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m300.541,1052.941-170-30-30,30 200,0z" />
        <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m300.541,1052.904-170,30-30-30 200,0z" />
        <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m-100.579,1052.904 170,30 30-30-200,0z" />
        <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m-100.579,1052.941 170-30 30,30-200,0z" />
      </g>
      <g transform="matrix(0.17208,-0.41544,0.41544,0.17208,-354.645,913.272)">
        <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m100,852.362-30,170 30,30 0-200z" />
        <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m99.962,852.362 30,170-30,30 0-200z" />
        <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m99.962,1253.482 30-170-30-30 0,200z" />
        <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m100,1253.482-30-170 30-30 0,200z" />
        <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m300.541,1052.941-170-30-30,30 200,0z" />
        <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m300.541,1052.904-170,30-30-30 200,0z" />
        <path fill="currentColor" stroke="currentColor" stroke-width="2.224" d="m-100.579,1052.904 170,30 30-30-200,0z" />
        <path fill="var(--background)" stroke="currentColor" stroke-width="2.224" d="m-100.579,1052.941 170-30 30,30-200,0z" />
      </g>
      <path fill="currentColor" stroke="currentColor" stroke-width="1" transform="translate(0,852.362)" d="M100,37.15A162.85,162.85 0 0 0-62.85,200 162.85,162.85 0 0 0 100,362.85 162.85,162.85 0 0 0 262.85,200 162.85,162.85 0 0 0 100,37.15zM100,65.5A134.5,134.5 0 0 1 234.5,200 134.5,134.5 0 0 1 100,334.5 134.5,134.5 0 0 1-34.5,200 134.5,134.5 0 0 1 100,65.5z" />
      <g>
        <path fill="currentColor" stroke="currentColor" d="m185.055,967.864-84.828,59.38 0,25.448 84.828-84.828z" />
        <path fill="var(--background)" stroke="currentColor" d="m185.039,967.848-59.38,84.828-25.448,0 84.828-84.828z" />
        <path fill="currentColor" stroke="currentColor" d="m14.907,1137.98 84.829-59.38 0-25.448-84.829,84.828z" />
        <path fill="var(--background)" stroke="currentColor" d="m14.923,1137.996 59.38-84.828 25.448,0-84.828,84.828z" />
        <path fill="currentColor" stroke="currentColor" d="m185.039,1137.996-59.38-84.828-25.448,0 84.828,84.828z" />
        <path fill="var(--background)" stroke="currentColor" d="m185.055,1137.98-84.828-59.38 0-25.448 84.828,84.828z" />
        <path fill="currentColor" stroke="currentColor" d="m14.923,967.848 59.38,84.828 25.448,0-84.828-84.828z" />
        <path fill="var(--background)" stroke="currentColor" d="m14.907,967.864 84.829,59.38 0,25.448-84.829-84.828z" />
      </g>
      <g>
        <path fill="currentColor" stroke="currentColor" d="m100,852.362-30,170 30,30 0-200z" />
        <path fill="var(--background)" stroke="currentColor" d="m99.962,852.362 30,170-30,30 0-200z" />
        <path fill="currentColor" stroke="currentColor" d="m99.962,1253.482 30-170-30-30 0,200z" />
        <path fill="var(--background)" stroke="currentColor" d="m100,1253.482-30-170 30-30 0,200z" />
        <path fill="currentColor" stroke="currentColor" d="m300.541,1052.941-170-30-30,30 200,0z" />
        <path fill="var(--background)" stroke="currentColor" d="m300.541,1052.904-170,30-30-30 200,0z" />
        <path fill="currentColor" stroke="currentColor" d="m-100.579,1052.904 170,30 30-30-200,0z" />
        <path fill="var(--background)" stroke="currentColor" d="m-100.579,1052.941 170-30 30,30-200,0z" />
      </g>
    </g>
  </svg>
)
