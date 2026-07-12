package auth

import "context"

// AdmissionEvidence is the only provider-admission state allowed to leave an
// adapter. It cannot carry tokens, cookies, claims, or authenticated clients.
type AdmissionEvidence struct {
	Policy    string `json:"policy"`
	Decision  string `json:"decision"`
	Subject   string `json:"subject,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Audited   bool   `json:"audited"`
}

type LoginCompletion struct {
	Identity  ExternalIdentity
	ReturnTo  string
	Admission *AdmissionEvidence
}

type admissionRequestIDKey struct{}

func WithAdmissionRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, admissionRequestIDKey{}, requestID)
}

func AdmissionRequestID(ctx context.Context) string {
	value, _ := ctx.Value(admissionRequestIDKey{}).(string)
	return value
}
