//! Generated self-contained canonical-CBOR codec from CSIL specification.
//!
//! CSIL is the CBOR Service Interface Language; this codec owns the payload
//! wire (a CBOR map keyed by the verbatim CSIL field name in canonical RFC
//! 8949 order) so the generated types need no serde derive. One
//! `encode_`/`decode_` pair is emitted per record type.
#![allow(dead_code, clippy::vec_init_then_push)]

use super::types::*;

/// A decode failure: the CBOR was malformed or did not match the expected shape.
#[derive(Debug, Clone, PartialEq)]
pub struct CsilCborError(pub String);

impl std::fmt::Display for CsilCborError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for CsilCborError {}

/// A minimal canonical-CBOR value tree: a closed set of variants the generated codec
/// builds and walks. A map is an ordered list of pairs, so the encoder controls the
/// wire order of a record's keys explicitly (laid down in canonical order).
#[derive(Debug, Clone, PartialEq)]
pub enum CsilCborValue {
    Uint(u64),
    Int(i64),
    Bool(bool),
    Float(f64),
    Null,
    Text(String),
    Bytes(Vec<u8>),
    Array(Vec<CsilCborValue>),
    Map(Vec<(CsilCborValue, CsilCborValue)>),
    Tag(u64, Box<CsilCborValue>),
}

fn cbor_int(x: i64) -> CsilCborValue {
    CsilCborValue::Int(x)
}
fn cbor_uint(x: u64) -> CsilCborValue {
    CsilCborValue::Uint(x)
}
fn cbor_float(x: f64) -> CsilCborValue {
    CsilCborValue::Float(x)
}
fn cbor_bool(x: bool) -> CsilCborValue {
    CsilCborValue::Bool(x)
}
fn cbor_text(x: &str) -> CsilCborValue {
    CsilCborValue::Text(x.to_string())
}
fn cbor_bytes(x: &[u8]) -> CsilCborValue {
    CsilCborValue::Bytes(x.to_vec())
}

/// Serialize a value tree to canonical CBOR bytes.
fn cbor_encode(v: &CsilCborValue) -> Vec<u8> {
    let mut out = Vec::new();
    cbor_enc(v, &mut out);
    out
}

fn cbor_head(major: u8, n: u64, out: &mut Vec<u8>) {
    let mt = major << 5;
    if n < 24 {
        out.push(mt | n as u8);
    } else if n < 0x100 {
        out.push(mt | 24);
        out.push(n as u8);
    } else if n < 0x10000 {
        out.push(mt | 25);
        out.extend_from_slice(&(n as u16).to_be_bytes());
    } else if n < 0x1_0000_0000 {
        out.push(mt | 26);
        out.extend_from_slice(&(n as u32).to_be_bytes());
    } else {
        out.push(mt | 27);
        out.extend_from_slice(&n.to_be_bytes());
    }
}

fn cbor_enc(v: &CsilCborValue, out: &mut Vec<u8>) {
    match v {
        CsilCborValue::Uint(x) => cbor_head(0, *x, out),
        // A non-negative `Int` rides major type 0 so it is byte-identical to a `Uint`
        // of the same magnitude; only a genuinely negative value uses major type 1.
        CsilCborValue::Int(x) => {
            if *x >= 0 {
                cbor_head(0, *x as u64, out);
            } else {
                cbor_head(1, (-(*x + 1)) as u64, out);
            }
        }
        CsilCborValue::Bool(x) => out.push(if *x { 0xf5 } else { 0xf4 }),
        CsilCborValue::Null => out.push(0xf6),
        CsilCborValue::Float(x) => {
            out.push(0xfb);
            out.extend_from_slice(&x.to_bits().to_be_bytes());
        }
        CsilCborValue::Text(s) => {
            let bytes = s.as_bytes();
            cbor_head(3, bytes.len() as u64, out);
            out.extend_from_slice(bytes);
        }
        CsilCborValue::Bytes(b) => {
            cbor_head(2, b.len() as u64, out);
            out.extend_from_slice(b);
        }
        CsilCborValue::Array(items) => {
            cbor_head(4, items.len() as u64, out);
            for item in items {
                cbor_enc(item, out);
            }
        }
        CsilCborValue::Map(entries) => {
            cbor_head(5, entries.len() as u64, out);
            for (k, val) in entries {
                cbor_enc(k, out);
                cbor_enc(val, out);
            }
        }
        CsilCborValue::Tag(num, inner) => {
            cbor_head(6, *num, out);
            cbor_enc(inner, out);
        }
    }
}

