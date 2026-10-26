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

/// Build the canonical CBOR value tree for a IdentityRef.
fn csil_enc_identity_ref(csil_v: &IdentityRef) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("domain"), cbor_text(&csil_v.domain)));
    csil_entries.push((cbor_text("account_id"), cbor_text(&csil_v.account_id)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a IdentityRef from a decoded CBOR value tree.
fn csil_dec_identity_ref(csil_root: &CsilCborValue) -> Result<IdentityRef, CsilCborError> {
    let domain = {
        let csil_field = cbor_require(csil_root, "domain")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let account_id = {
        let csil_field = cbor_require(csil_root, "account_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(IdentityRef { domain, account_id })
}

/// Encode a IdentityRef to canonical CSIL CBOR bytes.
pub fn encode_identity_ref(csil_v: &IdentityRef) -> Vec<u8> {
    cbor_encode(&csil_enc_identity_ref(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a IdentityRef.
pub fn decode_identity_ref(csil_data: &[u8]) -> Result<IdentityRef, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_identity_ref(&csil_root)
}

/// Build the canonical CBOR value tree for a SecretBinding.
fn csil_enc_secret_binding(csil_v: &SecretBinding) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(6);
    csil_entries.push((cbor_text("key"), cbor_text(&csil_v.key)));
    csil_entries.push((cbor_text("path"), cbor_text(&csil_v.path)));
    csil_entries.push((
        cbor_text("authority"),
        csil_enc_identity_ref(&csil_v.authority),
    ));
    csil_entries.push((cbor_text("binding_id"), cbor_text(&csil_v.binding_id)));
    csil_entries.push((cbor_text("session_id"), cbor_text(&csil_v.session_id)));
    if let Some(csil_inner) = &csil_v.issuance_id {
        csil_entries.push((cbor_text("issuance_id"), cbor_text(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a SecretBinding from a decoded CBOR value tree.
fn csil_dec_secret_binding(csil_root: &CsilCborValue) -> Result<SecretBinding, CsilCborError> {
    let authority = {
        let csil_field = cbor_require(csil_root, "authority")?;
        let csil_decode = csil_dec_identity_ref;
        csil_decode(csil_field)?
    };
    let binding_id = {
        let csil_field = cbor_require(csil_root, "binding_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let path = {
        let csil_field = cbor_require(csil_root, "path")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let key = {
        let csil_field = cbor_require(csil_root, "key")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let session_id = {
        let csil_field = cbor_require(csil_root, "session_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let issuance_id = match cbor_map_get(csil_root, "issuance_id") {
        Some(csil_field) => {
            let csil_decode = cbor_as_text;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(SecretBinding {
        authority,
        binding_id,
        path,
        key,
        session_id,
        issuance_id,
    })
}

/// Encode a SecretBinding to canonical CSIL CBOR bytes.
pub fn encode_secret_binding(csil_v: &SecretBinding) -> Vec<u8> {
    cbor_encode(&csil_enc_secret_binding(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a SecretBinding.
pub fn decode_secret_binding(csil_data: &[u8]) -> Result<SecretBinding, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_secret_binding(&csil_root)
}

/// Build the canonical CBOR value tree for a SecretCandidate.
fn csil_enc_secret_candidate(csil_v: &SecretCandidate) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((
        cbor_text("binding"),
        csil_enc_secret_binding(&csil_v.binding),
    ));
    csil_entries.push((cbor_text("encoding"), csil_enc_encoding(&csil_v.encoding)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a SecretCandidate from a decoded CBOR value tree.
fn csil_dec_secret_candidate(csil_root: &CsilCborValue) -> Result<SecretCandidate, CsilCborError> {
    let binding = {
        let csil_field = cbor_require(csil_root, "binding")?;
        let csil_decode = csil_dec_secret_binding;
        csil_decode(csil_field)?
    };
    let encoding = {
        let csil_field = cbor_require(csil_root, "encoding")?;
        let csil_decode = csil_dec_encoding;
        csil_decode(csil_field)?
    };
    Ok(SecretCandidate { binding, encoding })
}

/// Encode a SecretCandidate to canonical CSIL CBOR bytes.
pub fn encode_secret_candidate(csil_v: &SecretCandidate) -> Vec<u8> {
    cbor_encode(&csil_enc_secret_candidate(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a SecretCandidate.
pub fn decode_secret_candidate(csil_data: &[u8]) -> Result<SecretCandidate, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_secret_candidate(&csil_root)
}

/// Build the canonical CBOR value tree for a LiteralSegment.
fn csil_enc_literal_segment(csil_v: &LiteralSegment) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("start"), cbor_uint(csil_v.start)));
    csil_entries.push((cbor_text("value"), cbor_bytes(&csil_v.value)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a LiteralSegment from a decoded CBOR value tree.
fn csil_dec_literal_segment(csil_root: &CsilCborValue) -> Result<LiteralSegment, CsilCborError> {
    let start = {
        let csil_field = cbor_require(csil_root, "start")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let value = {
        let csil_field = cbor_require(csil_root, "value")?;
        let csil_decode = cbor_as_bytes;
        csil_decode(csil_field)?
    };
    Ok(LiteralSegment { start, value })
}

/// Encode a LiteralSegment to canonical CSIL CBOR bytes.
pub fn encode_literal_segment(csil_v: &LiteralSegment) -> Vec<u8> {
    cbor_encode(&csil_enc_literal_segment(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a LiteralSegment.
pub fn decode_literal_segment(csil_data: &[u8]) -> Result<LiteralSegment, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_literal_segment(&csil_root)
}

/// Build the canonical CBOR value tree for a SecretSegment.
fn csil_enc_secret_segment(csil_v: &SecretSegment) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("end"), cbor_uint(csil_v.end)));
    csil_entries.push((cbor_text("start"), cbor_uint(csil_v.start)));
    csil_entries.push((
        cbor_text("candidate"),
        csil_enc_secret_candidate(&csil_v.candidate),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a SecretSegment from a decoded CBOR value tree.
fn csil_dec_secret_segment(csil_root: &CsilCborValue) -> Result<SecretSegment, CsilCborError> {
    let start = {
        let csil_field = cbor_require(csil_root, "start")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let end = {
        let csil_field = cbor_require(csil_root, "end")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let candidate = {
        let csil_field = cbor_require(csil_root, "candidate")?;
        let csil_decode = csil_dec_secret_candidate;
        csil_decode(csil_field)?
    };
    Ok(SecretSegment {
        start,
        end,
        candidate,
    })
}

/// Encode a SecretSegment to canonical CSIL CBOR bytes.
pub fn encode_secret_segment(csil_v: &SecretSegment) -> Vec<u8> {
    cbor_encode(&csil_enc_secret_segment(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a SecretSegment.
pub fn decode_secret_segment(csil_data: &[u8]) -> Result<SecretSegment, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_secret_segment(&csil_root)
}

/// Build the canonical CBOR value tree for a AmbiguousSegment.
fn csil_enc_ambiguous_segment(csil_v: &AmbiguousSegment) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(4);
    csil_entries.push((cbor_text("end"), cbor_uint(csil_v.end)));
    csil_entries.push((cbor_text("start"), cbor_uint(csil_v.start)));
    csil_entries.push((cbor_text("reason"), cbor_text(&csil_v.reason)));
    csil_entries.push((
        cbor_text("candidates"),
        cbor_enc_array(&csil_v.candidates, csil_enc_secret_candidate),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a AmbiguousSegment from a decoded CBOR value tree.
fn csil_dec_ambiguous_segment(
    csil_root: &CsilCborValue,
) -> Result<AmbiguousSegment, CsilCborError> {
    let start = {
        let csil_field = cbor_require(csil_root, "start")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let end = {
        let csil_field = cbor_require(csil_root, "end")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let candidates = {
        let csil_field = cbor_require(csil_root, "candidates")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_secret_candidate);
        csil_decode(csil_field)?
    };
    let reason = {
        let csil_field = cbor_require(csil_root, "reason")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(AmbiguousSegment {
        start,
        end,
        candidates,
        reason,
    })
}

/// Encode a AmbiguousSegment to canonical CSIL CBOR bytes.
pub fn encode_ambiguous_segment(csil_v: &AmbiguousSegment) -> Vec<u8> {
    cbor_encode(&csil_enc_ambiguous_segment(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a AmbiguousSegment.
pub fn decode_ambiguous_segment(csil_data: &[u8]) -> Result<AmbiguousSegment, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_ambiguous_segment(&csil_root)
}

/// Build the canonical CBOR value tree for a GapSegment.
fn csil_enc_gap_segment(csil_v: &GapSegment) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("end"), cbor_uint(csil_v.end)));
    csil_entries.push((cbor_text("start"), cbor_uint(csil_v.start)));
    csil_entries.push((cbor_text("reason"), cbor_text(&csil_v.reason)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GapSegment from a decoded CBOR value tree.
fn csil_dec_gap_segment(csil_root: &CsilCborValue) -> Result<GapSegment, CsilCborError> {
    let start = {
        let csil_field = cbor_require(csil_root, "start")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let end = {
        let csil_field = cbor_require(csil_root, "end")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let reason = {
        let csil_field = cbor_require(csil_root, "reason")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(GapSegment { start, end, reason })
}

/// Encode a GapSegment to canonical CSIL CBOR bytes.
pub fn encode_gap_segment(csil_v: &GapSegment) -> Vec<u8> {
    cbor_encode(&csil_enc_gap_segment(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GapSegment.
pub fn decode_gap_segment(csil_data: &[u8]) -> Result<GapSegment, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_gap_segment(&csil_root)
}

/// Build the canonical CBOR value tree for a CaptureBatch.
fn csil_enc_capture_batch(csil_v: &CaptureBatch) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(7);
    csil_entries.push((cbor_text("final"), cbor_bool(csil_v.r#final)));
    csil_entries.push((cbor_text("complete"), cbor_bool(csil_v.complete)));
    csil_entries.push((
        cbor_text("segments"),
        cbor_enc_array(&csil_v.segments, csil_enc_segment),
    ));
    csil_entries.push((cbor_text("sequence"), cbor_uint(csil_v.sequence)));
    csil_entries.push((cbor_text("stream_id"), cbor_text(&csil_v.stream_id)));
    csil_entries.push((cbor_text("attempt_id"), cbor_text(&csil_v.attempt_id)));
    csil_entries.push((
        cbor_text("schema_version"),
        cbor_uint(csil_v.schema_version),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CaptureBatch from a decoded CBOR value tree.
fn csil_dec_capture_batch(csil_root: &CsilCborValue) -> Result<CaptureBatch, CsilCborError> {
    let schema_version = {
        let csil_field = cbor_require(csil_root, "schema_version")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let attempt_id = {
        let csil_field = cbor_require(csil_root, "attempt_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let stream_id = {
        let csil_field = cbor_require(csil_root, "stream_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let sequence = {
        let csil_field = cbor_require(csil_root, "sequence")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let segments = {
        let csil_field = cbor_require(csil_root, "segments")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_segment);
        csil_decode(csil_field)?
    };
    let r#final = {
        let csil_field = cbor_require(csil_root, "final")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    let complete = {
        let csil_field = cbor_require(csil_root, "complete")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    Ok(CaptureBatch {
        schema_version,
        attempt_id,
        stream_id,
        sequence,
        segments,
        r#final,
        complete,
    })
}

/// Encode a CaptureBatch to canonical CSIL CBOR bytes.
pub fn encode_capture_batch(csil_v: &CaptureBatch) -> Vec<u8> {
    cbor_encode(&csil_enc_capture_batch(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CaptureBatch.
pub fn decode_capture_batch(csil_data: &[u8]) -> Result<CaptureBatch, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_capture_batch(&csil_root)
}

/// Build the canonical CBOR value tree for a CaptureReceipt.
fn csil_enc_capture_receipt(csil_v: &CaptureReceipt) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(4);
    csil_entries.push((cbor_text("digest"), cbor_bytes(&csil_v.digest)));
    csil_entries.push((cbor_text("sequence"), cbor_uint(csil_v.sequence)));
    csil_entries.push((cbor_text("stream_id"), cbor_text(&csil_v.stream_id)));
    csil_entries.push((cbor_text("attempt_id"), cbor_text(&csil_v.attempt_id)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CaptureReceipt from a decoded CBOR value tree.
fn csil_dec_capture_receipt(csil_root: &CsilCborValue) -> Result<CaptureReceipt, CsilCborError> {
    let attempt_id = {
        let csil_field = cbor_require(csil_root, "attempt_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let stream_id = {
        let csil_field = cbor_require(csil_root, "stream_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let sequence = {
        let csil_field = cbor_require(csil_root, "sequence")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let digest = {
        let csil_field = cbor_require(csil_root, "digest")?;
        let csil_decode = cbor_as_bytes;
        csil_decode(csil_field)?
    };
    Ok(CaptureReceipt {
        attempt_id,
        stream_id,
        sequence,
        digest,
    })
}

/// Encode a CaptureReceipt to canonical CSIL CBOR bytes.
pub fn encode_capture_receipt(csil_v: &CaptureReceipt) -> Vec<u8> {
    cbor_encode(&csil_enc_capture_receipt(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CaptureReceipt.
pub fn decode_capture_receipt(csil_data: &[u8]) -> Result<CaptureReceipt, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_capture_receipt(&csil_root)
}

/// Encode a Encoding enum as its bare literal value.
fn csil_enc_encoding(csil_v: &Encoding) -> CsilCborValue {
    match csil_v {
        Encoding::Raw => cbor_text("raw"),
        Encoding::Base64 => cbor_text("base64"),
        Encoding::JsonString => cbor_text("json_string"),
        Encoding::UrlQuery => cbor_text("url_query"),
    }
}

/// Decode a bare literal value into a Encoding enum.
fn csil_dec_encoding(csil_v: &CsilCborValue) -> Result<Encoding, CsilCborError> {
    let csil_val = cbor_as_text(csil_v)?;
    match csil_val.as_str() {
        "raw" => Ok(Encoding::Raw),
        "base64" => Ok(Encoding::Base64),
        "json_string" => Ok(Encoding::JsonString),
        "url_query" => Ok(Encoding::UrlQuery),
        csil_other => Err(CsilCborError(format!(
            "csil cbor: unknown Encoding value {csil_other:?}"
        ))),
    }
}

/// Encode a Segment union as a tagged sum `[variant_index, value]`.
fn csil_enc_segment(csil_v: &Segment) -> CsilCborValue {
    match csil_v {
        Segment::Variant0(csil_x) => CsilCborValue::Array(vec![
            CsilCborValue::Uint(0),
            csil_enc_literal_segment(csil_x),
        ]),
        Segment::Variant1(csil_x) => CsilCborValue::Array(vec![
            CsilCborValue::Uint(1),
            csil_enc_secret_segment(csil_x),
        ]),
        Segment::Variant2(csil_x) => CsilCborValue::Array(vec![
            CsilCborValue::Uint(2),
            csil_enc_ambiguous_segment(csil_x),
        ]),
        Segment::Variant3(csil_x) => {
            CsilCborValue::Array(vec![CsilCborValue::Uint(3), csil_enc_gap_segment(csil_x)])
        }
    }
}

/// Decode a tagged sum `[variant_index, value]` into a Segment union.
fn csil_dec_segment(csil_v: &CsilCborValue) -> Result<Segment, CsilCborError> {
    let csil_arr = match csil_v {
        CsilCborValue::Array(csil_a) => csil_a,
        _ => {
            return Err(CsilCborError(
                "csil cbor: union expects a 2-element array".to_string(),
            ))
        }
    };
    if csil_arr.len() != 2 {
        return Err(CsilCborError(format!(
            "csil cbor: union array has {} elements, expected 2",
            csil_arr.len()
        )));
    }
    let csil_idx = cbor_as_u64(&csil_arr[0])?;
    match csil_idx {
        0 => {
            let csil_decode = csil_dec_literal_segment;
            Ok(Segment::Variant0(csil_decode(&csil_arr[1])?))
        }
        1 => {
            let csil_decode = csil_dec_secret_segment;
            Ok(Segment::Variant1(csil_decode(&csil_arr[1])?))
        }
        2 => {
            let csil_decode = csil_dec_ambiguous_segment;
            Ok(Segment::Variant2(csil_decode(&csil_arr[1])?))
        }
        3 => {
            let csil_decode = csil_dec_gap_segment;
            Ok(Segment::Variant3(csil_decode(&csil_arr[1])?))
        }
        csil_other => Err(CsilCborError(format!(
            "csil cbor: unknown Segment variant {csil_other}"
        ))),
    }
}
