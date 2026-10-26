use opal_knowledge_wire::*;
use std::io::{self, Read};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mode = std::env::args().nth(1).ok_or("missing mode")?;
    let mut text = String::new();
    io::stdin().read_to_string(&mut text)?;
    let text = text.trim();
    if text.len() % 2 != 0 { return Err("odd hex length".into()); }
    let bytes: Result<Vec<u8>, _> = (0..text.len()).step_by(2)
        .map(|i| u8::from_str_radix(&text[i..i+2], 16)).collect();
    let bytes = bytes?;
    if mode == "capture" {
        let result = opal_capture_wire::decode_capture_batch(&bytes)
            .map(|x| opal_capture_wire::encode_capture_batch(&x))
            .map_err(|e| format!("capture codec: {e:?}"))?;
        for b in result { print!("{b:02x}"); }
        println!();
        return Ok(());
    }
    let result = match mode.as_str() {
        "history" => decode_history_page(&bytes).map(|x| encode_history_page(&x)),
        "query" => decode_knowledge_query(&bytes).map(|x| encode_knowledge_query(&x)),
        "result" => decode_query_result(&bytes).map(|x| encode_query_result(&x)),
        "error" => decode_service_error(&bytes).map(|x| encode_service_error(&x)),
        _ => return Err("unknown mode".into()),
    }.map_err(|e| format!("codec: {e:?}"))?;
    for b in result { print!("{b:02x}"); }
    println!();
    Ok(())
}