/// Parse a full CBOR item and reject trailing bytes, so a payload that is not
/// exactly one value is an error rather than a silently-truncated read.
fn cbor_decode(b: &[u8]) -> Result<CsilCborValue, CsilCborError> {
    let mut pos = 0usize;
    let v = cbor_dec(b, &mut pos, 0)?;
    if pos != b.len() {
        return Err(CsilCborError(format!(
            "csil cbor: {} trailing bytes",
            b.len() - pos
        )));
    }
    Ok(v)
}

fn cbor_read_arg(b: &[u8], pos: &mut usize, low: u8) -> Result<u64, CsilCborError> {
    if low < 24 {
        *pos += 1;
        return Ok(low as u64);
    }
    let width = match low {
        24 => 1usize,
        25 => 2,
        26 => 4,
        27 => 8,
        _ => {
            return Err(CsilCborError(format!(
                "csil cbor: reserved additional info {low}"
            )))
        }
    };
    if *pos >= b.len() || width > b.len() - *pos - 1 {
        return Err(CsilCborError("csil cbor: truncated argument".to_string()));
    }
    let mut v = 0u64;
    for &byte in &b[*pos + 1..*pos + 1 + width] {
        v = (v << 8) | byte as u64;
    }
    *pos += 1 + width;
    Ok(v)
}

/// The most elements a decoded array or map reserves before it reads them. The
/// declared length is checked against the remaining input, but one input byte can
/// become a much larger value, so reserving the full declared length lets a small
/// frame reserve a large multiple of its size at every nesting level. Past this
/// bound, the vector grows only as elements are actually read.
const CSIL_CBOR_PREALLOC_LIMIT: usize = 1024;

fn cbor_dec(b: &[u8], pos: &mut usize, depth: usize) -> Result<CsilCborValue, CsilCborError> {
    if depth > 64 {
        return Err(CsilCborError(
            "csil cbor: nesting limit exceeded".to_string(),
        ));
    }
    if *pos >= b.len() {
        return Err(CsilCborError(
            "csil cbor: unexpected end of input".to_string(),
        ));
    }
    let ib = b[*pos];
    let major = ib >> 5;
    let low = ib & 0x1f;
    if major == 7 {
        return match low {
            20 => {
                *pos += 1;
                Ok(CsilCborValue::Bool(false))
            }
            21 => {
                *pos += 1;
                Ok(CsilCborValue::Bool(true))
            }
            22 | 23 => {
                *pos += 1;
                Ok(CsilCborValue::Null)
            }
            26 => {
                let bits = cbor_read_arg(b, pos, low)?;
                Ok(CsilCborValue::Float(f32::from_bits(bits as u32) as f64))
            }
            27 => {
                let bits = cbor_read_arg(b, pos, low)?;
                Ok(CsilCborValue::Float(f64::from_bits(bits)))
            }
            _ => Err(CsilCborError(format!(
                "csil cbor: unsupported simple value {low}"
            ))),
        };
    }
    let arg = cbor_read_arg(b, pos, low)?;
    match major {
        0 => Ok(CsilCborValue::Uint(arg)),
        1 => {
            if arg > i64::MAX as u64 {
                return Err(CsilCborError(
                    "csil cbor: negative integer out of range".to_string(),
                ));
            }
            Ok(CsilCborValue::Int(-1 - arg as i64))
        }
        2 => {
            if arg > (b.len() - *pos) as u64 {
                return Err(CsilCborError(
                    "csil cbor: truncated byte string".to_string(),
                ));
            }
            let n = arg as usize;
            let slice = b[*pos..*pos + n].to_vec();
            *pos += n;
            Ok(CsilCborValue::Bytes(slice))
        }
        3 => {
            if arg > (b.len() - *pos) as u64 {
                return Err(CsilCborError(
                    "csil cbor: truncated text string".to_string(),
                ));
            }
            let n = arg as usize;
            let s = std::str::from_utf8(&b[*pos..*pos + n])
                .map_err(|e| CsilCborError(format!("csil cbor: invalid utf-8: {e}")))?
                .to_string();
            *pos += n;
            Ok(CsilCborValue::Text(s))
        }
        4 => {
            if arg > (b.len() - *pos) as u64 {
                return Err(CsilCborError(
                    "csil cbor: array length exceeds remaining input".to_string(),
                ));
            }
            let n = arg as usize;
            let mut items = Vec::with_capacity(n.min(CSIL_CBOR_PREALLOC_LIMIT));
            for _ in 0..n {
                items.push(cbor_dec(b, pos, depth + 1)?);
            }
            Ok(CsilCborValue::Array(items))
        }
        5 => {
            if arg > (b.len() - *pos) as u64 {
                return Err(CsilCborError(
                    "csil cbor: map length exceeds remaining input".to_string(),
                ));
            }
            let n = arg as usize;
            let mut entries = Vec::with_capacity(n.min(CSIL_CBOR_PREALLOC_LIMIT));
            for _ in 0..n {
                let k = cbor_dec(b, pos, depth + 1)?;
                let val = cbor_dec(b, pos, depth + 1)?;
                entries.push((k, val));
            }
            Ok(CsilCborValue::Map(entries))
        }
        6 => {
            let inner = cbor_dec(b, pos, depth + 1)?;
            Ok(CsilCborValue::Tag(arg, Box::new(inner)))
        }
        _ => Err(CsilCborError(format!(
            "csil cbor: unexpected major type {major}"
        ))),
    }
}

