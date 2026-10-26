//! Generated types from CSIL specification

#![allow(non_camel_case_types, clippy::large_enum_variant)]

#[derive(Debug, Clone, PartialEq)]
pub struct IdentityRef {
    pub domain: String,
    pub account_id: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct SecretBinding {
    pub authority: IdentityRef,
    pub binding_id: String,
    pub path: String,
    pub key: String,
    pub session_id: String,
    pub issuance_id: Option<String>,
}

/// Encoding variants
#[derive(Debug, Clone, PartialEq)]
pub enum Encoding {
    Raw,
    Base64,
    JsonString,
    UrlQuery,
}

#[derive(Debug, Clone, PartialEq)]
pub struct SecretCandidate {
    pub binding: SecretBinding,
    pub encoding: Encoding,
}

#[derive(Debug, Clone, PartialEq)]
pub struct LiteralSegment {
    pub start: u64,
    pub value: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct SecretSegment {
    pub start: u64,
    pub end: u64,
    pub candidate: SecretCandidate,
}

#[derive(Debug, Clone, PartialEq)]
pub struct AmbiguousSegment {
    pub start: u64,
    pub end: u64,
    pub candidates: Vec<SecretCandidate>,
    pub reason: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GapSegment {
    pub start: u64,
    pub end: u64,
    pub reason: String,
}

/// Segment variants
#[derive(Debug, Clone, PartialEq)]
pub enum Segment {
    Variant0(LiteralSegment),
    Variant1(SecretSegment),
    Variant2(AmbiguousSegment),
    Variant3(GapSegment),
}

#[derive(Debug, Clone, PartialEq)]
pub struct CaptureBatch {
    pub schema_version: u64,
    pub attempt_id: String,
    pub stream_id: String,
    pub sequence: u64,
    pub segments: Vec<Segment>,
    pub r#final: bool,
    pub complete: bool,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CaptureReceipt {
    pub attempt_id: String,
    pub stream_id: String,
    pub sequence: u64,
    pub digest: Vec<u8>,
}
