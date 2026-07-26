//! Frame read/write utilities for the desktop sidecar wire format.
//!
//! The sync and async halves below are two transports over ONE wire format: the
//! sync half drives stdio (and, on Windows, the named pipe via
//! `sidecar_ipc::windows`'s `SyncPipeReader`/`SyncPipeWriter`), the async half
//! drives tokio streams. Every decision that is part of the FORMAT -- the
//! encoding, the size cap, the decode error mapping, the varint state machine --
//! lives in the shared helpers here and is called by both, so the twins can only
//! ever differ in their I/O loop. See the `#[cfg(any(windows, test))]` gates on
//! the async half: `test` is what makes them compile and run off Windows, so
//! drift fails CI in front of its author instead of on the one OS nobody built.
//!
//! Extracted from `main.rs` in <https://github.com/leapmux/leapmux/issues/282>.
//! This module depends only on `crate::proto` and the two consts below.

use std::io::{self, Read, Write};

use prost::Message;

use crate::proto;

// Must stay in sync with maxFrameSize in desktop/go/frame.go: it must exceed the
// 16 MiB org-events read limit plus its Frame/Event envelope so a full-size
// OrgMaterialized bootstrap is not rejected on read.
const MAX_FRAME_SIZE: u64 = 20 * 1024 * 1024; // 20 MiB
                                              // A base-128 varint carries 7 bits per byte, so a u64 length prefix needs at
                                              // most 10 bytes. A reader that has consumed this many without seeing a
                                              // terminating byte is being fed a malformed (or malicious) prefix.
const MAX_VARINT_BYTES: usize = 10;

/// The `GetSidecarInfo` request method, the handshake every connect path uses
/// to confirm the peer is the sidecar it claims to be (matching protocol version
/// and binary hash). Shared by every caller -- the handshake read/write loops
/// (sync and async) wrap it in a `Frame{Request{id, method: Some(...)}}`, and
/// the post-handshake `send_request(_async)` callers pass it directly. Centralizing
/// the shape keeps the wire contract grep-able from one site.
pub(crate) fn get_sidecar_info_request() -> proto::request::Method {
    proto::request::Method::GetSidecarInfo(proto::GetSidecarInfoRequest {})
}

/// Encodes `frame` into a length-delimited buffer ready to hand to a writer.
fn encode_frame(frame: &proto::Frame) -> io::Result<Vec<u8>> {
    let mut buf = Vec::with_capacity(frame.encoded_len() + MAX_VARINT_BYTES);
    frame.encode_length_delimited(&mut buf).map_err(|err| {
        io::Error::new(io::ErrorKind::InvalidData, format!("encode frame: {err}"))
    })?;
    Ok(buf)
}

/// Checks a decoded length prefix against `MAX_FRAME_SIZE` and narrows it to a
/// payload length.
///
/// Callers MUST call this BEFORE allocating the payload buffer: rejecting the
/// size is what stops a peer from making us allocate gigabytes off a bogus
/// varint, and a check that runs after the allocation protects nothing. The cap
/// is also what makes the `as usize` narrowing lossless on every target we
/// build for.
fn frame_len(size: u64) -> io::Result<usize> {
    if size > MAX_FRAME_SIZE {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("frame too large: {size} bytes (max {MAX_FRAME_SIZE})"),
        ));
    }
    Ok(size as usize)
}

/// Decodes a frame body -- the bytes AFTER the length prefix, exactly
/// `frame_len` of them.
fn decode_frame(data: &[u8]) -> io::Result<proto::Frame> {
    proto::Frame::decode(data)
        .map_err(|err| io::Error::new(io::ErrorKind::InvalidData, format!("decode frame: {err}")))
}

/// Folds one wire byte into an in-progress varint decode.
///
/// Returns `Some(value)` when `b` terminates the varint (high bit clear), and
/// `None` when more bytes are needed -- so a reader keeps only its own read
/// loop and shares the state machine.
fn varint_step(x: &mut u64, s: &mut u32, b: u8) -> Option<u64> {
    if b < 0x80 {
        return Some(*x | (b as u64) << *s);
    }
    *x |= ((b & 0x7f) as u64) << *s;
    *s += 7;
    None
}

/// The error a reader returns once a varint has run past `MAX_VARINT_BYTES`
/// without terminating.
fn varint_overflow() -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, "varint overflow")
}

pub(crate) fn write_frame(w: &mut impl Write, frame: &proto::Frame) -> io::Result<()> {
    w.write_all(&encode_frame(frame)?)?;
    w.flush()
}

// Note: prost's `decode_length_delimited` requires an in-memory `Buf`, not
// an `io::Read` stream. For streaming stdio reads we must manually decode the
// varint length prefix, then `read_exact` the payload before decoding.
pub(crate) fn read_frame(r: &mut impl Read) -> io::Result<proto::Frame> {
    let len = frame_len(read_varint(r)?)?;
    let mut data = vec![0u8; len];
    r.read_exact(&mut data)?;
    decode_frame(&data)
}

