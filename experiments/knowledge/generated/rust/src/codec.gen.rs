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

/// Encode a UTC instant as CBOR tag 0 RFC3339 text in UTC, per the wire contract;
/// sub-second precision is preserved when present and the `Z` offset is forced.
fn csil_enc_timestamp(t: &chrono::DateTime<chrono::Utc>) -> CsilCborValue {
    let text = t.to_rfc3339_opts(chrono::SecondsFormat::AutoSi, true);
    CsilCborValue::Tag(0, Box::new(CsilCborValue::Text(text)))
}

/// Decode a CBOR tag 0 RFC3339 timestamp back to a UTC instant.
fn csil_as_timestamp(v: &CsilCborValue) -> Result<chrono::DateTime<chrono::Utc>, CsilCborError> {
    let CsilCborValue::Tag(0, inner) = v else {
        return Err(CsilCborError(
            "csil cbor: expected CBOR tag 0 timestamp".to_string(),
        ));
    };
    let CsilCborValue::Text(s) = inner.as_ref() else {
        return Err(CsilCborError(
            "csil cbor: timestamp content must be text".to_string(),
        ));
    };
    chrono::DateTime::parse_from_rfc3339(s)
        .map(|dt| dt.with_timezone(&chrono::Utc))
        .map_err(|e| CsilCborError(format!("csil cbor: invalid timestamp: {e}")))
}

fn csil_bigint_be_bytes(mut n: u128) -> Vec<u8> {
    if n == 0 {
        return vec![0];
    }
    let mut out = Vec::new();
    while n > 0 {
        out.push((n & 0xff) as u8);
        n >>= 8;
    }
    out.reverse();
    out
}

fn csil_be_bytes_to_u128(bytes: &[u8]) -> Result<u128, CsilCborError> {
    if bytes.len() > 16 {
        return Err(CsilCborError(
            "csil cbor: bignum exceeds 128 bits".to_string(),
        ));
    }
    let mut n: u128 = 0;
    for &b in bytes {
        n = (n << 8) | b as u128;
    }
    Ok(n)
}

/// Encode an exact integer mantissa: a CBOR integer when it fits in 64 bits,
/// otherwise a bignum so the value stays exact across the wire.
fn csil_enc_bigint(m: i128) -> CsilCborValue {
    if let Ok(v) = i64::try_from(m) {
        CsilCborValue::Int(v)
    } else if let Ok(v) = u64::try_from(m) {
        CsilCborValue::Uint(v)
    } else if m >= 0 {
        CsilCborValue::Tag(
            2,
            Box::new(CsilCborValue::Bytes(csil_bigint_be_bytes(m as u128))),
        )
    } else {
        // A negative bignum encodes the magnitude of -1 - value.
        let mag = (-(m + 1)) as u128;
        CsilCborValue::Tag(3, Box::new(CsilCborValue::Bytes(csil_bigint_be_bytes(mag))))
    }
}

fn csil_dec_bigint(v: &CsilCborValue) -> Result<i128, CsilCborError> {
    match v {
        CsilCborValue::Uint(x) => Ok(*x as i128),
        CsilCborValue::Int(x) => Ok(*x as i128),
        CsilCborValue::Tag(num, inner) => {
            let CsilCborValue::Bytes(bytes) = inner.as_ref() else {
                return Err(CsilCborError(
                    "csil cbor: bignum content must be a byte string".to_string(),
                ));
            };
            let mag = csil_be_bytes_to_u128(bytes)?;
            match num {
                2 => i128::try_from(mag).map_err(|_| {
                    CsilCborError("csil cbor: decimal mantissa overflows i128".to_string())
                }),
                3 => {
                    let val = i128::try_from(mag).map_err(|_| {
                        CsilCborError("csil cbor: decimal mantissa overflows i128".to_string())
                    })?;
                    Ok(-1 - val)
                }
                _ => Err(CsilCborError(format!(
                    "csil cbor: unexpected bignum tag {num}"
                ))),
            }
        }
        _ => Err(CsilCborError(
            "csil cbor: expected integer mantissa".to_string(),
        )),
    }
}

/// Encode a `CsilDecimal` as CBOR tag 4: `[exponent, mantissa]`.
fn csil_enc_decimal(d: &CsilDecimal) -> CsilCborValue {
    CsilCborValue::Tag(
        4,
        Box::new(CsilCborValue::Array(vec![
            CsilCborValue::Int(d.exponent),
            csil_enc_bigint(d.mantissa),
        ])),
    )
}

