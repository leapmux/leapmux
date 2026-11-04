fn main() {
    // frame.proto imports leapmux/v1/build_info.proto, which stands alone so
    // this build compiles no other leapmux.v1 message.
    prost_build::compile_protos(
        &[
            "../../proto/leapmux/desktop/v1/frame.proto",
            "../../proto/leapmux/v1/build_info.proto",
        ],
        &["../../proto"],
    )
    .unwrap();
    tauri_build::build();
}
