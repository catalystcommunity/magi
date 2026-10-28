//! Generated types from CSIL specification

#![allow(non_camel_case_types, clippy::large_enum_variant)]

#[derive(Debug, Clone, PartialEq)]
pub struct Target {
    pub name: String,
    pub namespace: String,
    pub deployment: String,
    pub container: String,
    pub health_path: Option<String>,
    pub health_port: Option<u64>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct TargetImage {
    pub target: String,
    pub image: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ReleaseItem {
    pub target: String,
    pub image: String,
    pub previous_image: String,
    pub status: String,
    pub message: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Release {
    pub id: String,
    pub owner: String,
    pub status: String,
    pub reason: Option<String>,
    pub items: Vec<ReleaseItem>,
    pub created_ms: u64,
    pub updated_ms: u64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct CreateReleaseRequest {
    pub images: Vec<TargetImage>,
    pub reason: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct GetReleaseRequest {
    pub id: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListReleasesRequest {
    pub limit: Option<u64>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListReleasesResponse {
    pub releases: Vec<Release>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct RollbackRequest {
    pub id: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListTargetsRequest {
    pub name: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListTargetsResponse {
    pub targets: Vec<Target>,
}
