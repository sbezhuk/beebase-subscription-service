package subscription

import "strings"

// VerifyRequest is the body of POST /api/v1/subscription/verify.
type VerifyRequest struct {
	Provider          string `json:"provider"`
	SignedTransaction string `json:"signedTransaction,omitempty"`
	PurchaseToken     string `json:"purchaseToken,omitempty"`
	SubscriptionID    string `json:"subscriptionId,omitempty"`
	PackageName       string `json:"packageName,omitempty"`
}

// Validate returns a map of field name -> error code.
func (r *VerifyRequest) Validate() map[string]string {
	fields := map[string]string{}
	provider := strings.ToLower(r.Provider)
	if strings.TrimSpace(r.Provider) == "" {
		fields["provider"] = "provider_required"
		return fields
	}
	if provider != "apple" && provider != "google" {
		fields["provider"] = "unsupported_provider"
		return fields
	}
	switch provider {
	case "apple":
		if strings.TrimSpace(r.SignedTransaction) == "" {
			fields["signedTransaction"] = "signed_transaction_required"
		}
	case "google":
		if strings.TrimSpace(r.PurchaseToken) == "" {
			fields["purchaseToken"] = "purchase_token_required"
		}
	}
	return fields
}

// RestoreRequest is the body of POST /api/v1/subscription/restore.
type RestoreRequest struct {
	Provider          string `json:"provider"`
	SignedTransaction string `json:"signedTransaction,omitempty"`
	PurchaseToken     string `json:"purchaseToken,omitempty"`
	SubscriptionID    string `json:"subscriptionId,omitempty"`
	PackageName       string `json:"packageName,omitempty"`
}

// Validate returns a map of field name -> error code.
func (r *RestoreRequest) Validate() map[string]string {
	v := r.toVerifyRequest()
	return v.Validate()
}

func (r *RestoreRequest) toVerifyRequest() VerifyRequest {
	return VerifyRequest{
		Provider:          r.Provider,
		SignedTransaction: r.SignedTransaction,
		PurchaseToken:     r.PurchaseToken,
		SubscriptionID:    r.SubscriptionID,
		PackageName:       r.PackageName,
	}
}
