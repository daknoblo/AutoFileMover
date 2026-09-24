package foundry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const accountJSON = `{"id":"` + testResourceID + `",
	"properties":{"endpoint":"https://` + testAccount + `.openai.azure.com/"}}`

// fakeAzure serves the token and ARM endpoints a discovery run needs.
type fakeAzure struct {
	mu          sync.Mutex
	tokenCalls  int
	armCalls    int
	secretsSeen []string
}

func (f *fakeAzure) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls, f.armCalls
}

// newTestClient wires a client to an in-process HTTPS stand-in for Azure.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *fakeAzure) {
	t.Helper()
	fake := &fakeAzure{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
			fake.mu.Lock()
			fake.tokenCalls++
			fake.secretsSeen = append(fake.secretsSeen, r.FormValue("client_secret"))
			fake.mu.Unlock()
		} else {
			fake.mu.Lock()
			fake.armCalls++
			fake.mu.Unlock()
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)

	c, err := New(Identity{ResourceID: testResourceID, TenantID: testTenant,
		ClientID: testClient, ClientSecret: "top-secret"})
	if err != nil {
		t.Fatal(err)
	}
	httpClient := server.Client()
	httpClient.CheckRedirect = refuseRedirect
	c.httpClient = httpClient
	c.arm, c.login = server.URL, server.URL
	return c, fake
}

func writeToken(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, `{"token_type":"Bearer","expires_in":3600,"access_token":"test-token"}`)
}

func deploymentJSON(name, model, format string) string {
	return fmt.Sprintf(`{"id":%q,"name":%q,"sku":{"name":"Standard"},
		"properties":{"provisioningState":"Succeeded","model":{"name":%q,"format":%q}}}`,
		testResourceID+"/deployments/"+name, name, model, format)
}

func TestRefreshDiscoversChatDeployments(t *testing.T) {
	c, fake := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			writeToken(w)
		case r.URL.Path == testResourceID:
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Errorf("ARM call not signed: %q", got)
			}
			_, _ = fmt.Fprint(w, accountJSON)
		case r.URL.Path == testResourceID+"/deployments":
			_, _ = fmt.Fprintf(w, `{"value":[%s,%s,%s]}`,
				deploymentJSON("mini", "gpt-4o-mini", "OpenAI"),
				deploymentJSON("embeddings", "text-embedding-3-large", "OpenAI"),
				deploymentJSON("reasoner", "o3-mini", "OpenAI"))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})

	snapshot, err := c.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if snapshot.Endpoint != "https://"+testAccount+".openai.azure.com/openai/v1" {
		t.Fatalf("endpoint = %q", snapshot.Endpoint)
	}
	if len(snapshot.Deployments) != 2 {
		t.Fatalf("deployments = %+v; the embedding model must be filtered out", snapshot.Deployments)
	}
	// Sorted by name, so the list is stable across refreshes.
	if snapshot.Deployments[0].Name != "mini" || snapshot.Deployments[1].Name != "reasoner" {
		t.Fatalf("unsorted deployments: %+v", snapshot.Deployments)
	}
	if snapshot.Deployments[0].Reasoning || !snapshot.Deployments[1].Reasoning {
		t.Fatalf("reasoning flags wrong: %+v", snapshot.Deployments)
	}
	if snapshot.RefreshedAt.IsZero() {
		t.Fatal("missing refresh timestamp")
	}
	// The account and the deployment page share one cached token.
	if tokens, arm := fake.counts(); tokens != 1 || arm != 2 {
		t.Fatalf("token calls = %d, arm calls = %d; want 1 and 2", tokens, arm)
	}
	if fake.secretsSeen[0] != "top-secret" {
		t.Fatalf("client secret not sent: %q", fake.secretsSeen[0])
	}
}

func TestRefreshFollowsPagination(t *testing.T) {
	var base string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			writeToken(w)
		case r.URL.Path == testResourceID:
			_, _ = fmt.Fprint(w, accountJSON)
		case r.URL.Path == testResourceID+"/deployments" && r.URL.Query().Get("$skipToken") == "":
			_, _ = fmt.Fprintf(w, `{"value":[%s],"nextLink":%q}`,
				deploymentJSON("first", "gpt-4o", "OpenAI"),
				base+testResourceID+"/deployments?api-version="+apiVersion+"&$skipToken=2")
		case r.URL.Path == testResourceID+"/deployments":
			_, _ = fmt.Fprintf(w, `{"value":[%s]}`, deploymentJSON("second", "gpt-4o", "OpenAI"))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	base = c.arm

	snapshot, err := c.Refresh(t.Context())
	if err != nil || len(snapshot.Deployments) != 2 {
		t.Fatalf("Refresh = %+v, %v", snapshot.Deployments, err)
	}
}

