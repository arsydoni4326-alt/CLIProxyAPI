package management

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOAuthProxyOverride(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		expect string
	}{
		{name: "absent", query: "", expect: ""},
		{name: "checked true", query: "no_proxy=true", expect: "direct"},
		{name: "checked 1", query: "no_proxy=1", expect: "direct"},
		{name: "checked yes", query: "no_proxy=yes", expect: "direct"},
		{name: "checked case-insensitive", query: "no_proxy=TRUE", expect: "direct"},
		{name: "checked padded", query: "no_proxy=%20true%20", expect: "direct"},
		{name: "unchecked false", query: "no_proxy=false", expect: ""},
		{name: "unchecked 0", query: "no_proxy=0", expect: ""},
		{name: "garbage", query: "no_proxy=maybe", expect: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			req := httptest.NewRequest(http.MethodGet, "/codex-auth-url?"+tc.query, nil)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = req
			if got := oauthProxyOverride(c); got != tc.expect {
				t.Fatalf("oauthProxyOverride() = %q, want %q", got, tc.expect)
			}
		})
	}
}

func TestOAuthProxyOverride_NilContext(t *testing.T) {
	if got := oauthProxyOverride(nil); got != "" {
		t.Fatalf("oauthProxyOverride(nil) = %q, want empty", got)
	}
}
