//! Generated service traits from CSIL specification

use super::types::*;

/// Interaction service trait
pub trait Interaction {
    type Context;
    /// submit-input (request/response).
    fn submit_input(
        &self,
        ctx: &Self::Context,
        input: SubmitInputRequest,
    ) -> Result<Receipt, ServiceError>;
    /// correct-association (request/response).
    fn correct_association(
        &self,
        ctx: &Self::Context,
        input: CorrectAssociationRequest,
    ) -> Result<Receipt, ServiceError>;
}

/// Wire-id ordinals for the Interaction service (transport compact profiles).
pub mod interaction_wire_ids {
    pub const SERVICE: u64 = 20;
    pub const OP_SUBMIT_INPUT: u64 = 0;
    pub const OP_CORRECT_ASSOCIATION: u64 = 1;
}

/// Evidence service trait
pub trait Evidence {
    type Context;
    /// read-history (request/response).
    fn read_history(
        &self,
        ctx: &Self::Context,
        input: ReadHistoryRequest,
    ) -> Result<HistoryPage, ServiceError>;
}

/// Wire-id ordinals for the Evidence service (transport compact profiles).
pub mod evidence_wire_ids {
    pub const SERVICE: u64 = 21;
    pub const OP_READ_HISTORY: u64 = 0;
}

/// Knowledge service trait
pub trait Knowledge {
    type Context;
    /// query (request/response).
    fn query(
        &self,
        ctx: &Self::Context,
        input: KnowledgeQuery,
    ) -> Result<QueryResult, ServiceError>;
}

/// Wire-id ordinals for the Knowledge service (transport compact profiles).
pub mod knowledge_wire_ids {
    pub const SERVICE: u64 = 22;
    pub const OP_QUERY: u64 = 0;
}
