//! Generated transport-agnostic service clients from CSIL specification

use super::codec::*;
use super::types::*;

/// Error from a generated client call: a structured error the service returned,
/// or a transport-level failure. The caller-supplied `Transport` decides how an
/// error response maps onto `Service`.
#[derive(Debug, Clone)]
pub enum ClientError {
    Service { code: i64, message: String },
    Transport(String),
}

impl std::fmt::Display for ClientError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ClientError::Service { code, message } => write!(f, "service error {code}: {message}"),
            ClientError::Transport(msg) => write!(f, "transport error: {msg}"),
        }
    }
}

impl std::error::Error for ClientError {}

/// The caller-supplied byte carrier: it performs the call named by `(service, op)`
/// with the already-encoded request bytes and returns the response bytes, or an
/// error. The generated client owns (de)serialization via the codec; the carrier
/// only moves bytes, so it can be HTTP, a queue, or an in-process loop.
pub trait Transport {
    fn call(&self, service: &str, op: &str, req: &[u8]) -> Result<Vec<u8>, ClientError>;
}

/// Typed client for the Interaction service.
pub struct InteractionClient<T: Transport> {
    #[allow(dead_code)]
    transport: T,
}

impl<T: Transport> InteractionClient<T> {
    pub fn new(transport: T) -> Self {
        Self { transport }
    }

    /// submit-input (request/response).
    pub fn submit_input(&self, req: SubmitInputRequest) -> Result<Receipt, ClientError> {
        let csil_resp = self.transport.call(
            "Interaction",
            "submit-input",
            &encode_submit_input_request(&req),
        )?;
        decode_receipt(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }

    /// correct-association (request/response).
    pub fn correct_association(
        &self,
        req: CorrectAssociationRequest,
    ) -> Result<Receipt, ClientError> {
        let csil_resp = self.transport.call(
            "Interaction",
            "correct-association",
            &encode_correct_association_request(&req),
        )?;
        decode_receipt(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }
}

/// Typed client for the Evidence service.
pub struct EvidenceClient<T: Transport> {
    #[allow(dead_code)]
    transport: T,
}

impl<T: Transport> EvidenceClient<T> {
    pub fn new(transport: T) -> Self {
        Self { transport }
    }

    /// read-history (request/response).
    pub fn read_history(&self, req: ReadHistoryRequest) -> Result<HistoryPage, ClientError> {
        let csil_resp = self.transport.call(
            "Evidence",
            "read-history",
            &encode_read_history_request(&req),
        )?;
        decode_history_page(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }
}

/// Typed client for the Knowledge service.
pub struct KnowledgeClient<T: Transport> {
    #[allow(dead_code)]
    transport: T,
}

impl<T: Transport> KnowledgeClient<T> {
    pub fn new(transport: T) -> Self {
        Self { transport }
    }

    /// query (request/response).
    pub fn query(&self, req: KnowledgeQuery) -> Result<QueryResult, ClientError> {
        let csil_resp = self
            .transport
            .call("Knowledge", "query", &encode_knowledge_query(&req))?;
        decode_query_result(&csil_resp).map_err(|e| ClientError::Transport(e.to_string()))
    }
}
