// The extension leaves the interpreter's symbols to be resolved at load time. maturin sets the link
// arguments itself; this makes a plain `cargo build` / `cargo test` of the workspace link on macOS too
// (CI sets RUSTFLAGS, which a .cargo/config.toml cannot add to).
fn main() {
    pyo3_build_config::add_extension_module_link_args();
}