func TestRefreshRejectsForeignPageLink(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			writeToken(w)
		case r.URL.Path == testResourceID:
			_, _ = fmt.Fprint(w, accountJSON)
		default:
			_, _ = fmt.Fprintf(w, `{"value":[%s],"nextLink":"https://attacker.example.com/steal"}`,
				deploymentJSON("first", "gpt-4o", "OpenAI"))
		}
	})
	if _, err := c.Refresh(t.Context()); err == nil {
		t.Fatal("a foreign continuation link must abort discovery")
	}
}

func TestRefreshRejectsForeignDeploymentID(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			writeToken(w)
		case r.URL.Path == testResourceID:
			_, _ = fmt.Fprint(w, accountJSON)
		default:
			_, _ = fmt.Fprint(w, `{"value":[{"id":"/subscriptions/x/other","name":"chat",
				"properties":{"provisioningState":"Succeeded","model":{"name":"gpt-4o","format":"OpenAI"}}}]}`)
		}
	})
	if _, err := c.Refresh(t.Context()); err == nil {
		t.Fatal("a deployment from another account must abort discovery")
	}
}

func TestRefreshRejectsMismatchedAccount(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			writeToken(w)
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"/subscriptions/other","properties":{"endpoint":"https://x.openai.azure.com/"}}`)
	})
	if _, err := c.Refresh(t.Context()); err == nil {
		t.Fatal("an answer for another resource must abort discovery")
	}
}

func TestSignInFailureIsReportedWithoutTheBody(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":"invalid_client","error_description":"secret 1a2b3c is expired"}`)
	})
	_, err := c.Refresh(t.Context())
	if err == nil {
		t.Fatal("an invalid client secret must fail discovery")
	}
	if !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("missing Entra error code: %v", err)
	}
	if strings.Contains(err.Error(), "1a2b3c") {
		t.Fatalf("the sign-in body must not be reflected: %v", err)
	}
}

func TestSignInRefusesRedirect(t *testing.T) {
	c, fake := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			http.Redirect(w, r, "https://attacker.example.com/token", http.StatusFound)
			return
		}
		t.Errorf("discovery continued past a redirected sign-in: %q", r.URL.Path)
	})
	if _, err := c.Refresh(t.Context()); err == nil {
		t.Fatal("a redirected sign-in must fail instead of forwarding the secret")
	}
	if tokens, arm := fake.counts(); tokens != 1 || arm != 0 {
		t.Fatalf("token calls = %d, arm calls = %d; want 1 and 0", tokens, arm)
	}
}

func TestARMFailureMentionsTheLikelyCause(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			writeToken(w)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := c.Refresh(t.Context())
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "Reader") {
		t.Fatalf("unhelpful discovery error: %v", err)
	}
}

func TestTokenIsCachedUntilItExpires(t *testing.T) {
	c, fake := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { writeToken(w) })
	for range 3 {
		if _, err := c.token(t.Context(), armScope); err != nil {
			t.Fatal(err)
		}
	}
	if tokens, _ := fake.counts(); tokens != 1 {
		t.Fatalf("token calls = %d, want 1", tokens)
	}
	// A token that is about to lapse is replaced rather than reused.
	c.mu.Lock()
	c.tokens[armScope] = cachedToken{value: "stale", expiresAt: time.Now().Add(time.Second)}
	c.mu.Unlock()
	if token, err := c.token(t.Context(), armScope); err != nil || token != "test-token" {
		t.Fatalf("token = %q, %v", token, err)
	}
	if tokens, _ := fake.counts(); tokens != 2 {
		t.Fatalf("token calls = %d, want 2", tokens)
	}
}

func TestTokenRejectsUnusableAnswers(t *testing.T) {
	for _, body := range []string{
		`{"token_type":"Bearer","expires_in":3600,"access_token":""}`,
		`{"token_type":"Basic","expires_in":3600,"access_token":"x"}`,
		`{"token_type":"Bearer","expires_in":0,"access_token":"x"}`,
		`{"token_type":"Bearer","expires_in":3600,"access_token":"line\nbreak"}`,
		`not json`,
	} {
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, body)
		})
		if _, err := c.token(t.Context(), armScope); err == nil {
			t.Errorf("accepted unusable token answer %q", body)
		}
	}
}

func TestAuthorizeOnlySignsTheDiscoveredTarget(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			writeToken(w)
		case r.URL.Path == testResourceID:
			_, _ = fmt.Fprint(w, accountJSON)
		default:
			_, _ = fmt.Fprintf(w, `{"value":[%s]}`, deploymentJSON("chat", "gpt-4o", "OpenAI"))
		}
	})

	verified := "https://" + testAccount + ".openai.azure.com/openai/v1/chat/completions"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, verified, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Authorize(req); err == nil {
		t.Fatal("signing before a successful discovery must fail")
	}
	if _, err := c.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.Authorize(req); err != nil {
		t.Fatalf("verified target rejected: %v", err)
	}
	if req.Header.Get("Authorization") != "Bearer test-token" {
		t.Fatalf("missing bearer token: %q", req.Header.Get("Authorization"))
	}

	rejected := []struct {
		name, method, url string
	}{
		{"other host", http.MethodPost, "https://attacker.example.com/openai/v1/chat/completions"},
		{"other path", http.MethodPost, "https://" + testAccount + ".openai.azure.com/openai/v1/embeddings"},
		{"query appended", http.MethodPost, verified + "?api-version=2024-06-01"},
		{"plain http", http.MethodPost, "http://" + testAccount + ".openai.azure.com/openai/v1/chat/completions"},
		{"wrong method", http.MethodGet, verified},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			bad, err := http.NewRequestWithContext(t.Context(), tc.method, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Authorize(bad); err == nil {
				t.Fatalf("signed an unverified target: %s", tc.url)
			}
			if bad.Header.Get("Authorization") != "" {
				t.Fatal("a rejected request must carry no credential")
			}
		})
	}
}

