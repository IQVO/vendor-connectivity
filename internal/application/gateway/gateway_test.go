package gateway_test

import (
	"errors"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
)

func TestCodeFromProblemType(t *testing.T) {
	tests := map[string]string{
		"stale-profile-revision": "ErrStaleProfileRevision",
		"https://retailnetwork.example/problems/vendor-node-code-taken": "ErrVendorNodeCodeTaken",
		"urn:problem#feed-already-applied":                              "ErrFeedAlreadyApplied",
		"not-found":                                                     "ErrNotFound",
		"validation_failed":                                             "ErrValidationFailed",
		"a b!c":                                                         "ErrABc",
		"x9-y":                                                          "ErrX9Y",
		"":                                                              "ErrRefused",
		"--":                                                            "ErrRefused",
		"https://x/":                                                    "ErrRefused",
	}
	for in, want := range tests {
		if got := gateway.CodeFromProblemType(in); got != want {
			t.Errorf("CodeFromProblemType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRefusalIsAnError(t *testing.T) {
	var err error = &gateway.Refusal{Status: 409, Code: "ErrX", Message: "taken"}
	var ref *gateway.Refusal
	if !errors.As(err, &ref) || ref.Code != "ErrX" {
		t.Fatal("a refusal unwraps")
	}
	if err.Error() != "neighbour refused (409 ErrX): taken" {
		t.Fatalf("message = %q", err.Error())
	}
}
