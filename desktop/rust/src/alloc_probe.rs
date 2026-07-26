//! Test-only peak-allocation tracker.
//!
//! `#[global_allocator]` is declared in `main.rs` against `alloc_probe::PeakTracking`;
//! the per-test high-water-mark is read via `peak_alloc_of`. Lives in its own file so
//! the allocator plumbing stays out of the 4000-line `main.rs`.

#![cfg(test)]

use std::alloc::{GlobalAlloc, Layout, System};
use std::cell::Cell;

thread_local! {
    // Deliberately thread-local rather than a global counter: the test
    // harness runs tests in parallel threads, and a shared counter would
    // make one test's allocations visible to another. `const` init keeps
    // this free of a destructor, so the allocator can't re-enter TLS setup.
    static PEAK: Cell<usize> = const { Cell::new(0) };
}

fn record(size: usize) {
    // `try_with` (not `with`): the allocator stays live during TLS
    // teardown, after PEAK has been destroyed. Nothing to record then.
    let _ = PEAK.try_with(|peak| peak.set(peak.get().max(size)));
}

pub struct PeakTracking;

// SAFETY: every method forwards to `System` with the same arguments it was
// given; the only added work is recording the requested size.
unsafe impl GlobalAlloc for PeakTracking {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        record(layout.size());
        unsafe { System.alloc(layout) }
    }

    // `vec![0u8; n]` lands here, not in `alloc`, via the zeroing
    // specialization -- which is exactly the allocation under test.
    unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
        record(layout.size());
        unsafe { System.alloc_zeroed(layout) }
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        record(new_size);
        unsafe { System.realloc(ptr, layout, new_size) }
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        unsafe { System.dealloc(ptr, layout) }
    }
}

/// Runs `f` on the current thread, returning its value alongside the
/// largest single allocation it requested.
///
/// Only allocations on the calling thread are counted, so `f` must do the
/// work itself rather than hand it to another thread. `Runtime::block_on`
/// qualifies: it drives the future on the caller.
pub fn peak_alloc_of<T>(f: impl FnOnce() -> T) -> (T, usize) {
    PEAK.with(|peak| peak.set(0));
    let out = f();
    (out, PEAK.with(|peak| peak.get()))
}
