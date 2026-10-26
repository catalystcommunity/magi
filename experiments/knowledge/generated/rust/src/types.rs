//! Generated types from CSIL specification

#![allow(non_camel_case_types, clippy::large_enum_variant)]

/// Exact base-10 decimal carried on the wire as a CBOR tag-4 decimal fraction:
/// the two-element array `[exponent, mantissa]`, value = mantissa * 10^exponent.
/// Stored as that exact pair so no precision is lost. Convert to/from a decimal
/// library (e.g. `rust_decimal::Decimal`) through `as_str` / `from_str`, which is
/// why this type needs no such dependency of its own.
///
/// Equality and ordering are by *value*, not by representation: `(-2, 0)` ("0.00")
/// and `(-1, 0)` ("0.0") are equal, and the comparison honours the exact base-10
/// magnitude after normalizing differing exponents. This keeps `.eq`/`.ne` and the
/// `.ge/.le/.gt/.lt` validation bounds correct regardless of how a value was scaled.
///
/// On the wire it encodes as a CBOR **tag 4** decimal fraction — the two-element
/// array `[exponent, mantissa]` — so it interoperates byte-for-byte with the Go,
/// TypeScript, and Python generators. The generated `codec.gen.rs` owns that wire
/// form directly (reading/writing `exponent` and `mantissa` against its own value
/// model), so this type carries no serde impl and the crate needs no CBOR library.
#[derive(Debug, Clone, Copy)]
pub struct CsilDecimal {
    pub exponent: i64,
    pub mantissa: i128,
}

impl PartialEq for CsilDecimal {
    fn eq(&self, other: &Self) -> bool {
        self.cmp(other) == std::cmp::Ordering::Equal
    }
}

impl Eq for CsilDecimal {}

impl PartialOrd for CsilDecimal {
    fn partial_cmp(&self, other: &Self) -> Option<std::cmp::Ordering> {
        Some(self.cmp(other))
    }
}

impl Ord for CsilDecimal {
    fn cmp(&self, other: &Self) -> std::cmp::Ordering {
        use std::cmp::Ordering;
        let sign_a = self.mantissa.signum();
        let sign_b = other.mantissa.signum();
        if sign_a != sign_b {
            return sign_a.cmp(&sign_b);
        }
        if sign_a == 0 {
            return Ordering::Equal;
        }
        // Same nonzero sign: compare absolute magnitudes, then flip for negatives.
        let magnitude = Self::cmp_magnitude(
            self.mantissa.unsigned_abs(),
            self.exponent,
            other.mantissa.unsigned_abs(),
            other.exponent,
        );
        if sign_a < 0 {
            magnitude.reverse()
        } else {
            magnitude
        }
    }
}

impl CsilDecimal {
    /// Order two positive magnitudes `ma * 10^ea` and `mb * 10^eb` without ever
    /// scaling a mantissa (so no overflow): after stripping trailing zeros each
    /// value lands in the decade `[10^(w-1), 10^w)` for `w = digits + exponent`, so
    /// differing weights settle the order outright and equal weights reduce to a
    /// trailing-zero-padded digit-string compare.
    fn cmp_magnitude(mut ma: u128, mut ea: i64, mut mb: u128, mut eb: i64) -> std::cmp::Ordering {
        while ma.is_multiple_of(10) {
            ma /= 10;
            ea += 1;
        }
        while mb.is_multiple_of(10) {
            mb /= 10;
            eb += 1;
        }
        let digits_a = ma.to_string();
        let digits_b = mb.to_string();
        let weight_a = digits_a.len() as i64 + ea;
        let weight_b = digits_b.len() as i64 + eb;
        if weight_a != weight_b {
            return weight_a.cmp(&weight_b);
        }
        let width = digits_a.len().max(digits_b.len());
        let padded_a = format!("{digits_a:0<width$}");
        let padded_b = format!("{digits_b:0<width$}");
        padded_a.cmp(&padded_b)
    }
}

impl From<CsilDecimal> for (i64, i128) {
    fn from(d: CsilDecimal) -> Self {
        (d.exponent, d.mantissa)
    }
}

impl From<(i64, i128)> for CsilDecimal {
    fn from((exponent, mantissa): (i64, i128)) -> Self {
        Self { exponent, mantissa }
    }
}