/// Map a typed slice to a CBOR array via the per-element encoder.
fn cbor_enc_array<E>(xs: &[E], f: impl Fn(&E) -> CsilCborValue) -> CsilCborValue {
    CsilCborValue::Array(xs.iter().map(f).collect())
}

/// Map a typed map to a CBOR map. Rust `HashMap` iteration is unordered, so the inner
/// map's entry order is not canonicalized; the record's own keys (laid down at
/// generation time) are what the cross-language wire contract pins.
fn cbor_enc_map<K, V>(
    m: &std::collections::HashMap<K, V>,
    kf: impl Fn(&K) -> CsilCborValue,
    vf: impl Fn(&V) -> CsilCborValue,
) -> CsilCborValue {
    CsilCborValue::Map(m.iter().map(|(k, v)| (kf(k), vf(v))).collect())
}

fn cbor_dec_array<E>(
    v: &CsilCborValue,
    f: impl Fn(&CsilCborValue) -> Result<E, CsilCborError>,
) -> Result<Vec<E>, CsilCborError> {
    cbor_as_array(v)?.iter().map(f).collect()
}

fn cbor_dec_map<K: std::cmp::Eq + std::hash::Hash, V>(
    v: &CsilCborValue,
    kf: impl Fn(&CsilCborValue) -> Result<K, CsilCborError>,
    vf: impl Fn(&CsilCborValue) -> Result<V, CsilCborError>,
) -> Result<std::collections::HashMap<K, V>, CsilCborError> {
    let entries = cbor_as_map(v)?;
    let mut out = std::collections::HashMap::with_capacity(entries.len());
    for (k, val) in entries {
        out.insert(kf(k)?, vf(val)?);
    }
    Ok(out)
}

fn cbor_map_get<'a>(v: &'a CsilCborValue, key: &str) -> Option<&'a CsilCborValue> {
    if let CsilCborValue::Map(entries) = v {
        for (k, val) in entries {
            if matches!(k, CsilCborValue::Text(name) if name == key) {
                return Some(val);
            }
        }
    }
    None
}

fn cbor_expect_value(v: &CsilCborValue, expected: &CsilCborValue) -> Result<(), CsilCborError> {
    if v == expected {
        Ok(())
    } else {
        Err(CsilCborError(format!(
            "csil cbor: expected literal {expected:?}, got {v:?}"
        )))
    }
}