fn read_varint(r: &mut impl Read) -> io::Result<u64> {
    let mut x: u64 = 0;
    let mut s: u32 = 0;
    let mut buf = [0u8; 1];
    for _ in 0..MAX_VARINT_BYTES {
        r.read_exact(&mut buf)?;
        if let Some(v) = varint_step(&mut x, &mut s, buf[0]) {
            return Ok(v);
        }
    }
    Err(varint_overflow())
}

#[cfg(any(windows, test))]
use tokio::io::{AsyncReadExt, AsyncWriteExt};

#[cfg(any(windows, test))]
pub(crate) async fn write_frame_async<W: tokio::io::AsyncWrite + Unpin>(
    w: &mut W,
    frame: &proto::Frame,
) -> io::Result<()> {
    w.write_all(&encode_frame(frame)?).await?;
    w.flush().await
}

#[cfg(any(windows, test))]
pub(crate) async fn read_frame_async<R: tokio::io::AsyncRead + Unpin>(
    r: &mut R,
) -> io::Result<proto::Frame> {
    // frame_len rejects an oversize prefix before the vec! below allocates.
    let len = frame_len(read_varint_async(r).await?)?;
    let mut data = vec![0u8; len];
    r.read_exact(&mut data).await?;
    decode_frame(&data)
}