#[allow(clippy::should_implement_trait, clippy::wrong_self_convention)]
impl CsilDecimal {
    pub fn new(exponent: i64, mantissa: i128) -> Self {
        Self { exponent, mantissa }
    }

    /// Canonical decimal string for the exact value: `(-2, 12345)` renders as
    /// `123.45`. Round-trips through `from_str` by value.
    pub fn as_str(&self) -> String {
        let digits = self.mantissa.unsigned_abs().to_string();
        let body = if self.exponent >= 0 {
            let mut out = digits;
            out.push_str(&"0".repeat(self.exponent as usize));
            out
        } else {
            let scale = (-self.exponent) as usize;
            if digits.len() > scale {
                let point = digits.len() - scale;
                format!("{}.{}", &digits[..point], &digits[point..])
            } else {
                let pad = "0".repeat(scale - digits.len());
                format!("0.{pad}{digits}")
            }
        };
        if self.mantissa < 0 {
            format!("-{body}")
        } else {
            body
        }
    }

    /// Parse a decimal string into the exact `(exponent, mantissa)` pair.
    pub fn from_str(s: &str) -> Result<Self, String> {
        let t = s.trim();
        let (negative, rest) = match t.strip_prefix('-') {
            Some(r) => (true, r),
            None => (false, t.strip_prefix('+').unwrap_or(t)),
        };
        let (int_part, frac_part) = match rest.split_once('.') {
            Some((i, f)) => (i, f),
            None => (rest, ""),
        };
        if frac_part.contains('.') || (int_part.is_empty() && frac_part.is_empty()) {
            return Err(format!("invalid decimal: {s}"));
        }
        let mut digits = String::with_capacity(int_part.len() + frac_part.len());
        digits.push_str(int_part);
        digits.push_str(frac_part);
        if digits.is_empty() || !digits.bytes().all(|b| b.is_ascii_digit()) {
            return Err(format!("invalid decimal: {s}"));
        }
        let magnitude: i128 = digits
            .parse()
            .map_err(|e| format!("decimal out of range: {e}"))?;
        let mantissa = if negative { -magnitude } else { magnitude };
        Ok(Self {
            exponent: -(frac_part.len() as i64),
            mantissa,
        })
    }
}

impl std::fmt::Display for CsilDecimal {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.as_str())
    }
}

