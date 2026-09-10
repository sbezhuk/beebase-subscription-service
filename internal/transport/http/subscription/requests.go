package subscription

import "strings"

// VerifyRequest is the body of POST /api/v1/subscription/verify.
type VerifyRequest struct {
	Provider          string `json:"provider"`
	SignedTransaction string `json:"signed_transaction,omitempty"`
	PurchaseToken     string `json:"purchase_token,omitempty"`
	SubscriptionID    string `json:"subscription_id,omitempty"`
	PackageName       string `json:"package_name,omitempty"`
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
			fields["signed_transaction"] = "signed_transaction_required"
		}
	case "google":
		if strings.TrimSpace(r.PurchaseToken) == "" {
			fields["purchase_token"] = "purchase_token_required"
		}
	}
	return fields
}

// RestoreRequest is the body of POST /api/v1/subscription/restore.
type RestoreRequest struct {
	Provider          string `json:"provider"`
	SignedTransaction string `json:"signed_transaction,omitempty"`
	PurchaseToken     string `json:"purchase_token,omitempty"`
	SubscriptionID    string `json:"subscription_id,omitempty"`
	PackageName       string `json:"package_name,omitempty"`
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