fn cbor_require<'a>(v: &'a CsilCborValue, key: &str) -> Result<&'a CsilCborValue, CsilCborError> {
    cbor_map_get(v, key).ok_or_else(|| CsilCborError(format!("csil cbor: missing field {key:?}")))
}

fn cbor_as_i64(v: &CsilCborValue) -> Result<i64, CsilCborError> {
    match v {
        CsilCborValue::Uint(x) => i64::try_from(*x)
            .map_err(|_| CsilCborError("csil cbor: integer overflows i64".to_string())),
        CsilCborValue::Int(x) => Ok(*x),
        _ => Err(CsilCborError("csil cbor: expected integer".to_string())),
    }
}

fn cbor_as_u64(v: &CsilCborValue) -> Result<u64, CsilCborError> {
    match v {
        CsilCborValue::Uint(x) => Ok(*x),
        CsilCborValue::Int(x) if *x >= 0 => Ok(*x as u64),
        CsilCborValue::Int(_) => Err(CsilCborError(
            "csil cbor: negative integer where unsigned expected".to_string(),
        )),
        _ => Err(CsilCborError(
            "csil cbor: expected unsigned integer".to_string(),
        )),
    }
}

fn cbor_as_f64(v: &CsilCborValue) -> Result<f64, CsilCborError> {
    match v {
        CsilCborValue::Float(x) => Ok(*x),
        CsilCborValue::Uint(x) => Ok(*x as f64),
        CsilCborValue::Int(x) => Ok(*x as f64),
        _ => Err(CsilCborError("csil cbor: expected float".to_string())),
    }
}

fn cbor_as_bool(v: &CsilCborValue) -> Result<bool, CsilCborError> {
    match v {
        CsilCborValue::Bool(b) => Ok(*b),
        _ => Err(CsilCborError("csil cbor: expected bool".to_string())),
    }
}

fn cbor_as_text(v: &CsilCborValue) -> Result<String, CsilCborError> {
    match v {
        CsilCborValue::Text(s) => Ok(s.clone()),
        _ => Err(CsilCborError("csil cbor: expected text".to_string())),
    }
}

fn cbor_as_bytes(v: &CsilCborValue) -> Result<Vec<u8>, CsilCborError> {
    match v {
        CsilCborValue::Bytes(b) => Ok(b.clone()),
        _ => Err(CsilCborError("csil cbor: expected byte string".to_string())),
    }
}

fn cbor_as_array(v: &CsilCborValue) -> Result<&[CsilCborValue], CsilCborError> {
    match v {
        CsilCborValue::Array(a) => Ok(a),
        _ => Err(CsilCborError("csil cbor: expected array".to_string())),
    }
}

fn cbor_as_map(v: &CsilCborValue) -> Result<&[(CsilCborValue, CsilCborValue)], CsilCborError> {
    match v {
        CsilCborValue::Map(m) => Ok(m),
        _ => Err(CsilCborError("csil cbor: expected map".to_string())),
    }
}