#[cfg(any(windows, test))]
async fn read_varint_async<R: tokio::io::AsyncRead + Unpin>(r: &mut R) -> io::Result<u64> {
    let mut x: u64 = 0;
    let mut s: u32 = 0;
    let mut buf = [0u8; 1];
    for _ in 0..MAX_VARINT_BYTES {
        r.read_exact(&mut buf).await?;
        if let Some(v) = varint_step(&mut x, &mut s, buf[0]) {
            return Ok(v);
        }
    }
    Err(varint_overflow())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::alloc_probe;
    use crate::proto;
    use std::io;

    // The async tests need a single-worker multi-thread runtime to drive
    // `block_on`; a private one here keeps `frame.rs` from depending on the
    // sidecar-IPC layer's `pipe_runtime`. Built once per test that needs it.
    fn test_runtime() -> tokio::runtime::Runtime {
        tokio::runtime::Builder::new_multi_thread()
            .worker_threads(1)
            .enable_io()
            .enable_time()
            .thread_name("leapmux-frame-test")
            .build()
            .expect("build frame-test runtime")
    }

    // The handshake's identity check hinges on the GetSidecarInfo exchange; pin
    // that the shared request builder returns the right variant so a future
    // proto rename fails here instead of silently sending the wrong request.
    #[test]
    fn get_sidecar_info_request_returns_the_get_sidecar_info_variant() {
        assert!(matches!(
            get_sidecar_info_request(),
            proto::request::Method::GetSidecarInfo(_)
        ));
    }

    // A frame whose encoded body exceeds 127 bytes forces the length-delimited
    // prefix into a multi-byte varint, exercising the loop in read_varint_async.
    #[cfg(any(windows, test))]
    #[test]
    fn read_frame_async_roundtrips_multibyte_varint_frame() {
        test_runtime().block_on(async {
            let (mut writer, mut reader) = tokio::io::duplex(64 * 1024);
            // A Response carrying a SidecarInfo whose binary_hash is 200 bytes
            // pushes the length prefix past one byte AND exercises the
            // handshake-response shape (the realistic payload this codec
            // round-trips in production), not just an arbitrary long string.
            let info = proto::SidecarInfo {
                protocol_version: "1".to_string(),
                binary_hash: "x".repeat(200),
                ..Default::default()
            };
            let frame = proto::Frame {
                message: Some(proto::frame::Message::Response(proto::Response {
                    id: 7,
                    result: Some(proto::response::Result::SidecarInfo(info.clone())),
                    ..Default::default()
                })),
            };
            assert!(
                frame.encoded_len() > 127,
                "test precondition: frame must exceed 1-byte varint range, got {}",
                frame.encoded_len()
            );

            write_frame_async(&mut writer, &frame).await.expect("write");
            drop(writer);
            let received = read_frame_async(&mut reader).await.expect("read");
            assert_eq!(received.encoded_len(), frame.encoded_len());
            match received.message {
                Some(proto::frame::Message::Response(r)) => {
                    assert_eq!(r.id, 7);
                    // The SidecarInfo payload survives the encode/decode round
                    // trip intact -- pinning the handshake-response shape, not
                    // just the varint framing.
                    let got = r
                        .result
                        .as_ref()
                        .and_then(|res| match res {
                            proto::response::Result::SidecarInfo(si) => Some(si),
                            _ => None,
                        })
                        .expect("SidecarInfo result survived the round trip");
                    assert_eq!(got.binary_hash, info.binary_hash);
                    assert_eq!(got.protocol_version, info.protocol_version);
                }
                other => panic!("unexpected message: {other:?}"),
            }
        });
    }

    // frame_len and varint_step are the two decisions the sync and async readers
    // SHARE. The reader tests above reach them only through a socket, and only at
    // sizes a real frame happens to take -- so the boundary itself (exactly at the
    // cap vs. one byte over) is asserted here, where it can be stated exactly.
    #[test]
    fn frame_len_admits_the_cap_and_refuses_one_byte_past_it() {
        assert_eq!(frame_len(0).expect("an empty frame is a frame"), 0);
        // Exactly at the cap is legal: the check is `>`, not `>=`, and a frame of
        // precisely MAX_FRAME_SIZE must still be readable.
        assert_eq!(
            frame_len(MAX_FRAME_SIZE).expect("a frame at the cap is legal"),
            MAX_FRAME_SIZE as usize
        );

        let err = frame_len(MAX_FRAME_SIZE + 1).expect_err("one byte past the cap is refused");
        assert_eq!(err.kind(), io::ErrorKind::InvalidData);
        assert!(
            err.to_string().contains("frame too large"),
            "operators and the sync reader's tests key on this text: {err}"
        );

        // A bogus varint is the case the cap exists for: u64::MAX must be refused as
        // a size rather than narrowed by `as usize` into a plausible allocation.
        frame_len(u64::MAX).expect_err("a bogus length prefix is refused, not truncated");
    }

    #[test]
    fn varint_step_terminates_on_a_clear_high_bit_and_accumulates_otherwise() {
        // Single byte, high bit clear: terminates immediately with its own value.
        let (mut x, mut s) = (0u64, 0u32);
        assert_eq!(varint_step(&mut x, &mut s, 0x01), Some(1));

        // Two bytes: the first only accumulates (returns None and advances the
        // shift), the second terminates and contributes its bits at that shift.
        // 0xAC 0x02 is protobuf's canonical varint for 300.
        let (mut x, mut s) = (0u64, 0u32);
        assert_eq!(
            varint_step(&mut x, &mut s, 0xAC),
            None,
            "high bit set: more bytes needed"
        );
        assert_eq!(s, 7, "each continuation byte carries 7 payload bits");
        assert_eq!(varint_step(&mut x, &mut s, 0x02), Some(300));
    }

    // A length prefix exceeding MAX_FRAME_SIZE must be rejected without
    // attempting to allocate the payload, so a peer can't make us allocate
    // gigabytes by sending a bogus varint.
    //
    // The error assertions below are necessary but NOT sufficient: the same
    // error surfaces even if the check runs after the `vec!`. So measure the
    // allocation too -- that, and only that, pins the ordering that gives the
    // check its value. `test_runtime()` is resolved outside the probe so
    // one-time runtime construction isn't attributed to the read.
    #[cfg(any(windows, test))]
    #[test]
    fn read_frame_async_rejects_oversize_varint_before_allocating() {
        let runtime = test_runtime();
        let (_, peak) = alloc_probe::peak_alloc_of(|| {
            runtime.block_on(async {
                let (mut writer, mut reader) = tokio::io::duplex(64);
                let mut buf = Vec::new();
                let mut v: u64 = MAX_FRAME_SIZE + 1;
                loop {
                    let byte = (v & 0x7f) as u8;
                    v >>= 7;
                    if v == 0 {
                        buf.push(byte);
                        break;
                    }
                    buf.push(byte | 0x80);
                }
                tokio::io::AsyncWriteExt::write_all(&mut writer, &buf)
                    .await
                    .expect("write varint");
                drop(writer);

                let err = read_frame_async(&mut reader)
                    .await
                    .expect_err("oversize frame must error");
                assert_eq!(err.kind(), io::ErrorKind::InvalidData);
                assert!(
                    err.to_string().contains("frame too large"),
                    "unexpected error message: {err}"
                );
            })
        });
        assert!(
            (peak as u64) < MAX_FRAME_SIZE,
            "read_frame_async allocated {peak} bytes for a rejected {} byte prefix; \
             the MAX_FRAME_SIZE check must run BEFORE the payload is allocated",
            MAX_FRAME_SIZE + 1
        );
    }

    // The reader thread distinguishes UnexpectedEof from real errors so a clean
    // peer-close doesn't log a noisy error line. Pin that contract here.
    #[cfg(any(windows, test))]
    #[test]
    fn read_frame_async_returns_eof_when_peer_closes() {
        test_runtime().block_on(async {
            let (writer, mut reader) = tokio::io::duplex(64);
            drop(writer);
            let err = read_frame_async(&mut reader)
                .await
                .expect_err("eof must be reported");
            assert_eq!(err.kind(), io::ErrorKind::UnexpectedEof);
        });
    }
}