/// Decode a CBOR tag 4 decimal fraction into an exact `CsilDecimal`.
fn csil_as_decimal(v: &CsilCborValue) -> Result<CsilDecimal, CsilCborError> {
    let CsilCborValue::Tag(4, inner) = v else {
        return Err(CsilCborError(
            "csil cbor: expected CBOR tag 4 decimal".to_string(),
        ));
    };
    let CsilCborValue::Array(arr) = inner.as_ref() else {
        return Err(CsilCborError(
            "csil cbor: tag 4 content must be [exponent, mantissa]".to_string(),
        ));
    };
    if arr.len() != 2 {
        return Err(CsilCborError(
            "csil cbor: tag 4 content must be [exponent, mantissa]".to_string(),
        ));
    }
    let exponent = cbor_as_i64(&arr[0])?;
    let mantissa = csil_dec_bigint(&arr[1])?;
    Ok(CsilDecimal { exponent, mantissa })
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

/// Build the canonical CBOR value tree for a RecordRef.
fn csil_enc_record_ref(csil_v: &RecordRef) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("kind"), cbor_text(&csil_v.kind)));
    csil_entries.push((
        cbor_text("authority"),
        csil_enc_identity_ref(&csil_v.authority),
    ));
    csil_entries.push((cbor_text("record_id"), cbor_text(&csil_v.record_id)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a RecordRef from a decoded CBOR value tree.
fn csil_dec_record_ref(csil_root: &CsilCborValue) -> Result<RecordRef, CsilCborError> {
    let authority = {
        let csil_field = cbor_require(csil_root, "authority")?;
        let csil_decode = csil_dec_identity_ref;
        csil_decode(csil_field)?
    };
    let kind = {
        let csil_field = cbor_require(csil_root, "kind")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let record_id = {
        let csil_field = cbor_require(csil_root, "record_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(RecordRef {
        authority,
        kind,
        record_id,
    })
}

/// Encode a RecordRef to canonical CSIL CBOR bytes.
pub fn encode_record_ref(csil_v: &RecordRef) -> Vec<u8> {
    cbor_encode(&csil_enc_record_ref(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a RecordRef.
pub fn decode_record_ref(csil_data: &[u8]) -> Result<RecordRef, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_record_ref(&csil_root)
}

/// Build the canonical CBOR value tree for a ScopeRef.
fn csil_enc_scope_ref(csil_v: &ScopeRef) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("scope_id"), cbor_text(&csil_v.scope_id)));
    csil_entries.push((
        cbor_text("authority"),
        csil_enc_identity_ref(&csil_v.authority),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ScopeRef from a decoded CBOR value tree.
fn csil_dec_scope_ref(csil_root: &CsilCborValue) -> Result<ScopeRef, CsilCborError> {
    let authority = {
        let csil_field = cbor_require(csil_root, "authority")?;
        let csil_decode = csil_dec_identity_ref;
        csil_decode(csil_field)?
    };
    let scope_id = {
        let csil_field = cbor_require(csil_root, "scope_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(ScopeRef {
        authority,
        scope_id,
    })
}

/// Encode a ScopeRef to canonical CSIL CBOR bytes.
pub fn encode_scope_ref(csil_v: &ScopeRef) -> Vec<u8> {
    cbor_encode(&csil_enc_scope_ref(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ScopeRef.
pub fn decode_scope_ref(csil_data: &[u8]) -> Result<ScopeRef, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_scope_ref(&csil_root)
}

/// Build the canonical CBOR value tree for a SchemaRef.
fn csil_enc_schema_ref(csil_v: &SchemaRef) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("name"), cbor_text(&csil_v.name)));
    csil_entries.push((cbor_text("digest"), cbor_bytes(&csil_v.digest)));
    csil_entries.push((cbor_text("version"), cbor_uint(csil_v.version)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a SchemaRef from a decoded CBOR value tree.
fn csil_dec_schema_ref(csil_root: &CsilCborValue) -> Result<SchemaRef, CsilCborError> {
    let name = {
        let csil_field = cbor_require(csil_root, "name")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let version = {
        let csil_field = cbor_require(csil_root, "version")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let digest = {
        let csil_field = cbor_require(csil_root, "digest")?;
        let csil_decode = cbor_as_bytes;
        csil_decode(csil_field)?
    };
    Ok(SchemaRef {
        name,
        version,
        digest,
    })
}

/// Encode a SchemaRef to canonical CSIL CBOR bytes.
pub fn encode_schema_ref(csil_v: &SchemaRef) -> Vec<u8> {
    cbor_encode(&csil_enc_schema_ref(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a SchemaRef.
pub fn decode_schema_ref(csil_data: &[u8]) -> Result<SchemaRef, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_schema_ref(&csil_root)
}

/// Build the canonical CBOR value tree for a ArtifactRef.
fn csil_enc_artifact_ref(csil_v: &ArtifactRef) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(5);
    csil_entries.push((cbor_text("size"), cbor_uint(csil_v.size)));
    csil_entries.push((cbor_text("digest"), cbor_bytes(&csil_v.digest)));
    csil_entries.push((
        cbor_text("authority"),
        csil_enc_identity_ref(&csil_v.authority),
    ));
    csil_entries.push((cbor_text("object_id"), cbor_text(&csil_v.object_id)));
    csil_entries.push((cbor_text("media_type"), cbor_text(&csil_v.media_type)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ArtifactRef from a decoded CBOR value tree.
fn csil_dec_artifact_ref(csil_root: &CsilCborValue) -> Result<ArtifactRef, CsilCborError> {
    let authority = {
        let csil_field = cbor_require(csil_root, "authority")?;
        let csil_decode = csil_dec_identity_ref;
        csil_decode(csil_field)?
    };
    let object_id = {
        let csil_field = cbor_require(csil_root, "object_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let digest = {
        let csil_field = cbor_require(csil_root, "digest")?;
        let csil_decode = cbor_as_bytes;
        csil_decode(csil_field)?
    };
    let size = {
        let csil_field = cbor_require(csil_root, "size")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let media_type = {
        let csil_field = cbor_require(csil_root, "media_type")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(ArtifactRef {
        authority,
        object_id,
        digest,
        size,
        media_type,
    })
}

/// Encode a ArtifactRef to canonical CSIL CBOR bytes.
pub fn encode_artifact_ref(csil_v: &ArtifactRef) -> Vec<u8> {
    cbor_encode(&csil_enc_artifact_ref(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ArtifactRef.
pub fn decode_artifact_ref(csil_data: &[u8]) -> Result<ArtifactRef, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_artifact_ref(&csil_root)
}

/// Build the canonical CBOR value tree for a ContextAccess.
fn csil_enc_context_access(csil_v: &ContextAccess) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(6);
    csil_entries.push((cbor_text("actor"), csil_enc_identity_ref(&csil_v.actor)));
    if let Some(csil_inner) = &csil_v.persona {
        csil_entries.push((cbor_text("persona"), csil_enc_identity_ref(csil_inner)));
    }
    csil_entries.push((cbor_text("purpose"), cbor_text(&csil_v.purpose)));
    csil_entries.push((
        cbor_text("processor"),
        csil_enc_identity_ref(&csil_v.processor),
    ));
    csil_entries.push((
        cbor_text("control_scope"),
        csil_enc_scope_ref(&csil_v.control_scope),
    ));
    if let Some(csil_inner) = &csil_v.represented_identity {
        csil_entries.push((
            cbor_text("represented_identity"),
            csil_enc_identity_ref(csil_inner),
        ));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ContextAccess from a decoded CBOR value tree.
fn csil_dec_context_access(csil_root: &CsilCborValue) -> Result<ContextAccess, CsilCborError> {
    let actor = {
        let csil_field = cbor_require(csil_root, "actor")?;
        let csil_decode = csil_dec_identity_ref;
        csil_decode(csil_field)?
    };
    let control_scope = {
        let csil_field = cbor_require(csil_root, "control_scope")?;
        let csil_decode = csil_dec_scope_ref;
        csil_decode(csil_field)?
    };
    let purpose = {
        let csil_field = cbor_require(csil_root, "purpose")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let processor = {
        let csil_field = cbor_require(csil_root, "processor")?;
        let csil_decode = csil_dec_identity_ref;
        csil_decode(csil_field)?
    };
    let represented_identity = match cbor_map_get(csil_root, "represented_identity") {
        Some(csil_field) => {
            let csil_decode = csil_dec_identity_ref;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let persona = match cbor_map_get(csil_root, "persona") {
        Some(csil_field) => {
            let csil_decode = csil_dec_identity_ref;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(ContextAccess {
        actor,
        control_scope,
        purpose,
        processor,
        represented_identity,
        persona,
    })
}

/// Encode a ContextAccess to canonical CSIL CBOR bytes.
pub fn encode_context_access(csil_v: &ContextAccess) -> Vec<u8> {
    cbor_encode(&csil_enc_context_access(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ContextAccess.
pub fn decode_context_access(csil_data: &[u8]) -> Result<ContextAccess, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_context_access(&csil_root)
}

/// Build the canonical CBOR value tree for a Restrictions.
fn csil_enc_restrictions(csil_v: &Restrictions) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((
        cbor_text("required_scopes"),
        cbor_enc_array(&csil_v.required_scopes, csil_enc_scope_ref),
    ));
    csil_entries.push((
        cbor_text("permitted_purposes"),
        cbor_enc_array(&csil_v.permitted_purposes, |csil_elem| cbor_text(csil_elem)),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a Restrictions from a decoded CBOR value tree.
fn csil_dec_restrictions(csil_root: &CsilCborValue) -> Result<Restrictions, CsilCborError> {
    let required_scopes = {
        let csil_field = cbor_require(csil_root, "required_scopes")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_scope_ref);
        csil_decode(csil_field)?
    };
    let permitted_purposes = {
        let csil_field = cbor_require(csil_root, "permitted_purposes")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, cbor_as_text);
        csil_decode(csil_field)?
    };
    Ok(Restrictions {
        required_scopes,
        permitted_purposes,
    })
}

/// Encode a Restrictions to canonical CSIL CBOR bytes.
pub fn encode_restrictions(csil_v: &Restrictions) -> Vec<u8> {
    cbor_encode(&csil_enc_restrictions(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a Restrictions.
pub fn decode_restrictions(csil_data: &[u8]) -> Result<Restrictions, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_restrictions(&csil_root)
}

/// Build the canonical CBOR value tree for a ServiceError.
fn csil_enc_service_error(csil_v: &ServiceError) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("code"), csil_enc_error_code(&csil_v.code)));
    csil_entries.push((cbor_text("message"), cbor_text(&csil_v.message)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ServiceError from a decoded CBOR value tree.
fn csil_dec_service_error(csil_root: &CsilCborValue) -> Result<ServiceError, CsilCborError> {
    let code = {
        let csil_field = cbor_require(csil_root, "code")?;
        let csil_decode = csil_dec_error_code;
        csil_decode(csil_field)?
    };
    let message = {
        let csil_field = cbor_require(csil_root, "message")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(ServiceError { code, message })
}

/// Encode a ServiceError to canonical CSIL CBOR bytes.
pub fn encode_service_error(csil_v: &ServiceError) -> Vec<u8> {
    cbor_encode(&csil_enc_service_error(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ServiceError.
pub fn decode_service_error(csil_data: &[u8]) -> Result<ServiceError, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_service_error(&csil_root)
}

/// Build the canonical CBOR value tree for a EvidenceHeader.
fn csil_enc_evidence_header(csil_v: &EvidenceHeader) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(13);
    csil_entries.push((cbor_text("ref"), csil_enc_record_ref(&csil_v.r#ref)));
    csil_entries.push((
        cbor_text("causes"),
        cbor_enc_array(&csil_v.causes, csil_enc_record_ref),
    ));
    csil_entries.push((
        cbor_text("capture"),
        csil_enc_capture_status(&csil_v.capture),
    ));
    if let Some(csil_inner) = &csil_v.persona {
        csil_entries.push((cbor_text("persona"), csil_enc_identity_ref(csil_inner)));
    }
    if let Some(csil_inner) = &csil_v.agent_id {
        csil_entries.push((cbor_text("agent_id"), cbor_text(csil_inner)));
    }
    csil_entries.push((
        cbor_text("producer"),
        csil_enc_identity_ref(&csil_v.producer),
    ));
    csil_entries.push((cbor_text("stream_id"), cbor_text(&csil_v.stream_id)));
    if let Some(csil_inner) = &csil_v.attempt_id {
        csil_entries.push((cbor_text("attempt_id"), cbor_text(csil_inner)));
    }
    if let Some(csil_inner) = &csil_v.observed_at {
        csil_entries.push((cbor_text("observed_at"), csil_enc_timestamp(csil_inner)));
    }
    csil_entries.push((
        cbor_text("received_at"),
        csil_enc_timestamp(&csil_v.received_at),
    ));
    csil_entries.push((
        cbor_text("restrictions"),
        csil_enc_restrictions(&csil_v.restrictions),
    ));
    csil_entries.push((
        cbor_text("source_sequence"),
        cbor_uint(csil_v.source_sequence),
    ));
    if let Some(csil_inner) = &csil_v.represented_identity {
        csil_entries.push((
            cbor_text("represented_identity"),
            csil_enc_identity_ref(csil_inner),
        ));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a EvidenceHeader from a decoded CBOR value tree.
fn csil_dec_evidence_header(csil_root: &CsilCborValue) -> Result<EvidenceHeader, CsilCborError> {
    let r#ref = {
        let csil_field = cbor_require(csil_root, "ref")?;
        let csil_decode = csil_dec_record_ref;
        csil_decode(csil_field)?
    };
    let producer = {
        let csil_field = cbor_require(csil_root, "producer")?;
        let csil_decode = csil_dec_identity_ref;
        csil_decode(csil_field)?
    };
    let received_at = {
        let csil_field = cbor_require(csil_root, "received_at")?;
        let csil_decode = csil_as_timestamp;
        csil_decode(csil_field)?
    };
    let observed_at = match cbor_map_get(csil_root, "observed_at") {
        Some(csil_field) => {
            let csil_decode = csil_as_timestamp;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let stream_id = {
        let csil_field = cbor_require(csil_root, "stream_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let source_sequence = {
        let csil_field = cbor_require(csil_root, "source_sequence")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let restrictions = {
        let csil_field = cbor_require(csil_root, "restrictions")?;
        let csil_decode = csil_dec_restrictions;
        csil_decode(csil_field)?
    };
    let causes = {
        let csil_field = cbor_require(csil_root, "causes")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_record_ref);
        csil_decode(csil_field)?
    };
    let capture = {
        let csil_field = cbor_require(csil_root, "capture")?;
        let csil_decode = csil_dec_capture_status;
        csil_decode(csil_field)?
    };
    let represented_identity = match cbor_map_get(csil_root, "represented_identity") {
        Some(csil_field) => {
            let csil_decode = csil_dec_identity_ref;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let persona = match cbor_map_get(csil_root, "persona") {
        Some(csil_field) => {
            let csil_decode = csil_dec_identity_ref;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let agent_id = match cbor_map_get(csil_root, "agent_id") {
        Some(csil_field) => {
            let csil_decode = cbor_as_text;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let attempt_id = match cbor_map_get(csil_root, "attempt_id") {
        Some(csil_field) => {
            let csil_decode = cbor_as_text;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(EvidenceHeader {
        r#ref,
        producer,
        received_at,
        observed_at,
        stream_id,
        source_sequence,
        restrictions,
        causes,
        capture,
        represented_identity,
        persona,
        agent_id,
        attempt_id,
    })
}

/// Encode a EvidenceHeader to canonical CSIL CBOR bytes.
pub fn encode_evidence_header(csil_v: &EvidenceHeader) -> Vec<u8> {
    cbor_encode(&csil_enc_evidence_header(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a EvidenceHeader.
pub fn decode_evidence_header(csil_data: &[u8]) -> Result<EvidenceHeader, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_evidence_header(&csil_root)
}

/// Build the canonical CBOR value tree for a InteractionRecord.
fn csil_enc_interaction_record(csil_v: &InteractionRecord) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(7);
    if let Some(csil_inner) = &csil_v.media {
        csil_entries.push((cbor_text("media"), csil_enc_artifact_ref(csil_inner)));
    }
    csil_entries.push((
        cbor_text("header"),
        csil_enc_evidence_header(&csil_v.header),
    ));
    csil_entries.push((cbor_text("channel"), csil_enc_channel(&csil_v.channel)));
    csil_entries.push((cbor_text("content"), cbor_text(&csil_v.content)));
    csil_entries.push((cbor_text("point_id"), cbor_text(&csil_v.point_id)));
    csil_entries.push((
        cbor_text("reply_hints"),
        cbor_enc_array(&csil_v.reply_hints, csil_enc_record_ref),
    ));
    csil_entries.push((
        cbor_text("conversation_ids"),
        cbor_enc_array(&csil_v.conversation_ids, |csil_elem| cbor_text(csil_elem)),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a InteractionRecord from a decoded CBOR value tree.
fn csil_dec_interaction_record(
    csil_root: &CsilCborValue,
) -> Result<InteractionRecord, CsilCborError> {
    let header = {
        let csil_field = cbor_require(csil_root, "header")?;
        let csil_decode = csil_dec_evidence_header;
        csil_decode(csil_field)?
    };
    let point_id = {
        let csil_field = cbor_require(csil_root, "point_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let channel = {
        let csil_field = cbor_require(csil_root, "channel")?;
        let csil_decode = csil_dec_channel;
        csil_decode(csil_field)?
    };
    let content = {
        let csil_field = cbor_require(csil_root, "content")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let media = match cbor_map_get(csil_root, "media") {
        Some(csil_field) => {
            let csil_decode = csil_dec_artifact_ref;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let conversation_ids = {
        let csil_field = cbor_require(csil_root, "conversation_ids")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, cbor_as_text);
        csil_decode(csil_field)?
    };
    let reply_hints = {
        let csil_field = cbor_require(csil_root, "reply_hints")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_record_ref);
        csil_decode(csil_field)?
    };
    Ok(InteractionRecord {
        header,
        point_id,
        channel,
        content,
        media,
        conversation_ids,
        reply_hints,
    })
}

/// Encode a InteractionRecord to canonical CSIL CBOR bytes.
pub fn encode_interaction_record(csil_v: &InteractionRecord) -> Vec<u8> {
    cbor_encode(&csil_enc_interaction_record(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a InteractionRecord.
pub fn decode_interaction_record(csil_data: &[u8]) -> Result<InteractionRecord, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_interaction_record(&csil_root)
}

/// Build the canonical CBOR value tree for a DestinationMeaning.
fn csil_enc_destination_meaning(csil_v: &DestinationMeaning) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("content"), cbor_text(&csil_v.content)));
    csil_entries.push((cbor_text("session_id"), cbor_text(&csil_v.session_id)));
    csil_entries.push((
        cbor_text("prompt_refs"),
        cbor_enc_array(&csil_v.prompt_refs, csil_enc_record_ref),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a DestinationMeaning from a decoded CBOR value tree.
fn csil_dec_destination_meaning(
    csil_root: &CsilCborValue,
) -> Result<DestinationMeaning, CsilCborError> {
    let session_id = {
        let csil_field = cbor_require(csil_root, "session_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let prompt_refs = {
        let csil_field = cbor_require(csil_root, "prompt_refs")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_record_ref);
        csil_decode(csil_field)?
    };
    let content = {
        let csil_field = cbor_require(csil_root, "content")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(DestinationMeaning {
        session_id,
        prompt_refs,
        content,
    })
}

/// Encode a DestinationMeaning to canonical CSIL CBOR bytes.
pub fn encode_destination_meaning(csil_v: &DestinationMeaning) -> Vec<u8> {
    cbor_encode(&csil_enc_destination_meaning(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a DestinationMeaning.
pub fn decode_destination_meaning(csil_data: &[u8]) -> Result<DestinationMeaning, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_destination_meaning(&csil_root)
}

/// Build the canonical CBOR value tree for a InterpretationRecord.
fn csil_enc_interpretation_record(csil_v: &InterpretationRecord) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(7);
    if let Some(csil_inner) = &csil_v.prior {
        csil_entries.push((cbor_text("prior"), csil_enc_record_ref(csil_inner)));
    }
    csil_entries.push((
        cbor_text("header"),
        csil_enc_evidence_header(&csil_v.header),
    ));
    csil_entries.push((cbor_text("method"), cbor_text(&csil_v.method)));
    csil_entries.push((
        cbor_text("sources"),
        cbor_enc_array(&csil_v.sources, csil_enc_record_ref),
    ));
    csil_entries.push((cbor_text("uncertainty"), cbor_text(&csil_v.uncertainty)));
    csil_entries.push((
        cbor_text("context_refs"),
        cbor_enc_array(&csil_v.context_refs, csil_enc_record_ref),
    ));
    csil_entries.push((
        cbor_text("destinations"),
        cbor_enc_array(&csil_v.destinations, csil_enc_destination_meaning),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a InterpretationRecord from a decoded CBOR value tree.
fn csil_dec_interpretation_record(
    csil_root: &CsilCborValue,
) -> Result<InterpretationRecord, CsilCborError> {
    let header = {
        let csil_field = cbor_require(csil_root, "header")?;
        let csil_decode = csil_dec_evidence_header;
        csil_decode(csil_field)?
    };
    let sources = {
        let csil_field = cbor_require(csil_root, "sources")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_record_ref);
        csil_decode(csil_field)?
    };
    let context_refs = {
        let csil_field = cbor_require(csil_root, "context_refs")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_record_ref);
        csil_decode(csil_field)?
    };
    let destinations = {
        let csil_field = cbor_require(csil_root, "destinations")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_destination_meaning);
        csil_decode(csil_field)?
    };
    let method = {
        let csil_field = cbor_require(csil_root, "method")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let uncertainty = {
        let csil_field = cbor_require(csil_root, "uncertainty")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let prior = match cbor_map_get(csil_root, "prior") {
        Some(csil_field) => {
            let csil_decode = csil_dec_record_ref;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(InterpretationRecord {
        header,
        sources,
        context_refs,
        destinations,
        method,
        uncertainty,
        prior,
    })
}

/// Encode a InterpretationRecord to canonical CSIL CBOR bytes.
pub fn encode_interpretation_record(csil_v: &InterpretationRecord) -> Vec<u8> {
    cbor_encode(&csil_enc_interpretation_record(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a InterpretationRecord.
pub fn decode_interpretation_record(
    csil_data: &[u8],
) -> Result<InterpretationRecord, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_interpretation_record(&csil_root)
}

/// Build the canonical CBOR value tree for a SessionInput.
fn csil_enc_session_input(csil_v: &SessionInput) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(7);
    csil_entries.push((
        cbor_text("header"),
        csil_enc_evidence_header(&csil_v.header),
    ));
    csil_entries.push((cbor_text("content"), cbor_text(&csil_v.content)));
    csil_entries.push((
        cbor_text("sources"),
        cbor_enc_array(&csil_v.sources, csil_enc_record_ref),
    ));
    csil_entries.push((cbor_text("session_id"), cbor_text(&csil_v.session_id)));
    csil_entries.push((cbor_text("command_key"), cbor_text(&csil_v.command_key)));
    csil_entries.push((
        cbor_text("interpretation"),
        csil_enc_record_ref(&csil_v.interpretation),
    ));
    csil_entries.push((
        cbor_text("policy_revision"),
        cbor_uint(csil_v.policy_revision),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a SessionInput from a decoded CBOR value tree.
fn csil_dec_session_input(csil_root: &CsilCborValue) -> Result<SessionInput, CsilCborError> {
    let header = {
        let csil_field = cbor_require(csil_root, "header")?;
        let csil_decode = csil_dec_evidence_header;
        csil_decode(csil_field)?
    };
    let session_id = {
        let csil_field = cbor_require(csil_root, "session_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let interpretation = {
        let csil_field = cbor_require(csil_root, "interpretation")?;
        let csil_decode = csil_dec_record_ref;
        csil_decode(csil_field)?
    };
    let sources = {
        let csil_field = cbor_require(csil_root, "sources")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_record_ref);
        csil_decode(csil_field)?
    };
    let content = {
        let csil_field = cbor_require(csil_root, "content")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let command_key = {
        let csil_field = cbor_require(csil_root, "command_key")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let policy_revision = {
        let csil_field = cbor_require(csil_root, "policy_revision")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    Ok(SessionInput {
        header,
        session_id,
        interpretation,
        sources,
        content,
        command_key,
        policy_revision,
    })
}

/// Encode a SessionInput to canonical CSIL CBOR bytes.
pub fn encode_session_input(csil_v: &SessionInput) -> Vec<u8> {
    cbor_encode(&csil_enc_session_input(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a SessionInput.
pub fn decode_session_input(csil_data: &[u8]) -> Result<SessionInput, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_session_input(&csil_root)
}

/// Build the canonical CBOR value tree for a PresentationRecord.
fn csil_enc_presentation_record(csil_v: &PresentationRecord) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(4);
    csil_entries.push((
        cbor_text("state"),
        csil_enc_presentation_state(&csil_v.state),
    ));
    csil_entries.push((
        cbor_text("header"),
        csil_enc_evidence_header(&csil_v.header),
    ));
    csil_entries.push((cbor_text("output"), csil_enc_record_ref(&csil_v.output)));
    csil_entries.push((cbor_text("point_id"), cbor_text(&csil_v.point_id)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a PresentationRecord from a decoded CBOR value tree.
fn csil_dec_presentation_record(
    csil_root: &CsilCborValue,
) -> Result<PresentationRecord, CsilCborError> {
    let header = {
        let csil_field = cbor_require(csil_root, "header")?;
        let csil_decode = csil_dec_evidence_header;
        csil_decode(csil_field)?
    };
    let output = {
        let csil_field = cbor_require(csil_root, "output")?;
        let csil_decode = csil_dec_record_ref;
        csil_decode(csil_field)?
    };
    let point_id = {
        let csil_field = cbor_require(csil_root, "point_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let state = {
        let csil_field = cbor_require(csil_root, "state")?;
        let csil_decode = csil_dec_presentation_state;
        csil_decode(csil_field)?
    };
    Ok(PresentationRecord {
        header,
        output,
        point_id,
        state,
    })
}

/// Encode a PresentationRecord to canonical CSIL CBOR bytes.
pub fn encode_presentation_record(csil_v: &PresentationRecord) -> Vec<u8> {
    cbor_encode(&csil_enc_presentation_record(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a PresentationRecord.
pub fn decode_presentation_record(csil_data: &[u8]) -> Result<PresentationRecord, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_presentation_record(&csil_root)
}

/// Build the canonical CBOR value tree for a TypedDimension.
fn csil_enc_typed_dimension(csil_v: &TypedDimension) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("schema"), csil_enc_schema_ref(&csil_v.schema)));
    csil_entries.push((cbor_text("payload"), cbor_bytes(&csil_v.payload)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a TypedDimension from a decoded CBOR value tree.
fn csil_dec_typed_dimension(csil_root: &CsilCborValue) -> Result<TypedDimension, CsilCborError> {
    let schema = {
        let csil_field = cbor_require(csil_root, "schema")?;
        let csil_decode = csil_dec_schema_ref;
        csil_decode(csil_field)?
    };
    let payload = {
        let csil_field = cbor_require(csil_root, "payload")?;
        let csil_decode = cbor_as_bytes;
        csil_decode(csil_field)?
    };
    Ok(TypedDimension { schema, payload })
}

/// Encode a TypedDimension to canonical CSIL CBOR bytes.
pub fn encode_typed_dimension(csil_v: &TypedDimension) -> Vec<u8> {
    cbor_encode(&csil_enc_typed_dimension(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a TypedDimension.
pub fn decode_typed_dimension(csil_data: &[u8]) -> Result<TypedDimension, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_typed_dimension(&csil_root)
}

/// Build the canonical CBOR value tree for a LearningAssessment.
fn csil_enc_learning_assessment(csil_v: &LearningAssessment) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(7);
    if let Some(csil_inner) = &csil_v.prior {
        csil_entries.push((cbor_text("prior"), csil_enc_record_ref(csil_inner)));
    }
    csil_entries.push((
        cbor_text("header"),
        csil_enc_evidence_header(&csil_v.header),
    ));
    csil_entries.push((
        cbor_text("inputs"),
        cbor_enc_array(&csil_v.inputs, csil_enc_record_ref),
    ));
    csil_entries.push((cbor_text("method"), cbor_text(&csil_v.method)));
    csil_entries.push((
        cbor_text("contrary"),
        cbor_enc_array(&csil_v.contrary, csil_enc_record_ref),
    ));
    if let Some(csil_inner) = &csil_v.dimensions {
        csil_entries.push((
            cbor_text("dimensions"),
            cbor_enc_array(csil_inner, csil_enc_typed_dimension),
        ));
    }
    csil_entries.push((
        cbor_text("supporting"),
        cbor_enc_array(&csil_v.supporting, csil_enc_record_ref),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a LearningAssessment from a decoded CBOR value tree.
fn csil_dec_learning_assessment(
    csil_root: &CsilCborValue,
) -> Result<LearningAssessment, CsilCborError> {
    let header = {
        let csil_field = cbor_require(csil_root, "header")?;
        let csil_decode = csil_dec_evidence_header;
        csil_decode(csil_field)?
    };
    let inputs = {
        let csil_field = cbor_require(csil_root, "inputs")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_record_ref);
        csil_decode(csil_field)?
    };
    let method = {
        let csil_field = cbor_require(csil_root, "method")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let supporting = {
        let csil_field = cbor_require(csil_root, "supporting")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_record_ref);
        csil_decode(csil_field)?
    };
    let contrary = {
        let csil_field = cbor_require(csil_root, "contrary")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_record_ref);
        csil_decode(csil_field)?
    };
    let prior = match cbor_map_get(csil_root, "prior") {
        Some(csil_field) => {
            let csil_decode = csil_dec_record_ref;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let dimensions = match cbor_map_get(csil_root, "dimensions") {
        Some(csil_field) => {
            let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_typed_dimension);
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(LearningAssessment {
        header,
        inputs,
        method,
        supporting,
        contrary,
        prior,
        dimensions,
    })
}

/// Encode a LearningAssessment to canonical CSIL CBOR bytes.
pub fn encode_learning_assessment(csil_v: &LearningAssessment) -> Vec<u8> {
    cbor_encode(&csil_enc_learning_assessment(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a LearningAssessment.
pub fn decode_learning_assessment(csil_data: &[u8]) -> Result<LearningAssessment, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_learning_assessment(&csil_root)
}

/// Build the canonical CBOR value tree for a SourceContext.
fn csil_enc_source_context(csil_v: &SourceContext) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(8);
    if let Some(csil_inner) = &csil_v.author {
        csil_entries.push((cbor_text("author"), csil_enc_identity_ref(csil_inner)));
    }
    csil_entries.push((
        cbor_text("header"),
        csil_enc_evidence_header(&csil_v.header),
    ));
    csil_entries.push((cbor_text("adapter"), cbor_text(&csil_v.adapter)));
    csil_entries.push((cbor_text("content"), cbor_text(&csil_v.content)));
    if let Some(csil_inner) = &csil_v.artifact {
        csil_entries.push((cbor_text("artifact"), csil_enc_artifact_ref(csil_inner)));
    }
    csil_entries.push((cbor_text("resource_id"), cbor_text(&csil_v.resource_id)));
    if let Some(csil_inner) = &csil_v.origin_revision {
        csil_entries.push((cbor_text("origin_revision"), cbor_text(csil_inner)));
    }
    csil_entries.push((
        cbor_text("enrichment_allowed"),
        cbor_bool(csil_v.enrichment_allowed),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a SourceContext from a decoded CBOR value tree.
fn csil_dec_source_context(csil_root: &CsilCborValue) -> Result<SourceContext, CsilCborError> {
    let header = {
        let csil_field = cbor_require(csil_root, "header")?;
        let csil_decode = csil_dec_evidence_header;
        csil_decode(csil_field)?
    };
    let resource_id = {
        let csil_field = cbor_require(csil_root, "resource_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let adapter = {
        let csil_field = cbor_require(csil_root, "adapter")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let origin_revision = match cbor_map_get(csil_root, "origin_revision") {
        Some(csil_field) => {
            let csil_decode = cbor_as_text;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let author = match cbor_map_get(csil_root, "author") {
        Some(csil_field) => {
            let csil_decode = csil_dec_identity_ref;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let content = {
        let csil_field = cbor_require(csil_root, "content")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let enrichment_allowed = {
        let csil_field = cbor_require(csil_root, "enrichment_allowed")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    let artifact = match cbor_map_get(csil_root, "artifact") {
        Some(csil_field) => {
            let csil_decode = csil_dec_artifact_ref;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(SourceContext {
        header,
        resource_id,
        adapter,
        origin_revision,
        author,
        content,
        enrichment_allowed,
        artifact,
    })
}

/// Encode a SourceContext to canonical CSIL CBOR bytes.
pub fn encode_source_context(csil_v: &SourceContext) -> Vec<u8> {
    cbor_encode(&csil_enc_source_context(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a SourceContext.
pub fn decode_source_context(csil_data: &[u8]) -> Result<SourceContext, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_source_context(&csil_root)
}

/// Build the canonical CBOR value tree for a Receipt.
fn csil_enc_receipt(csil_v: &Receipt) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("record"), csil_enc_record_ref(&csil_v.record)));
    csil_entries.push((
        cbor_text("accepted_at"),
        csil_enc_timestamp(&csil_v.accepted_at),
    ));
    csil_entries.push((cbor_text("command_key"), cbor_text(&csil_v.command_key)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a Receipt from a decoded CBOR value tree.
fn csil_dec_receipt(csil_root: &CsilCborValue) -> Result<Receipt, CsilCborError> {
    let record = {
        let csil_field = cbor_require(csil_root, "record")?;
        let csil_decode = csil_dec_record_ref;
        csil_decode(csil_field)?
    };
    let command_key = {
        let csil_field = cbor_require(csil_root, "command_key")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let accepted_at = {
        let csil_field = cbor_require(csil_root, "accepted_at")?;
        let csil_decode = csil_as_timestamp;
        csil_decode(csil_field)?
    };
    Ok(Receipt {
        record,
        command_key,
        accepted_at,
    })
}

/// Encode a Receipt to canonical CSIL CBOR bytes.
pub fn encode_receipt(csil_v: &Receipt) -> Vec<u8> {
    cbor_encode(&csil_enc_receipt(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a Receipt.
pub fn decode_receipt(csil_data: &[u8]) -> Result<Receipt, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_receipt(&csil_root)
}

/// Build the canonical CBOR value tree for a DeliveryReceipt.
fn csil_enc_delivery_receipt(csil_v: &DeliveryReceipt) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("input"), csil_enc_record_ref(&csil_v.input)));
    csil_entries.push((cbor_text("destination"), cbor_text(&csil_v.destination)));
    csil_entries.push((
        cbor_text("received_at"),
        csil_enc_timestamp(&csil_v.received_at),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a DeliveryReceipt from a decoded CBOR value tree.
fn csil_dec_delivery_receipt(csil_root: &CsilCborValue) -> Result<DeliveryReceipt, CsilCborError> {
    let input = {
        let csil_field = cbor_require(csil_root, "input")?;
        let csil_decode = csil_dec_record_ref;
        csil_decode(csil_field)?
    };
    let destination = {
        let csil_field = cbor_require(csil_root, "destination")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let received_at = {
        let csil_field = cbor_require(csil_root, "received_at")?;
        let csil_decode = csil_as_timestamp;
        csil_decode(csil_field)?
    };
    Ok(DeliveryReceipt {
        input,
        destination,
        received_at,
    })
}

/// Encode a DeliveryReceipt to canonical CSIL CBOR bytes.
pub fn encode_delivery_receipt(csil_v: &DeliveryReceipt) -> Vec<u8> {
    cbor_encode(&csil_enc_delivery_receipt(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a DeliveryReceipt.
pub fn decode_delivery_receipt(csil_data: &[u8]) -> Result<DeliveryReceipt, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_delivery_receipt(&csil_root)
}

/// Build the canonical CBOR value tree for a ContextUse.
fn csil_enc_context_use(csil_v: &ContextUse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("input"), csil_enc_record_ref(&csil_v.input)));
    csil_entries.push((cbor_text("used_at"), csil_enc_timestamp(&csil_v.used_at)));
    csil_entries.push((cbor_text("context_id"), cbor_text(&csil_v.context_id)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ContextUse from a decoded CBOR value tree.
fn csil_dec_context_use(csil_root: &CsilCborValue) -> Result<ContextUse, CsilCborError> {
    let input = {
        let csil_field = cbor_require(csil_root, "input")?;
        let csil_decode = csil_dec_record_ref;
        csil_decode(csil_field)?
    };
    let context_id = {
        let csil_field = cbor_require(csil_root, "context_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let used_at = {
        let csil_field = cbor_require(csil_root, "used_at")?;
        let csil_decode = csil_as_timestamp;
        csil_decode(csil_field)?
    };
    Ok(ContextUse {
        input,
        context_id,
        used_at,
    })
}

/// Encode a ContextUse to canonical CSIL CBOR bytes.
pub fn encode_context_use(csil_v: &ContextUse) -> Vec<u8> {
    cbor_encode(&csil_enc_context_use(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ContextUse.
pub fn decode_context_use(csil_data: &[u8]) -> Result<ContextUse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_context_use(&csil_root)
}

/// Build the canonical CBOR value tree for a HistoryPage.
fn csil_enc_history_page(csil_v: &HistoryPage) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((
        cbor_text("records"),
        cbor_enc_array(&csil_v.records, csil_enc_evidence_record),
    ));
    if let Some(csil_inner) = &csil_v.next_cursor {
        csil_entries.push((cbor_text("next_cursor"), cbor_bytes(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a HistoryPage from a decoded CBOR value tree.
fn csil_dec_history_page(csil_root: &CsilCborValue) -> Result<HistoryPage, CsilCborError> {
    let records = {
        let csil_field = cbor_require(csil_root, "records")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_evidence_record);
        csil_decode(csil_field)?
    };
    let next_cursor = match cbor_map_get(csil_root, "next_cursor") {
        Some(csil_field) => {
            let csil_decode = cbor_as_bytes;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(HistoryPage {
        records,
        next_cursor,
    })
}

/// Encode a HistoryPage to canonical CSIL CBOR bytes.
pub fn encode_history_page(csil_v: &HistoryPage) -> Vec<u8> {
    cbor_encode(&csil_enc_history_page(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a HistoryPage.
pub fn decode_history_page(csil_data: &[u8]) -> Result<HistoryPage, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_history_page(&csil_root)
}

/// Build the canonical CBOR value tree for a Quantity.
fn csil_enc_quantity(csil_v: &Quantity) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("name"), cbor_text(&csil_v.name)));
    csil_entries.push((cbor_text("unit"), cbor_text(&csil_v.unit)));
    csil_entries.push((cbor_text("value"), csil_enc_decimal(&csil_v.value)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a Quantity from a decoded CBOR value tree.
fn csil_dec_quantity(csil_root: &CsilCborValue) -> Result<Quantity, CsilCborError> {
    let name = {
        let csil_field = cbor_require(csil_root, "name")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let value = {
        let csil_field = cbor_require(csil_root, "value")?;
        let csil_decode = csil_as_decimal;
        csil_decode(csil_field)?
    };
    let unit = {
        let csil_field = cbor_require(csil_root, "unit")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(Quantity { name, value, unit })
}

/// Encode a Quantity to canonical CSIL CBOR bytes.
pub fn encode_quantity(csil_v: &Quantity) -> Vec<u8> {
    cbor_encode(&csil_enc_quantity(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a Quantity.
pub fn decode_quantity(csil_data: &[u8]) -> Result<Quantity, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_quantity(&csil_root)
}

/// Build the canonical CBOR value tree for a KnowledgeQuery.
fn csil_enc_knowledge_query(csil_v: &KnowledgeQuery) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(5);
    if let Some(csil_inner) = &csil_v.after {
        csil_entries.push((cbor_text("after"), csil_enc_timestamp(csil_inner)));
    }
    csil_entries.push((cbor_text("limit"), cbor_uint(csil_v.limit)));
    if let Some(csil_inner) = &csil_v.before {
        csil_entries.push((cbor_text("before"), csil_enc_timestamp(csil_inner)));
    }
    csil_entries.push((cbor_text("purpose"), cbor_text(&csil_v.purpose)));
    csil_entries.push((cbor_text("question"), cbor_text(&csil_v.question)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a KnowledgeQuery from a decoded CBOR value tree.
fn csil_dec_knowledge_query(csil_root: &CsilCborValue) -> Result<KnowledgeQuery, CsilCborError> {
    let question = {
        let csil_field = cbor_require(csil_root, "question")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let purpose = {
        let csil_field = cbor_require(csil_root, "purpose")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let limit = {
        let csil_field = cbor_require(csil_root, "limit")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let after = match cbor_map_get(csil_root, "after") {
        Some(csil_field) => {
            let csil_decode = csil_as_timestamp;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let before = match cbor_map_get(csil_root, "before") {
        Some(csil_field) => {
            let csil_decode = csil_as_timestamp;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(KnowledgeQuery {
        question,
        purpose,
        limit,
        after,
        before,
    })
}

/// Encode a KnowledgeQuery to canonical CSIL CBOR bytes.
pub fn encode_knowledge_query(csil_v: &KnowledgeQuery) -> Vec<u8> {
    cbor_encode(&csil_enc_knowledge_query(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a KnowledgeQuery.
pub fn decode_knowledge_query(csil_data: &[u8]) -> Result<KnowledgeQuery, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_knowledge_query(&csil_root)
}

/// Build the canonical CBOR value tree for a QueryMatch.
fn csil_enc_query_match(csil_v: &QueryMatch) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("source"), csil_enc_record_ref(&csil_v.source)));
    csil_entries.push((cbor_text("content"), cbor_text(&csil_v.content)));
    csil_entries.push((
        cbor_text("observed_at"),
        csil_enc_timestamp(&csil_v.observed_at),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a QueryMatch from a decoded CBOR value tree.
fn csil_dec_query_match(csil_root: &CsilCborValue) -> Result<QueryMatch, CsilCborError> {
    let source = {
        let csil_field = cbor_require(csil_root, "source")?;
        let csil_decode = csil_dec_record_ref;
        csil_decode(csil_field)?
    };
    let content = {
        let csil_field = cbor_require(csil_root, "content")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let observed_at = {
        let csil_field = cbor_require(csil_root, "observed_at")?;
        let csil_decode = csil_as_timestamp;
        csil_decode(csil_field)?
    };
    Ok(QueryMatch {
        source,
        content,
        observed_at,
    })
}

/// Encode a QueryMatch to canonical CSIL CBOR bytes.
pub fn encode_query_match(csil_v: &QueryMatch) -> Vec<u8> {
    cbor_encode(&csil_enc_query_match(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a QueryMatch.
pub fn decode_query_match(csil_data: &[u8]) -> Result<QueryMatch, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_query_match(&csil_root)
}

/// Build the canonical CBOR value tree for a QueryResult.
fn csil_enc_query_result(csil_v: &QueryResult) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(4);
    csil_entries.push((
        cbor_text("gaps"),
        cbor_enc_array(&csil_v.gaps, |csil_elem| cbor_text(csil_elem)),
    ));
    csil_entries.push((
        cbor_text("status"),
        csil_enc_retrieval_status(&csil_v.status),
    ));
    csil_entries.push((
        cbor_text("matches"),
        cbor_enc_array(&csil_v.matches, csil_enc_query_match),
    ));
    if let Some(csil_inner) = &csil_v.quantities {
        csil_entries.push((
            cbor_text("quantities"),
            cbor_enc_array(csil_inner, csil_enc_quantity),
        ));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a QueryResult from a decoded CBOR value tree.
fn csil_dec_query_result(csil_root: &CsilCborValue) -> Result<QueryResult, CsilCborError> {
    let status = {
        let csil_field = cbor_require(csil_root, "status")?;
        let csil_decode = csil_dec_retrieval_status;
        csil_decode(csil_field)?
    };
    let matches = {
        let csil_field = cbor_require(csil_root, "matches")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_query_match);
        csil_decode(csil_field)?
    };
    let gaps = {
        let csil_field = cbor_require(csil_root, "gaps")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, cbor_as_text);
        csil_decode(csil_field)?
    };
    let quantities = match cbor_map_get(csil_root, "quantities") {
        Some(csil_field) => {
            let csil_decode = |csil_v| cbor_dec_array(csil_v, csil_dec_quantity);
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(QueryResult {
        status,
        matches,
        gaps,
        quantities,
    })
}

/// Encode a QueryResult to canonical CSIL CBOR bytes.
pub fn encode_query_result(csil_v: &QueryResult) -> Vec<u8> {
    cbor_encode(&csil_enc_query_result(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a QueryResult.
pub fn decode_query_result(csil_data: &[u8]) -> Result<QueryResult, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_query_result(&csil_root)
}

/// Build the canonical CBOR value tree for a SubmitInputRequest.
fn csil_enc_submit_input_request(csil_v: &SubmitInputRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("command_key"), cbor_text(&csil_v.command_key)));
    csil_entries.push((
        cbor_text("interaction"),
        csil_enc_interaction_record(&csil_v.interaction),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a SubmitInputRequest from a decoded CBOR value tree.
fn csil_dec_submit_input_request(
    csil_root: &CsilCborValue,
) -> Result<SubmitInputRequest, CsilCborError> {
    let command_key = {
        let csil_field = cbor_require(csil_root, "command_key")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let interaction = {
        let csil_field = cbor_require(csil_root, "interaction")?;
        let csil_decode = csil_dec_interaction_record;
        csil_decode(csil_field)?
    };
    Ok(SubmitInputRequest {
        command_key,
        interaction,
    })
}

/// Encode a SubmitInputRequest to canonical CSIL CBOR bytes.
pub fn encode_submit_input_request(csil_v: &SubmitInputRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_submit_input_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a SubmitInputRequest.
pub fn decode_submit_input_request(csil_data: &[u8]) -> Result<SubmitInputRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_submit_input_request(&csil_root)
}

/// Build the canonical CBOR value tree for a CorrectAssociationRequest.
fn csil_enc_correct_association_request(csil_v: &CorrectAssociationRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("command_key"), cbor_text(&csil_v.command_key)));
    csil_entries.push((
        cbor_text("interpretation"),
        csil_enc_interpretation_record(&csil_v.interpretation),
    ));
    csil_entries.push((
        cbor_text("expected_revision"),
        cbor_uint(csil_v.expected_revision),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CorrectAssociationRequest from a decoded CBOR value tree.
fn csil_dec_correct_association_request(
    csil_root: &CsilCborValue,
) -> Result<CorrectAssociationRequest, CsilCborError> {
    let command_key = {
        let csil_field = cbor_require(csil_root, "command_key")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let expected_revision = {
        let csil_field = cbor_require(csil_root, "expected_revision")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let interpretation = {
        let csil_field = cbor_require(csil_root, "interpretation")?;
        let csil_decode = csil_dec_interpretation_record;
        csil_decode(csil_field)?
    };
    Ok(CorrectAssociationRequest {
        command_key,
        expected_revision,
        interpretation,
    })
}

/// Encode a CorrectAssociationRequest to canonical CSIL CBOR bytes.
pub fn encode_correct_association_request(csil_v: &CorrectAssociationRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_correct_association_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CorrectAssociationRequest.
pub fn decode_correct_association_request(
    csil_data: &[u8],
) -> Result<CorrectAssociationRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_correct_association_request(&csil_root)
}

/// Build the canonical CBOR value tree for a ReadHistoryRequest.
fn csil_enc_read_history_request(csil_v: &ReadHistoryRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("root"), csil_enc_record_ref(&csil_v.root)));
    csil_entries.push((cbor_text("limit"), cbor_uint(csil_v.limit)));
    if let Some(csil_inner) = &csil_v.after_cursor {
        csil_entries.push((cbor_text("after_cursor"), cbor_bytes(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ReadHistoryRequest from a decoded CBOR value tree.
fn csil_dec_read_history_request(
    csil_root: &CsilCborValue,
) -> Result<ReadHistoryRequest, CsilCborError> {
    let root = {
        let csil_field = cbor_require(csil_root, "root")?;
        let csil_decode = csil_dec_record_ref;
        csil_decode(csil_field)?
    };
    let limit = {
        let csil_field = cbor_require(csil_root, "limit")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let after_cursor = match cbor_map_get(csil_root, "after_cursor") {
        Some(csil_field) => {
            let csil_decode = cbor_as_bytes;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(ReadHistoryRequest {
        root,
        limit,
        after_cursor,
    })
}

/// Encode a ReadHistoryRequest to canonical CSIL CBOR bytes.
pub fn encode_read_history_request(csil_v: &ReadHistoryRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_read_history_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ReadHistoryRequest.
pub fn decode_read_history_request(csil_data: &[u8]) -> Result<ReadHistoryRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_read_history_request(&csil_root)
}

/// Encode a CaptureStatus enum as its bare literal value.
fn csil_enc_capture_status(csil_v: &CaptureStatus) -> CsilCborValue {
    match csil_v {
        CaptureStatus::Complete => cbor_text("complete"),
        CaptureStatus::Incomplete => cbor_text("incomplete"),
        CaptureStatus::Unknown => cbor_text("unknown"),
    }
}

/// Decode a bare literal value into a CaptureStatus enum.
fn csil_dec_capture_status(csil_v: &CsilCborValue) -> Result<CaptureStatus, CsilCborError> {
    let csil_val = cbor_as_text(csil_v)?;
    match csil_val.as_str() {
        "complete" => Ok(CaptureStatus::Complete),
        "incomplete" => Ok(CaptureStatus::Incomplete),
        "unknown" => Ok(CaptureStatus::Unknown),
        csil_other => Err(CsilCborError(format!(
            "csil cbor: unknown CaptureStatus value {csil_other:?}"
        ))),
    }
}

/// Encode a Channel enum as its bare literal value.
fn csil_enc_channel(csil_v: &Channel) -> CsilCborValue {
    match csil_v {
        Channel::Text => cbor_text("text"),
        Channel::Voice => cbor_text("voice"),
        Channel::System => cbor_text("system"),
    }
}

/// Decode a bare literal value into a Channel enum.
fn csil_dec_channel(csil_v: &CsilCborValue) -> Result<Channel, CsilCborError> {
    let csil_val = cbor_as_text(csil_v)?;
    match csil_val.as_str() {
        "text" => Ok(Channel::Text),
        "voice" => Ok(Channel::Voice),
        "system" => Ok(Channel::System),
        csil_other => Err(CsilCborError(format!(
            "csil cbor: unknown Channel value {csil_other:?}"
        ))),
    }
}

/// Encode a PresentationState enum as its bare literal value.
fn csil_enc_presentation_state(csil_v: &PresentationState) -> CsilCborValue {
    match csil_v {
        PresentationState::Queued => cbor_text("queued"),
        PresentationState::Started => cbor_text("started"),
        PresentationState::Interrupted => cbor_text("interrupted"),
        PresentationState::Completed => cbor_text("completed"),
    }
}

/// Decode a bare literal value into a PresentationState enum.
fn csil_dec_presentation_state(csil_v: &CsilCborValue) -> Result<PresentationState, CsilCborError> {
    let csil_val = cbor_as_text(csil_v)?;
    match csil_val.as_str() {
        "queued" => Ok(PresentationState::Queued),
        "started" => Ok(PresentationState::Started),
        "interrupted" => Ok(PresentationState::Interrupted),
        "completed" => Ok(PresentationState::Completed),
        csil_other => Err(CsilCborError(format!(
            "csil cbor: unknown PresentationState value {csil_other:?}"
        ))),
    }
}

/// Encode a RetrievalStatus enum as its bare literal value.
fn csil_enc_retrieval_status(csil_v: &RetrievalStatus) -> CsilCborValue {
    match csil_v {
        RetrievalStatus::Complete => cbor_text("complete"),
        RetrievalStatus::Partial => cbor_text("partial"),
        RetrievalStatus::NoMatch => cbor_text("no_match"),
        RetrievalStatus::Unavailable => cbor_text("unavailable"),
        RetrievalStatus::Denied => cbor_text("denied"),
        RetrievalStatus::Cancelled => cbor_text("cancelled"),
    }
}

/// Decode a bare literal value into a RetrievalStatus enum.
fn csil_dec_retrieval_status(csil_v: &CsilCborValue) -> Result<RetrievalStatus, CsilCborError> {
    let csil_val = cbor_as_text(csil_v)?;
    match csil_val.as_str() {
        "complete" => Ok(RetrievalStatus::Complete),
        "partial" => Ok(RetrievalStatus::Partial),
        "no_match" => Ok(RetrievalStatus::NoMatch),
        "unavailable" => Ok(RetrievalStatus::Unavailable),
        "denied" => Ok(RetrievalStatus::Denied),
        "cancelled" => Ok(RetrievalStatus::Cancelled),
        csil_other => Err(CsilCborError(format!(
            "csil cbor: unknown RetrievalStatus value {csil_other:?}"
        ))),
    }
}

/// Encode a ErrorCode enum as its bare literal value.
fn csil_enc_error_code(csil_v: &ErrorCode) -> CsilCborValue {
    match csil_v {
        ErrorCode::Denied => cbor_text("denied"),
        ErrorCode::Conflict => cbor_text("conflict"),
        ErrorCode::Invalid => cbor_text("invalid"),
        ErrorCode::Unsupported => cbor_text("unsupported"),
        ErrorCode::Unavailable => cbor_text("unavailable"),
        ErrorCode::Limit => cbor_text("limit"),
    }
}

/// Decode a bare literal value into a ErrorCode enum.
fn csil_dec_error_code(csil_v: &CsilCborValue) -> Result<ErrorCode, CsilCborError> {
    let csil_val = cbor_as_text(csil_v)?;
    match csil_val.as_str() {
        "denied" => Ok(ErrorCode::Denied),
        "conflict" => Ok(ErrorCode::Conflict),
        "invalid" => Ok(ErrorCode::Invalid),
        "unsupported" => Ok(ErrorCode::Unsupported),
        "unavailable" => Ok(ErrorCode::Unavailable),
        "limit" => Ok(ErrorCode::Limit),
        csil_other => Err(CsilCborError(format!(
            "csil cbor: unknown ErrorCode value {csil_other:?}"
        ))),
    }
}

/// Encode a EvidenceRecord union as a tagged sum `[variant_index, value]`.
fn csil_enc_evidence_record(csil_v: &EvidenceRecord) -> CsilCborValue {
    match csil_v {
        EvidenceRecord::Variant0(csil_x) => CsilCborValue::Array(vec![
            CsilCborValue::Uint(0),
            csil_enc_interaction_record(csil_x),
        ]),
        EvidenceRecord::Variant1(csil_x) => CsilCborValue::Array(vec![
            CsilCborValue::Uint(1),
            csil_enc_interpretation_record(csil_x),
        ]),
        EvidenceRecord::Variant2(csil_x) => {
            CsilCborValue::Array(vec![CsilCborValue::Uint(2), csil_enc_session_input(csil_x)])
        }
        EvidenceRecord::Variant3(csil_x) => CsilCborValue::Array(vec![
            CsilCborValue::Uint(3),
            csil_enc_presentation_record(csil_x),
        ]),
        EvidenceRecord::Variant4(csil_x) => CsilCborValue::Array(vec![
            CsilCborValue::Uint(4),
            csil_enc_learning_assessment(csil_x),
        ]),
        EvidenceRecord::Variant5(csil_x) => CsilCborValue::Array(vec![
            CsilCborValue::Uint(5),
            csil_enc_source_context(csil_x),
        ]),
    }
}

/// Decode a tagged sum `[variant_index, value]` into a EvidenceRecord union.
fn csil_dec_evidence_record(csil_v: &CsilCborValue) -> Result<EvidenceRecord, CsilCborError> {
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
            let csil_decode = csil_dec_interaction_record;
            Ok(EvidenceRecord::Variant0(csil_decode(&csil_arr[1])?))
        }
        1 => {
            let csil_decode = csil_dec_interpretation_record;
            Ok(EvidenceRecord::Variant1(csil_decode(&csil_arr[1])?))
        }
        2 => {
            let csil_decode = csil_dec_session_input;
            Ok(EvidenceRecord::Variant2(csil_decode(&csil_arr[1])?))
        }
        3 => {
            let csil_decode = csil_dec_presentation_record;
            Ok(EvidenceRecord::Variant3(csil_decode(&csil_arr[1])?))
        }
        4 => {
            let csil_decode = csil_dec_learning_assessment;
            Ok(EvidenceRecord::Variant4(csil_decode(&csil_arr[1])?))
        }
        5 => {
            let csil_decode = csil_dec_source_context;
            Ok(EvidenceRecord::Variant5(csil_decode(&csil_arr[1])?))
        }
        csil_other => Err(CsilCborError(format!(
            "csil cbor: unknown EvidenceRecord variant {csil_other}"
        ))),
    }
}