#[derive(Debug, Clone, PartialEq)]
pub struct IdentityRef {
    pub domain: String,
    pub account_id: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct RecordRef {
    pub authority: IdentityRef,
    pub kind: String,
    pub record_id: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ScopeRef {
    pub authority: IdentityRef,
    pub scope_id: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct SchemaRef {
    pub name: String,
    pub version: u64,
    pub digest: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ArtifactRef {
    pub authority: IdentityRef,
    pub object_id: String,
    pub digest: Vec<u8>,
    pub size: u64,
    pub media_type: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ContextAccess {
    pub actor: IdentityRef,
    pub control_scope: ScopeRef,
    pub purpose: String,
    pub processor: IdentityRef,
    pub represented_identity: Option<IdentityRef>,
    pub persona: Option<IdentityRef>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Restrictions {
    pub required_scopes: Vec<ScopeRef>,
    pub permitted_purposes: Vec<String>,
}

/// CaptureStatus variants
#[derive(Debug, Clone, PartialEq)]
pub enum CaptureStatus {
    Complete,
    Incomplete,
    Unknown,
}

/// Channel variants
#[derive(Debug, Clone, PartialEq)]
pub enum Channel {
    Text,
    Voice,
    System,
}

/// PresentationState variants
#[derive(Debug, Clone, PartialEq)]
pub enum PresentationState {
    Queued,
    Started,
    Interrupted,
    Completed,
}

/// RetrievalStatus variants
#[derive(Debug, Clone, PartialEq)]
pub enum RetrievalStatus {
    Complete,
    Partial,
    NoMatch,
    Unavailable,
    Denied,
    Cancelled,
}

/// ErrorCode variants
#[derive(Debug, Clone, PartialEq)]
pub enum ErrorCode {
    Denied,
    Conflict,
    Invalid,
    Unsupported,
    Unavailable,
    Limit,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ServiceError {
    pub code: ErrorCode,
    pub message: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct EvidenceHeader {
    pub r#ref: RecordRef,
    pub producer: IdentityRef,
    pub received_at: chrono::DateTime<chrono::Utc>,
    pub observed_at: Option<chrono::DateTime<chrono::Utc>>,
    pub stream_id: String,
    pub source_sequence: u64,
    pub restrictions: Restrictions,
    pub causes: Vec<RecordRef>,
    pub capture: CaptureStatus,
    pub represented_identity: Option<IdentityRef>,
    pub persona: Option<IdentityRef>,
    pub agent_id: Option<String>,
    pub attempt_id: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct InteractionRecord {
    pub header: EvidenceHeader,
    pub point_id: String,
    pub channel: Channel,
    pub content: String,
    pub media: Option<ArtifactRef>,
    pub conversation_ids: Vec<String>,
    pub reply_hints: Vec<RecordRef>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct DestinationMeaning {
    pub session_id: String,
    pub prompt_refs: Vec<RecordRef>,
    pub content: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct InterpretationRecord {
    pub header: EvidenceHeader,
    pub sources: Vec<RecordRef>,
    pub context_refs: Vec<RecordRef>,
    pub destinations: Vec<DestinationMeaning>,
    pub method: String,
    pub uncertainty: String,
    pub prior: Option<RecordRef>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct SessionInput {
    pub header: EvidenceHeader,
    pub session_id: String,
    pub interpretation: RecordRef,
    pub sources: Vec<RecordRef>,
    pub content: String,
    pub command_key: String,
    pub policy_revision: u64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct PresentationRecord {
    pub header: EvidenceHeader,
    pub output: RecordRef,
    pub point_id: String,
    pub state: PresentationState,
}

#[derive(Debug, Clone, PartialEq)]
pub struct TypedDimension {
    pub schema: SchemaRef,
    pub payload: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct LearningAssessment {
    pub header: EvidenceHeader,
    pub inputs: Vec<RecordRef>,
    pub method: String,
    pub supporting: Vec<RecordRef>,
    pub contrary: Vec<RecordRef>,
    pub prior: Option<RecordRef>,
    pub dimensions: Option<Vec<TypedDimension>>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct SourceContext {
    pub header: EvidenceHeader,
    pub resource_id: String,
    pub adapter: String,
    pub origin_revision: Option<String>,
    pub author: Option<IdentityRef>,
    pub content: String,
    pub enrichment_allowed: bool,
    pub artifact: Option<ArtifactRef>,
}

/// EvidenceRecord variants
#[derive(Debug, Clone, PartialEq)]
pub enum EvidenceRecord {
    Variant0(InteractionRecord),
    Variant1(InterpretationRecord),
    Variant2(SessionInput),
    Variant3(PresentationRecord),
    Variant4(LearningAssessment),
    Variant5(SourceContext),
}

#[derive(Debug, Clone, PartialEq)]
pub struct Receipt {
    pub record: RecordRef,
    pub command_key: String,
    pub accepted_at: chrono::DateTime<chrono::Utc>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct DeliveryReceipt {
    pub input: RecordRef,
    pub destination: String,
    pub received_at: chrono::DateTime<chrono::Utc>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ContextUse {
    pub input: RecordRef,
    pub context_id: String,
    pub used_at: chrono::DateTime<chrono::Utc>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct HistoryPage {
    pub records: Vec<EvidenceRecord>,
    pub next_cursor: Option<Vec<u8>>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Quantity {
    pub name: String,
    pub value: CsilDecimal,
    pub unit: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct KnowledgeQuery {
    pub question: String,
    pub purpose: String,
    pub limit: u64,
    pub after: Option<chrono::DateTime<chrono::Utc>>,
    pub before: Option<chrono::DateTime<chrono::Utc>>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct QueryMatch {
    pub source: RecordRef,
    pub content: String,
    pub observed_at: chrono::DateTime<chrono::Utc>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct QueryResult {
    pub status: RetrievalStatus,
    pub matches: Vec<QueryMatch>,
    pub gaps: Vec<String>,
    pub quantities: Option<Vec<Quantity>>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct SubmitInputRequest {
    pub command_key: String,
    pub interaction: InteractionRecord,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CorrectAssociationRequest {
    pub command_key: String,
    pub expected_revision: u64,
    pub interpretation: InterpretationRecord,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ReadHistoryRequest {
    pub root: RecordRef,
    pub limit: u64,
    pub after_cursor: Option<Vec<u8>>,
}
