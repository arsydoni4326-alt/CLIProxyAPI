// Package main demonstrates how to embed request authentication
// (access control) into a host process using the @sdk/access package.
//
// It shows how to:
//   - Implement a custom access Provider (Identifier + Authenticate)
//   - Register providers globally and snapshot them into a Manager
//   - Chain providers: a not-handled result falls through to the next provider
//   - Aggregate no-credentials / invalid-credential results
//   - Inspect Result metadata for logging and auditing
//
// The example is self-contained and offline: it runs without a server,
// config file, or network, and exits non-zero if any assertion fails,
// so `go run ./examples/access-hook` doubles as a CI smoke test.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

// customToken validates the X-Custom header.
//
// This mirrors the built-in `config-api-key` provider pattern: read a
// credential from the request, return NotHandled when the source is not
// recognized, InvalidCredential when the credential is wrong, and a
// Result when authentication succeeds.
type customToken struct{}

func (customToken) Identifier() string { return "custom-auth" }

func (customToken) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	token := strings.TrimSpace(r.Header.Get("X-Custom"))
	if token == "" {
		// No recognizable credential → let the next provider try.
		return nil, sdkaccess.NewNotHandledError()
	}
	if token != "s3cret" {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	return &sdkaccess.Result{
		Provider:  "custom-auth",
		Principal: "service-user",
		// Metadata is surfaced to logs and downstream auditing.
		Metadata: map[string]string{"source": "x-custom"},
	}, nil
}

// partnerToken is a second provider in the chain. It handles requests that
// carry the X-Partner header and falls through otherwise, demonstrating
// provider chaining: custom-auth → partner-token.
type partnerToken struct{}

func (partnerToken) Identifier() string { return "partner-token" }

func (partnerToken) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	token := strings.TrimSpace(r.Header.Get("X-Partner"))
	if token == "" {
		return nil, sdkaccess.NewNotHandledError()
	}
	if token != "partner-key" {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	return &sdkaccess.Result{
		Provider:  "partner-token",
		Principal: "partner-account",
		Metadata:  map[string]string{"source": "x-partner"},
	}, nil
}

// init registers both providers into the global registry. Registration is
// process-global and preserves first-seen order. In a real embedding, custom
// providers shipped as separate Go modules are typically pulled in via blank
// imports so their init() registers them here.
func init() {
	sdkaccess.RegisterProvider("custom-auth", customToken{})
	sdkaccess.RegisterProvider("partner-token", partnerToken{})
}

func main() {
	// Snapshot the global registry into a Manager. The Manager walks the
	// providers in order and returns on the first success.
	manager := sdkaccess.NewManager()
	manager.SetProviders(sdkaccess.RegisteredProviders())

	// The integration point with the CLI service is documented here:
	//
	//	coreCfg, _ := config.LoadConfig("config.yaml")
	//	accessManager := sdkaccess.NewManager()
	//	svc, _ := cliproxy.NewBuilder().
	//		WithConfig(coreCfg).
	//		WithConfigPath("config.yaml").
	//		WithRequestAccessManager(accessManager).
	//		Build()
	//
	// Register custom providers before Build() so they are present in the
	// global registry snapshot. On hot reload, refresh config-backed
	// providers (the built-in `config-api-key` validates top-level
	// `api-keys`) and call accessManager.SetProviders(
	// sdkaccess.RegisteredProviders()) again.

	type wantOutcome struct {
		provider  string
		principal string
		source    string
		code      sdkaccess.AuthErrorCode
	}

	cases := []struct {
		name string
		req  *http.Request
		want wantOutcome
	}{
		{
			name: "valid X-Custom token authenticates as custom-auth",
			req:  withHeader(httptest.NewRequest(http.MethodGet, "/v1/models", nil), "X-Custom", "s3cret"),
			want: wantOutcome{provider: "custom-auth", principal: "service-user", source: "x-custom"},
		},
		{
			name: "valid X-Partner token falls through to partner-token",
			req:  withHeader(httptest.NewRequest(http.MethodGet, "/v1/models", nil), "X-Partner", "partner-key"),
			want: wantOutcome{provider: "partner-token", principal: "partner-account", source: "x-partner"},
		},
		{
			name: "no credentials aggregates no_credentials",
			req:  httptest.NewRequest(http.MethodGet, "/v1/models", nil),
			want: wantOutcome{code: sdkaccess.AuthErrorCodeNoCredentials},
		},
		{
			name: "wrong custom token yields invalid_credential",
			req:  withHeader(httptest.NewRequest(http.MethodGet, "/v1/models", nil), "X-Custom", "wrong"),
			want: wantOutcome{code: sdkaccess.AuthErrorCodeInvalidCredential},
		},
	}

	failed := false
	for _, tc := range cases {
		result, authErr := manager.Authenticate(context.Background(), tc.req)
		switch {
		case tc.want.code != "":
			if authErr == nil {
				fail(&failed, tc.name, "expected auth error %q, got success", tc.want.code)
				continue
			}
			if !sdkaccess.IsAuthErrorCode(authErr, tc.want.code) {
				fail(&failed, tc.name, "expected code %q, got %q", tc.want.code, authErr.Code)
				continue
			}
			if tc.want.code == sdkaccess.AuthErrorCodeInvalidCredential && authErr.HTTPStatusCode() != http.StatusUnauthorized {
				fail(&failed, tc.name, "expected HTTP 401 on invalid credential, got %d", authErr.HTTPStatusCode())
				continue
			}
		case tc.want.provider != "":
			if authErr != nil {
				fail(&failed, tc.name, "unexpected auth error: %v", authErr)
				continue
			}
			if result == nil ||
				result.Provider != tc.want.provider ||
				result.Principal != tc.want.principal ||
				result.Metadata["source"] != tc.want.source {
				fail(&failed, tc.name, "unexpected result %+v", result)
				continue
			}
		}
		fmt.Printf("PASS  %s\n", tc.name)
	}

	if failed {
		fmt.Fprintln(os.Stderr, "access-hook example: one or more assertions failed")
		os.Exit(1)
	}
	fmt.Println("All access-hook assertions passed. Custom providers are embedded and chained via the sdk/access manager.")
}

// withHeader returns a copy of req carrying the given header.
func withHeader(req *http.Request, key, value string) *http.Request {
	cloned := req.Clone(req.Context())
	cloned.Header.Set(key, value)
	return cloned
}

// fail records a failed assertion and prints the formatted message.
func fail(failed *bool, name, format string, args ...any) {
	*failed = true
	fmt.Fprintf(os.Stderr, "FAIL  %s: %s\n", name, fmt.Sprintf(format, args...))
}
