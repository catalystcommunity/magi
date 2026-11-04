//! Generated types from CSIL specification

#![allow(non_camel_case_types, clippy::large_enum_variant)]

#[derive(Debug, Clone, PartialEq)]
pub struct EgressHeader {
    pub name: String,
    pub value: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct FlowRecord {
    pub id: String,
    pub environment: String,
    pub owner: String,
    pub destination: String,
    pub method: Option<String>,
    pub url: Option<String>,
    pub status: Option<u64>,
    pub outcome: String,
    pub reasons: Vec<String>,
    pub plugins: Vec<String>,
    pub secrets_used: Vec<String>,
    pub redacted: u64,
    pub created_ms: u64,
    pub duration_ms: u64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListFlowsRequest {
    pub environment: Option<String>,
    pub owner: Option<String>,
    pub limit: Option<u64>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListFlowsResponse {
    pub flows: Vec<FlowRecord>,
}