func TestAuthorizeStripsAPreExistingCredential(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { writeToken(w) })
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://x.example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("api-key", "stale")
	req.Header.Set("Authorization", "Bearer stale")
	if err := c.Authorize(req); err == nil {
		t.Fatal("an unverified target must not be signed")
	}
	if req.Header.Get("api-key") != "" || req.Header.Get("Authorization") != "" {
		t.Fatalf("stale credentials survived: %v", req.Header)
	}
}

func TestProviderCachesAndForcesDiscovery(t *testing.T) {
	c, fake := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			writeToken(w)
		case r.URL.Path == testResourceID:
			_, _ = fmt.Fprint(w, accountJSON)
		default:
			_, _ = fmt.Fprintf(w, `{"value":[%s]}`, deploymentJSON("chat", "gpt-4o", "OpenAI"))
		}
	})
	p := &Provider{enabled: true, client: c, gate: make(chan struct{}, 1)}

	for range 3 {
		if snapshot, err := p.Catalog(t.Context(), false); err != nil || len(snapshot.Deployments) != 1 {
			t.Fatalf("Catalog = %+v, %v", snapshot, err)
		}
	}
	_, arm := fake.counts()
	if arm != 2 {
		t.Fatalf("arm calls = %d; a cached catalog must not re-query Azure", arm)
	}
	if _, err := p.Catalog(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if _, arm = fake.counts(); arm != 4 {
		t.Fatalf("arm calls = %d; a forced refresh must re-query Azure", arm)
	}

	status := p.Status(t.Context(), false)
	if !status.Enabled || status.Error != "" || len(status.Deployments) != 1 ||
		status.ResourceID != testResourceID || status.TenantID != testTenant || status.ClientID != testClient {
		t.Fatalf("status = %+v", status)
	}
	if encoded, err := json.Marshal(status); err != nil || strings.Contains(string(encoded), "top-secret") {
		t.Fatalf("status leaks the client secret: %s, %v", encoded, err)
	}
}

func TestProviderRetriesAFailedDiscoverySoon(t *testing.T) {
	c, fake := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	p := &Provider{enabled: true, client: c, gate: make(chan struct{}, 1)}
	if _, err := p.Catalog(t.Context(), false); err == nil {
		t.Fatal("expected a discovery failure")
	}
	tokens, _ := fake.counts()
	// Within the short failure window the error is reused rather than retried.
	if _, err := p.Catalog(t.Context(), false); err == nil {
		t.Fatal("expected the cached failure")
	}
	if again, _ := fake.counts(); again != tokens {
		t.Fatalf("token calls = %d, want %d", again, tokens)
	}
	p.mu.Lock()
	p.lastAttempt = time.Now().Add(-failedCatalogTTL - time.Second)
	p.mu.Unlock()
	if _, err := p.Catalog(t.Context(), false); err == nil {
		t.Fatal("expected a retried discovery failure")
	}
	if again, _ := fake.counts(); again <= tokens {
		t.Fatalf("a stale failure must be retried: %d calls", again)
	}
}

func TestProviderIsInertWithoutAnIdentity(t *testing.T) {
	p := NewProvider(Identity{})
	if p.Enabled() {
		t.Fatal("an empty identity must not enable Foundry mode")
	}
	if status := p.Status(t.Context(), false); status.Enabled || len(status.Deployments) != 0 {
		t.Fatalf("status = %+v", status)
	}
	if _, err := p.Catalog(t.Context(), false); err == nil {
		t.Fatal("Catalog must report that no identity is configured")
	}
	if err := p.Authorize(&http.Request{}); err == nil {
		t.Fatal("Authorize must report that no identity is configured")
	}
}

func TestProviderSurfacesAnInvalidIdentity(t *testing.T) {
	p := NewProvider(Identity{ResourceID: "nonsense", TenantID: testTenant,
		ClientID: testClient, ClientSecret: "s"})
	if !p.Enabled() {
		t.Fatal("a supplied identity must enable Foundry mode so its error is visible")
	}
	status := p.Status(t.Context(), false)
	if status.Error == "" || !strings.Contains(status.Error, "Cognitive Services") {
		t.Fatalf("status = %+v", status)
	}
}
