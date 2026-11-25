//! Generated types from CSIL specification

#![allow(clippy::large_enum_variant)]

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

#[derive(Debug, Clone, PartialEq)]
pub struct DatabaseInstance {
    pub name: String,
    pub role: String,
    pub ready: bool,
    pub restarts: u64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct DatabaseIncident {
    pub seq: u64,
    pub kind: String,
    pub instance: String,
    pub message: String,
    pub at_ms: u64,
}

#[derive(Debug, Clone, PartialEq)]
pub struct DatabaseStatusRequest {
    pub cluster: Option<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct DatabaseStatusResponse {
    pub watched: bool,
    pub instances: Vec<DatabaseInstance>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListIncidentsRequest {
    pub after: Option<u64>,
    pub limit: Option<u64>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListIncidentsResponse {
    pub incidents: Vec<DatabaseIncident>,
}
