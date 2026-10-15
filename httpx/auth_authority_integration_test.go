//go:build integration

package httpx

import (
	"net/http"
	"testing"
)

func TestAuthDoesNotApplyCredentialsToAnOverriddenAuthority(t *testing.T) {
	for _, provider := range []AuthProvider{
		NewBearerTokenProvider("secret"),
		NewBasicAuthProvider("user", "secret"),
		NewAPIKeyProvider("secret"),
		NewQueryParamAuthProvider("token", "secret"),
	} {
		origin := newCredentialRecorder(t, ok)
		client := must(NewClient(WithAuth(origin.srv.URL, provider)))
		t.Cleanup(client.CloseIdleConnections)
		req, _ := http.NewRequest(http.MethodGet, origin.srv.URL, nil)
		req.Host = "unbound.example"
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		seen := origin.requests()
		if len(seen) != 1 || seen[0].authorization != "" || seen[0].apiKey != "" || seen[0].queryToken != "" {
			t.Fatalf("provider %T leaked to overridden authority: %+v", provider, seen)
		}
	}
}
