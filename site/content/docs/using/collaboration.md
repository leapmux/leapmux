---
title: "Device Sync & Presence"
description: "How LeapMux keeps a workspace layout in sync across your browsers and devices in near-real-time, and how the turn-end sound picks a single client."
type: docs
weight: 9
---

LeapMux keeps a workspace's **layout** in sync across every client where you have it open — your browser tabs, windows, and devices. Open the same workspace in two places and its tiling tree, tabs, splits, and floating windows stay identical in both, in near-real-time. This is layout sync, not content mirroring: what you type into an agent chat or terminal composer in one client does not echo into another's input area.

## What syncs

When a workspace is open in more than one of your clients, these stay in step within roughly a frame to a network round-trip:

- **The tiling tree** — splits, grids, and leaf tiles, including dragging a divider to resize.
- **Tabs** — agent, terminal, and file tabs: whether they exist, their order, which tile they live in, and which Worker hosts them.
- **Floating windows** — position, size, opacity, and the tiles inside them.
- **Workspace lifecycle** — creating, renaming, or deleting a workspace updates the sidebar everywhere.
- **Tab titles and file-tab paths** — these ride the Worker's end-to-end-encrypted channel rather than the layout channel, so the Hub never sees them.

For the layout primitives themselves, see [Tabs & Layout](/docs/using/tabs-and-layout/).

## Your own devices and tabs

The common case is one person, several clients. Open a workspace on your laptop and your phone, or in two browser tabs, and a split or tab you create in one shows up in the others. There is nothing to set up — no session to start, no toggle; sync is always on. Your edits apply optimistically, appearing instantly before the Hub confirms them.

If two of your clients change the same thing at the same instant, LeapMux resolves it automatically: last write wins, with no merge prompt, and every client converges on the same result. You will rarely notice it happen.

## Presence: who hears the turn-end sound

LeapMux tracks one presence fact per workspace: which client is **active** — the one that most recently received your keyboard, pointer, or scroll input. It exists for a single purpose: when an agent finishes a turn, only the active client plays the notification sound, so the same agent doesn't ding on every open tab and device at once. Your other clients viewing that workspace stay silent. (If no clear active client can be determined, LeapMux plays the sound rather than risk swallowing it.)

You don't choose the active client — it follows wherever you last interacted. To keep a particular client quiet regardless, set **Turn End Sound** to **None** in its **Preferences** dialog (see [Settings & Preferences](/docs/using/settings/)). The sound is also skipped for trivial single-exchange turns and rate-limited so it can't fire repeatedly.

> **Note:** All tabs and windows of one browser profile count as a **single** client, because they share one login session — so they never compete for the ding among themselves. A separate browser, another device, or a CLI token is a distinct client.

There are no other presence features — no avatars, "who's viewing" badges, remote cursors, or typing indicators.

## Who can see a workspace

You see — and sync — exactly the workspaces you own. Workspace access is strictly owner-only.

## Troubleshooting

| Symptom | Likely cause | What to do |
| --- | --- | --- |
| A change in one tab didn't appear in another | The tabs aren't on the same workspace, or one lost its connection | Confirm both show the same workspace; reload the one that's behind. |
| Every device dinged when an agent finished | Each was a separate client (different browser/device/CLI token), or no active client could be determined | Interact with the client you want active, or set **Turn End Sound** to **None** on the others. Tabs of one browser share a session and won't each ding. |

For broader diagnostics, see [Troubleshooting](/docs/reference/troubleshooting/).
