package model

import (
	"strings"
	"testing"
)

func TestUnpurchasedModelIsNotARejectedKey(t *testing.T) {
	err := apiErr(403, "AccessDenied.Unpurchased: The model service is not activated. Activate the model in Model Studio.")
	if got := ClassifyError(err); got != ErrCategoryQuota {
		t.Fatalf("category=%v", got)
	}
	got := FriendlyAPIError(err, "alibaba", "glm-5.2")
	if !strings.Contains(got, "enable access") || !strings.Contains(got, "/model") || strings.Contains(got, "key") || strings.Contains(got, "no credit left") {
		t.Fatalf("misleading or unactionable message: %s", got)
	}
	if got := ClassifyError(apiErr(403, "AccessDenied: API key is invalid")); got != ErrCategoryAuth {
		t.Fatalf("generic 403 category=%v", got)
	}
}
