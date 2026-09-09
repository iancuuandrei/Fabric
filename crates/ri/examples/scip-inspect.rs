//! Inspect producer metadata and encoding declarations without admitting an index.
fn main() -> Result<(), Box<dyn std::error::Error>> {
    let path = std::env::args_os()
        .nth(1)
        .ok_or("expected SCIP file path")?;
    let bytes = std::fs::read(path)?;
    let index = engorch_ri::scip::decode(&bytes)?;
    let metadata = index.metadata.ok_or("missing metadata")?;
    let tool = metadata.tool_info.ok_or("missing tool info")?;
    let documents: Vec<_> = index
        .documents
        .iter()
        .map(|document| {
            serde_json::json!({
                "path":document.relative_path,"position_encoding":document.position_encoding,
                "occurrences":document.occurrences.len(),"symbols":document.symbols.len(),
            })
        })
        .collect();
    println!(
        "{}",
        serde_json::json!({"tool":tool.name,"version":tool.version,
        "project_root":metadata.project_root,"text_encoding":metadata.text_document_encoding,
        "documents":documents,"external_symbols":index.external_symbols.len()})
    );
    Ok(())
}