/// Build the canonical CBOR value tree for a Target.
fn csil_enc_target(csil_v: &Target) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(6);
    csil_entries.push((cbor_text("name"), cbor_text(&csil_v.name)));
    csil_entries.push((cbor_text("container"), cbor_text(&csil_v.container)));
    csil_entries.push((cbor_text("namespace"), cbor_text(&csil_v.namespace)));
    csil_entries.push((cbor_text("deployment"), cbor_text(&csil_v.deployment)));
    if let Some(csil_inner) = &csil_v.health_path {
        csil_entries.push((cbor_text("health_path"), cbor_text(csil_inner)));
    }
    if let Some(csil_inner) = &csil_v.health_port {
        csil_entries.push((cbor_text("health_port"), cbor_uint(*csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a Target from a decoded CBOR value tree.
fn csil_dec_target(csil_root: &CsilCborValue) -> Result<Target, CsilCborError> {
    let name = {
        let csil_field = cbor_require(csil_root, "name")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let namespace = {
        let csil_field = cbor_require(csil_root, "namespace")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let deployment = {
        let csil_field = cbor_require(csil_root, "deployment")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let container = {
        let csil_field = cbor_require(csil_root, "container")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let health_path = match cbor_map_get(csil_root, "health_path") {
        Some(csil_field) => {
            let csil_decode = cbor_as_text;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let health_port = match cbor_map_get(csil_root, "health_port") {
        Some(csil_field) => {
            let csil_decode = cbor_as_u64;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(Target {
        name,
        namespace,
        deployment,
        container,
        health_path,
        health_port,
    })
}

/// Encode a Target to canonical CSIL CBOR bytes.
pub fn encode_target(csil_v: &Target) -> Vec<u8> {
    cbor_encode(&csil_enc_target(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a Target.
pub fn decode_target(csil_data: &[u8]) -> Result<Target, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_target(&csil_root)
}

/// Build the canonical CBOR value tree for a TargetImage.
fn csil_enc_target_image(csil_v: &TargetImage) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("image"), cbor_text(&csil_v.image)));
    csil_entries.push((cbor_text("target"), cbor_text(&csil_v.target)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a TargetImage from a decoded CBOR value tree.
fn csil_dec_target_image(csil_root: &CsilCborValue) -> Result<TargetImage, CsilCborError> {
    let target = {
        let csil_field = cbor_require(csil_root, "target")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let image = {
        let csil_field = cbor_require(csil_root, "image")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(TargetImage { target, image })
}

/// Encode a TargetImage to canonical CSIL CBOR bytes.
pub fn encode_target_image(csil_v: &TargetImage) -> Vec<u8> {
    cbor_encode(&csil_enc_target_image(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a TargetImage.
pub fn decode_target_image(csil_data: &[u8]) -> Result<TargetImage, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_target_image(&csil_root)
}

/// Build the canonical CBOR value tree for a ReleaseItem.
fn csil_enc_release_item(csil_v: &ReleaseItem) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(5);
    csil_entries.push((cbor_text("image"), cbor_text(&csil_v.image)));
    csil_entries.push((cbor_text("status"), cbor_text(&csil_v.status)));
    csil_entries.push((cbor_text("target"), cbor_text(&csil_v.target)));
    if let Some(csil_inner) = &csil_v.message {
        csil_entries.push((cbor_text("message"), cbor_text(csil_inner)));
    }
    csil_entries.push((
        cbor_text("previous_image"),
        cbor_text(&csil_v.previous_image),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ReleaseItem from a decoded CBOR value tree.
fn csil_dec_release_item(csil_root: &CsilCborValue) -> Result<ReleaseItem, CsilCborError> {
    let target = {
        let csil_field = cbor_require(csil_root, "target")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let image = {
        let csil_field = cbor_require(csil_root, "image")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let previous_image = {
        let csil_field = cbor_require(csil_root, "previous_image")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let status = {
        let csil_field = cbor_require(csil_root, "status")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let message = match cbor_map_get(csil_root, "message") {
        Some(csil_field) => {
            let csil_decode = cbor_as_text;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(ReleaseItem {
        target,
        image,
        previous_image,
        status,
        message,
    })
}

/// Encode a ReleaseItem to canonical CSIL CBOR bytes.
pub fn encode_release_item(csil_v: &ReleaseItem) -> Vec<u8> {
    cbor_encode(&csil_enc_release_item(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ReleaseItem.
pub fn decode_release_item(csil_data: &[u8]) -> Result<ReleaseItem, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_release_item(&csil_root)
}

/// Build the canonical CBOR value tree for a Release.
fn csil_enc_release(csil_v: &Release) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(7);
    csil_entries.push((cbor_text("id"), cbor_text(&csil_v.id)));
    csil_entries.push((
        cbor_text("items"),
        cbor_enc_array(&csil_v.items, csil_enc_release_item),
    ));
    csil_entries.push((cbor_text("owner"), cbor_text(&csil_v.owner)));
    if let Some(csil_inner) = &csil_v.reason {
        csil_entries.push((cbor_text("reason"), cbor_text(csil_inner)));
    }
    csil_entries.push((cbor_text("status"), cbor_text(&csil_v.status)));
    csil_entries.push((cbor_text("created_ms"), cbor_uint(csil_v.created_ms)));
    csil_entries.push((cbor_text("updated_ms"), cbor_uint(csil_v.updated_ms)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a Release from a decoded CBOR value tree.
fn csil_dec_release(csil_root: &CsilCborValue) -> Result<Release, CsilCborError> {
    let id = {
        let csil_field = cbor_require(csil_root, "id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let owner = {
        let csil_field = cbor_require(csil_root, "owner")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let status = {
        let csil_field = cbor_require(csil_root, "status")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let reason = match cbor_map_get(csil_root, "reason") {
        Some(csil_field) => {
            let csil_decode = cbor_as_text;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let items = {
        let csil_field = cbor_require(csil_root, "items")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_release_item);
        csil_decode(csil_field)?
    };
    let created_ms = {
        let csil_field = cbor_require(csil_root, "created_ms")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let updated_ms = {
        let csil_field = cbor_require(csil_root, "updated_ms")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    Ok(Release {
        id,
        owner,
        status,
        reason,
        items,
        created_ms,
        updated_ms,
    })
}

/// Encode a Release to canonical CSIL CBOR bytes.
pub fn encode_release(csil_v: &Release) -> Vec<u8> {
    cbor_encode(&csil_enc_release(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a Release.
pub fn decode_release(csil_data: &[u8]) -> Result<Release, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_release(&csil_root)
}

/// Build the canonical CBOR value tree for a CreateReleaseRequest.
fn csil_enc_create_release_request(csil_v: &CreateReleaseRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((
        cbor_text("images"),
        cbor_enc_array(&csil_v.images, csil_enc_target_image),
    ));
    if let Some(csil_inner) = &csil_v.reason {
        csil_entries.push((cbor_text("reason"), cbor_text(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CreateReleaseRequest from a decoded CBOR value tree.
fn csil_dec_create_release_request(
    csil_root: &CsilCborValue,
) -> Result<CreateReleaseRequest, CsilCborError> {
    let images = {
        let csil_field = cbor_require(csil_root, "images")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_target_image);
        csil_decode(csil_field)?
    };
    let reason = match cbor_map_get(csil_root, "reason") {
        Some(csil_field) => {
            let csil_decode = cbor_as_text;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(CreateReleaseRequest { images, reason })
}

/// Encode a CreateReleaseRequest to canonical CSIL CBOR bytes.
pub fn encode_create_release_request(csil_v: &CreateReleaseRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_create_release_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CreateReleaseRequest.
pub fn decode_create_release_request(
    csil_data: &[u8],
) -> Result<CreateReleaseRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_create_release_request(&csil_root)
}

/// Build the canonical CBOR value tree for a GetReleaseRequest.
fn csil_enc_get_release_request(csil_v: &GetReleaseRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    csil_entries.push((cbor_text("id"), cbor_text(&csil_v.id)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetReleaseRequest from a decoded CBOR value tree.
fn csil_dec_get_release_request(
    csil_root: &CsilCborValue,
) -> Result<GetReleaseRequest, CsilCborError> {
    let id = {
        let csil_field = cbor_require(csil_root, "id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(GetReleaseRequest { id })
}

/// Encode a GetReleaseRequest to canonical CSIL CBOR bytes.
pub fn encode_get_release_request(csil_v: &GetReleaseRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_get_release_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetReleaseRequest.
pub fn decode_get_release_request(csil_data: &[u8]) -> Result<GetReleaseRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_release_request(&csil_root)
}

/// Build the canonical CBOR value tree for a ListReleasesRequest.
fn csil_enc_list_releases_request(csil_v: &ListReleasesRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    if let Some(csil_inner) = &csil_v.limit {
        csil_entries.push((cbor_text("limit"), cbor_uint(*csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ListReleasesRequest from a decoded CBOR value tree.
fn csil_dec_list_releases_request(
    csil_root: &CsilCborValue,
) -> Result<ListReleasesRequest, CsilCborError> {
    let limit = match cbor_map_get(csil_root, "limit") {
        Some(csil_field) => {
            let csil_decode = cbor_as_u64;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(ListReleasesRequest { limit })
}

/// Encode a ListReleasesRequest to canonical CSIL CBOR bytes.
pub fn encode_list_releases_request(csil_v: &ListReleasesRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_list_releases_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ListReleasesRequest.
pub fn decode_list_releases_request(
    csil_data: &[u8],
) -> Result<ListReleasesRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_list_releases_request(&csil_root)
}

/// Build the canonical CBOR value tree for a ListReleasesResponse.
fn csil_enc_list_releases_response(csil_v: &ListReleasesResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    csil_entries.push((
        cbor_text("releases"),
        cbor_enc_array(&csil_v.releases, csil_enc_release),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ListReleasesResponse from a decoded CBOR value tree.
fn csil_dec_list_releases_response(
    csil_root: &CsilCborValue,
) -> Result<ListReleasesResponse, CsilCborError> {
    let releases = {
        let csil_field = cbor_require(csil_root, "releases")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_release);
        csil_decode(csil_field)?
    };
    Ok(ListReleasesResponse { releases })
}

/// Encode a ListReleasesResponse to canonical CSIL CBOR bytes.
pub fn encode_list_releases_response(csil_v: &ListReleasesResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_list_releases_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ListReleasesResponse.
pub fn decode_list_releases_response(
    csil_data: &[u8],
) -> Result<ListReleasesResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_list_releases_response(&csil_root)
}

/// Build the canonical CBOR value tree for a RollbackRequest.
fn csil_enc_rollback_request(csil_v: &RollbackRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    csil_entries.push((cbor_text("id"), cbor_text(&csil_v.id)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a RollbackRequest from a decoded CBOR value tree.
fn csil_dec_rollback_request(csil_root: &CsilCborValue) -> Result<RollbackRequest, CsilCborError> {
    let id = {
        let csil_field = cbor_require(csil_root, "id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(RollbackRequest { id })
}

/// Encode a RollbackRequest to canonical CSIL CBOR bytes.
pub fn encode_rollback_request(csil_v: &RollbackRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_rollback_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a RollbackRequest.
pub fn decode_rollback_request(csil_data: &[u8]) -> Result<RollbackRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_rollback_request(&csil_root)
}

/// Build the canonical CBOR value tree for a ListTargetsRequest.
fn csil_enc_list_targets_request(csil_v: &ListTargetsRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    if let Some(csil_inner) = &csil_v.name {
        csil_entries.push((cbor_text("name"), cbor_text(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ListTargetsRequest from a decoded CBOR value tree.
fn csil_dec_list_targets_request(
    csil_root: &CsilCborValue,
) -> Result<ListTargetsRequest, CsilCborError> {
    let name = match cbor_map_get(csil_root, "name") {
        Some(csil_field) => {
            let csil_decode = cbor_as_text;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(ListTargetsRequest { name })
}

/// Encode a ListTargetsRequest to canonical CSIL CBOR bytes.
pub fn encode_list_targets_request(csil_v: &ListTargetsRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_list_targets_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ListTargetsRequest.
pub fn decode_list_targets_request(csil_data: &[u8]) -> Result<ListTargetsRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_list_targets_request(&csil_root)
}

/// Build the canonical CBOR value tree for a ListTargetsResponse.
fn csil_enc_list_targets_response(csil_v: &ListTargetsResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    csil_entries.push((
        cbor_text("targets"),
        cbor_enc_array(&csil_v.targets, csil_enc_target),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ListTargetsResponse from a decoded CBOR value tree.
fn csil_dec_list_targets_response(
    csil_root: &CsilCborValue,
) -> Result<ListTargetsResponse, CsilCborError> {
    let targets = {
        let csil_field = cbor_require(csil_root, "targets")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_target);
        csil_decode(csil_field)?
    };
    Ok(ListTargetsResponse { targets })
}

/// Encode a ListTargetsResponse to canonical CSIL CBOR bytes.
pub fn encode_list_targets_response(csil_v: &ListTargetsResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_list_targets_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ListTargetsResponse.
pub fn decode_list_targets_response(
    csil_data: &[u8],
) -> Result<ListTargetsResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_list_targets_response(&csil_root)
}
