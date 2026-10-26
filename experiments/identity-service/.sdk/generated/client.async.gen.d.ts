import type { ActivateUserRequest, ActivateUserResponse, AddApplicationKeyRequest, AddApplicationKeyResponse, AddTrustedIssuerRequest, AddTrustedIssuerResponse, AdminIssueAttestationRequest, AdminIssueAttestationResponse, AdminUserClaimsRequest, AdminUserClaimsResponse, ApproveClaimRequest, ApproveClaimResponse, ApproveLocalRpRequest, ApproveLocalRpResponse, AuthenticateRequest, AuthenticateResponse, BrowserActAsCompleteRequest, BrowserActAsCompleteResponse, BrowserActAsInspectRequest, BrowserActAsInspectResponse, BrowserAuthorizationCompleteRequest, BrowserAuthorizationCompleteResponse, BrowserAuthorizationInspectRequest, BrowserAuthorizationInspectResponse, ChangePasswordRequest, ChangePasswordResponse, CheckPermissionRequest, CheckPermissionResponse, CheckResult, CompletePasswordRecoveryRequest, CompletePasswordRecoveryResponse, ConfirmContactVerificationRequest, ConfirmContactVerificationResponse, CreateGuestbookRequest, CreateProfileRequest, CreateProfileResponse, CreateUserRequest, CreateUserResponse, DeactivateUserRequest, DeactivateUserResponse, DeleteGuestbookRequest, DeleteGuestbookResponse, DenyLocalRpRequest, DenyLocalRpResponse, DepositClaimRequest, DepositClaimResponse, EmptyRequest, EnrollApplicationInstanceRequest, EnrollApplicationInstanceResponse, GetActAsGrantRevocationsRequest, GetActAsGrantRevocationsResponse, GetApplicationKeysRequest, GetApplicationKeysResponse, GetDomainKeysResponse, GetLocalRpPolicyRequest, GetLocalRpPolicyResponse, GetLocalRpRequest, GetLocalRpResponse, GetMyInfoResponse, GetNotificationCapabilitiesResponse, GetRevocationsRequest, GetRevocationsResponse, GetUiConfigurationResponse, GetUserAuthenticationActivityRequest, GetUserAuthenticationActivityResponse, GetUserKeysRequest, GetUserKeysResponse, GetUserRequest, GetUserResponse, GrantRelationRequest, GrantRelationResponse, GuestbookEntry, GuestbookListRequest, GuestbookListResponse, HandshakeRequest, HandshakeResponse, HelloRequest, HelloResponse, IntrospectBrowserSessionRequest, IntrospectBrowserSessionResponse, ListActAsGrantsResponse, ListClaimTypesResponse, ListLocalRpsRequest, ListLocalRpsResponse, ListLocalesResponse, ListPendingClaimApprovalsResponse, ListRelationsRequest, ListRelationsResponse, ListReleaseRulesResponse, ListSettablePoliciesResponse, ListTrustedIssuersResponse, ListUserClaimsRequest, ListUserClaimsResponse, ListUsersRequest, ListUsersResponse, ListVerifiedContactMethodsResponse, LocalRpTicketRedemptionResponse, PurgeLocalRpTicketsRequest, PurgeLocalRpTicketsResponse, PurgeUserRequest, PurgeUserResponse, RecheckPinsRequest, RecheckPinsResponse, RefreshActAsGrantRequest, RefreshActAsGrantResponse, RejectClaimRequest, RejectClaimResponse, RemoveClaimRequest, RemoveClaimResponse, RemoveClaimTypeLabelRequest, RemoveClaimTypeLabelResponse, RemoveClaimTypeRequest, RemoveClaimTypeResponse, RemoveCredentialRequest, RemoveCredentialResponse, RemoveMyClaimRequest, RemoveMyClaimResponse, RemoveRelationRequest, RemoveRelationResponse, RemoveReleaseRuleRequest, RemoveReleaseRuleResponse, RemoveTrustedIssuerRequest, RemoveTrustedIssuerResponse, RenewApplicationKeyAttestationRequest, RenewApplicationKeyAttestationResponse, RequestContactVerificationRequest, RequestContactVerificationResponse, RequestPasswordRecoveryRequest, RequestPasswordRecoveryResponse, RequestVerificationRequest, RequestVerificationResponse, ResetPasswordRequest, ResetPasswordResponse, RevokeActAsGrantRequest, RevokeActAsGrantResponse, RevokeApplicationKeyRequest, RevokeApplicationKeyResponse, RevokeDomainKeyRequest, RevokeDomainKeyResponse, RevokeLocalRpRequest, RevokeLocalRpResponse, RevokeVerifiedContactMethodRequest, RevokeVerifiedContactMethodResponse, RpActAsRefreshRequest, RpDecryptRequest, RpDecryptResponse, RpIssueAttestationRequest, RpIssueAttestationResponse, RpResolveActAsRevocationsRequest, RpResolveApplicationKeysRequest, RpResolveApplicationKeysResponse, RpResolveDomainKeysRequest, RpResolveDomainKeysResponse, RpSignRequest, RpSignResponse, RpUserInfoRequest, RpVerifyRequest, RpVerifyResponse, SessionCurrentResponse, SessionLogoutResponse, SessionPasswordLoginRequest, SessionPasswordLoginResponse, SetClaimRequest, SetClaimResponse, SetClaimTypeLabelRequest, SetClaimTypeLabelResponse, SetClaimTypeRequest, SetClaimTypeResponse, SetLocalRpPolicyRequest, SetLocalRpPolicyResponse, SetMyClaimRequest, SetMyClaimResponse, SetMyClaimSharingRequest, SetMyClaimSharingResponse, SetReleaseRuleRequest, SetReleaseRuleResponse, SetUserClaimRequest, SetUserClaimResponse, SignedLocalRpTicketRedemptionRequest, SignedUserInfoRequest, StartApplicationKeyChallengeRequest, StartApplicationKeyChallengeResponse, TranslationsRequest, TranslationsResponse, UpdateGuestbookRequest, UpdateUserRequest, UpdateUserResponse, UserInfo, ValidatePasswordRecoveryRequest, ValidatePasswordRecoveryResponse } from "./types.gen.ts";
export interface AsyncServiceTransport {
    call(service: string, op: string, req: Uint8Array): Promise<Uint8Array>;
}
export declare class AccountAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    changePassword(req: ChangePasswordRequest): Promise<ChangePasswordResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getMyInfo(req: EmptyRequest): Promise<GetMyInfoResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listSettablePolicies(req: EmptyRequest): Promise<ListSettablePoliciesResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setMyClaim(req: SetMyClaimRequest): Promise<SetMyClaimResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeMyClaim(req: RemoveMyClaimRequest): Promise<RemoveMyClaimResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setMyClaimSharing(req: SetMyClaimSharingRequest): Promise<SetMyClaimSharingResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    createProfile(req: CreateProfileRequest): Promise<CreateProfileResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    requestVerification(req: RequestVerificationRequest): Promise<RequestVerificationResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listVerifiedContactMethods(req: EmptyRequest): Promise<ListVerifiedContactMethodsResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    revokeVerifiedContactMethod(req: RevokeVerifiedContactMethodRequest): Promise<RevokeVerifiedContactMethodResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    requestContactVerification(req: RequestContactVerificationRequest): Promise<RequestContactVerificationResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    confirmContactVerification(req: ConfirmContactVerificationRequest): Promise<ConfirmContactVerificationResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    enrollApplicationInstance(req: EnrollApplicationInstanceRequest): Promise<EnrollApplicationInstanceResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listActAsGrants(req: EmptyRequest): Promise<ListActAsGrantsResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    revokeActAsGrant(req: RevokeActAsGrantRequest): Promise<RevokeActAsGrantResponse>;
}
export declare class ActAsAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    refreshGrant(req: RefreshActAsGrantRequest): Promise<RefreshActAsGrantResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getGrantRevocations(req: GetActAsGrantRevocationsRequest): Promise<GetActAsGrantRevocationsResponse>;
}
export declare class AdminAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listUsers(req: ListUsersRequest): Promise<ListUsersResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getUser(req: GetUserRequest): Promise<GetUserResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getUserAuthenticationActivity(req: GetUserAuthenticationActivityRequest): Promise<GetUserAuthenticationActivityResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    createUser(req: CreateUserRequest): Promise<CreateUserResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    updateUser(req: UpdateUserRequest): Promise<UpdateUserResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    deactivateUser(req: DeactivateUserRequest): Promise<DeactivateUserResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    activateUser(req: ActivateUserRequest): Promise<ActivateUserResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    purgeUser(req: PurgeUserRequest): Promise<PurgeUserResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    revokeDomainKey(req: RevokeDomainKeyRequest): Promise<RevokeDomainKeyResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    resetPassword(req: ResetPasswordRequest): Promise<ResetPasswordResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    authenticate(req: AuthenticateRequest): Promise<AuthenticateResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeCredential(req: RemoveCredentialRequest): Promise<RemoveCredentialResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setClaim(req: SetClaimRequest): Promise<SetClaimResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeClaim(req: RemoveClaimRequest): Promise<RemoveClaimResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listUserClaims(req: ListUserClaimsRequest): Promise<ListUserClaimsResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getUserClaims(req: AdminUserClaimsRequest): Promise<AdminUserClaimsResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setUserClaim(req: SetUserClaimRequest): Promise<SetUserClaimResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listSettablePolicies(req: EmptyRequest): Promise<ListSettablePoliciesResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listClaimTypes(req: EmptyRequest): Promise<ListClaimTypesResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setClaimType(req: SetClaimTypeRequest): Promise<SetClaimTypeResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeClaimType(req: RemoveClaimTypeRequest): Promise<RemoveClaimTypeResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setClaimTypeLabel(req: SetClaimTypeLabelRequest): Promise<SetClaimTypeLabelResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeClaimTypeLabel(req: RemoveClaimTypeLabelRequest): Promise<RemoveClaimTypeLabelResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listTrustedIssuers(req: EmptyRequest): Promise<ListTrustedIssuersResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    addTrustedIssuer(req: AddTrustedIssuerRequest): Promise<AddTrustedIssuerResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeTrustedIssuer(req: RemoveTrustedIssuerRequest): Promise<RemoveTrustedIssuerResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listReleaseRules(req: EmptyRequest): Promise<ListReleaseRulesResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setReleaseRule(req: SetReleaseRuleRequest): Promise<SetReleaseRuleResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeReleaseRule(req: RemoveReleaseRuleRequest): Promise<RemoveReleaseRuleResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listPendingClaimApprovals(req: EmptyRequest): Promise<ListPendingClaimApprovalsResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    approveClaim(req: ApproveClaimRequest): Promise<ApproveClaimResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    rejectClaim(req: RejectClaimRequest): Promise<RejectClaimResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    adminIssueAttestation(req: AdminIssueAttestationRequest): Promise<AdminIssueAttestationResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    grantRelation(req: GrantRelationRequest): Promise<GrantRelationResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    removeRelation(req: RemoveRelationRequest): Promise<RemoveRelationResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listRelations(req: ListRelationsRequest): Promise<ListRelationsResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    checkPermission(req: CheckPermissionRequest): Promise<CheckPermissionResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    recheckPins(req: RecheckPinsRequest): Promise<RecheckPinsResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listLocalRps(req: ListLocalRpsRequest): Promise<ListLocalRpsResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getLocalRp(req: GetLocalRpRequest): Promise<GetLocalRpResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    approveLocalRp(req: ApproveLocalRpRequest): Promise<ApproveLocalRpResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    denyLocalRp(req: DenyLocalRpRequest): Promise<DenyLocalRpResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    revokeLocalRp(req: RevokeLocalRpRequest): Promise<RevokeLocalRpResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getLocalRpPolicy(req: GetLocalRpPolicyRequest): Promise<GetLocalRpPolicyResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    setLocalRpPolicy(req: SetLocalRpPolicyRequest): Promise<SetLocalRpPolicyResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    purgeLocalRpTickets(req: PurgeLocalRpTicketsRequest): Promise<PurgeLocalRpTicketsResponse>;
}
export declare class ApplicationKeysAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getApplicationKeys(req: GetApplicationKeysRequest): Promise<GetApplicationKeysResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    startKeyChallenge(req: StartApplicationKeyChallengeRequest): Promise<StartApplicationKeyChallengeResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    addKey(req: AddApplicationKeyRequest): Promise<AddApplicationKeyResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    renewAttestation(req: RenewApplicationKeyAttestationRequest): Promise<RenewApplicationKeyAttestationResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    revokeKey(req: RevokeApplicationKeyRequest): Promise<RevokeApplicationKeyResponse>;
}
export declare class AttestationAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    depositClaim(req: DepositClaimRequest): Promise<DepositClaimResponse>;
}
export declare class BrowserAuthorizationAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    inspect(req: BrowserAuthorizationInspectRequest): Promise<BrowserAuthorizationInspectResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    complete(req: BrowserAuthorizationCompleteRequest): Promise<BrowserAuthorizationCompleteResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    inspectActAs(req: BrowserActAsInspectRequest): Promise<BrowserActAsInspectResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    completeActAs(req: BrowserActAsCompleteRequest): Promise<BrowserActAsCompleteResponse>;
}
export declare class DomainKeysAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getDomainKeys(req: EmptyRequest): Promise<GetDomainKeysResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getRevocations(req: GetRevocationsRequest): Promise<GetRevocationsResponse>;
}
export declare class GuestbookAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    createEntry(req: CreateGuestbookRequest): Promise<GuestbookEntry>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listEntries(req: GuestbookListRequest): Promise<GuestbookListResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    updateEntry(req: UpdateGuestbookRequest): Promise<GuestbookEntry>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    deleteEntry(req: DeleteGuestbookRequest): Promise<DeleteGuestbookResponse>;
}
export declare class HandshakeAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    handshake(req: HandshakeRequest): Promise<HandshakeResponse>;
}
export declare class HelloAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    hello(req: HelloRequest): Promise<HelloResponse>;
}
export declare class I18nAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getTranslations(req: TranslationsRequest): Promise<TranslationsResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    listLocales(req: EmptyRequest): Promise<ListLocalesResponse>;
}
export declare class IdentityAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getUserInfo(req: SignedUserInfoRequest): Promise<UserInfo>;
}
export declare class LocalRpAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    redeemClaimTicket(req: SignedLocalRpTicketRedemptionRequest): Promise<LocalRpTicketRedemptionResponse>;
}
export declare class NotificationAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getCapabilities(req: EmptyRequest): Promise<GetNotificationCapabilitiesResponse>;
}
export declare class OpsAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    healthcheck(req: EmptyRequest): Promise<CheckResult>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    readiness(req: EmptyRequest): Promise<CheckResult>;
}
export declare class RecoveryAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    requestPasswordRecovery(req: RequestPasswordRecoveryRequest): Promise<RequestPasswordRecoveryResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    validatePasswordRecovery(req: ValidatePasswordRecoveryRequest): Promise<ValidatePasswordRecoveryResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    completePasswordRecovery(req: CompletePasswordRecoveryRequest): Promise<CompletePasswordRecoveryResponse>;
}
export declare class RpAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    signRequest(req: RpSignRequest): Promise<RpSignResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    decryptToken(req: RpDecryptRequest): Promise<RpDecryptResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    verifyAssertion(req: RpVerifyRequest): Promise<RpVerifyResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    userinfoFetch(req: RpUserInfoRequest): Promise<UserInfo>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    issueAttestation(req: RpIssueAttestationRequest): Promise<RpIssueAttestationResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    resolveDomainKeys(req: RpResolveDomainKeysRequest): Promise<RpResolveDomainKeysResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    resolveApplicationKeys(req: RpResolveApplicationKeysRequest): Promise<RpResolveApplicationKeysResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    actAsRefreshGrant(req: RpActAsRefreshRequest): Promise<RefreshActAsGrantResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    resolveActAsRevocations(req: RpResolveActAsRevocationsRequest): Promise<GetActAsGrantRevocationsResponse>;
}
export declare class SessionAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    loginPassword(req: SessionPasswordLoginRequest): Promise<SessionPasswordLoginResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getCurrent(req: EmptyRequest): Promise<SessionCurrentResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    logout(req: EmptyRequest): Promise<SessionLogoutResponse>;
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    introspect(req: IntrospectBrowserSessionRequest): Promise<IntrospectBrowserSessionResponse>;
}
export declare class UiAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getConfiguration(req: EmptyRequest): Promise<GetUiConfigurationResponse>;
}
export declare class UserKeysAsyncClient {
    private readonly t;
    constructor(t: AsyncServiceTransport);
    /**
     * @throws {ServiceError} when the API returns an error response
     * @throws transport errors (network, timeout) raised by the transport
     */
    getUserKeys(req: GetUserKeysRequest): Promise<GetUserKeysResponse>;
}
export declare class AsyncApiClient {
    readonly account: AccountAsyncClient;
    readonly actAs: ActAsAsyncClient;
    readonly admin: AdminAsyncClient;
    readonly applicationKeys: ApplicationKeysAsyncClient;
    readonly attestation: AttestationAsyncClient;
    readonly browserAuthorization: BrowserAuthorizationAsyncClient;
    readonly domainKeys: DomainKeysAsyncClient;
    readonly guestbook: GuestbookAsyncClient;
    readonly handshake: HandshakeAsyncClient;
    readonly hello: HelloAsyncClient;
    readonly i18n: I18nAsyncClient;
    readonly identity: IdentityAsyncClient;
    readonly localRp: LocalRpAsyncClient;
    readonly notification: NotificationAsyncClient;
    readonly ops: OpsAsyncClient;
    readonly recovery: RecoveryAsyncClient;
    readonly rp: RpAsyncClient;
    readonly session: SessionAsyncClient;
    readonly ui: UiAsyncClient;
    readonly userKeys: UserKeysAsyncClient;
    constructor(t: AsyncServiceTransport);
}
