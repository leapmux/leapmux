fn main() {
    prost_build::compile_protos(
        &["../../proto/leapmux/desktop/v1/frame.proto"],
        &["../../proto"],
    )
    .unwrap();
    tauri_build::build();
}
