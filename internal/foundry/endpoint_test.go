package foundry

import (
	"strings"
	"testing"
)

const (
	testAccount    = "myaccount"
	testResourceID = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg" +
		"/providers/Microsoft.CognitiveServices/accounts/" + testAccount
	testTenant = "11111111-1111-1111-1111-111111111111"
	testClient = "22222222-2222-2222-2222-222222222222"
)

func TestValidateResourceID(t *testing.T) {
	if err := validateResourceID(testResourceID); err != nil {
		t.Fatalf("valid resource ID rejected: %v", err)
	}
	invalid := []string{
		"",
		"myaccount",
		"/subscriptions/not-a-uuid/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/a",
		"/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Storage/accounts/a",
		"/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/../providers/Microsoft.CognitiveServices/accounts/a",
		testResourceID + "/deployments",
	}
	for _, id := range invalid {
		if err := validateResourceID(id); err == nil {
			t.Errorf("accepted invalid resource ID %q", id)
		}
	}
}

func TestNormalizeEndpointAcceptsOnlyTheAccountHost(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"https://myaccount.openai.azure.com", "https://myaccount.openai.azure.com/openai/v1"},
		{"https://myaccount.openai.azure.com/", "https://myaccount.openai.azure.com/openai/v1"},
		{"https://myaccount.services.ai.azure.com/openai/v1", "https://myaccount.services.ai.azure.com/openai/v1"},
		{"https://MyAccount.OpenAI.Azure.com/", "https://myaccount.openai.azure.com/openai/v1"},
	}
	for _, tc := range cases {
		got, ok := normalizeEndpoint(tc.raw, testAccount)
		if !ok || got != tc.want {
			t.Errorf("normalizeEndpoint(%q) = %q, %v; want %q", tc.raw, got, ok, tc.want)
		}
	}
	rejected := []string{
		"http://myaccount.openai.azure.com",
		"https://attacker.example.com",
		"https://myaccount.openai.azure.com.evil.test",
		"https://user:pass@myaccount.openai.azure.com",
		"https://myaccount.openai.azure.com/openai/v1?key=1",
		"https://myaccount.openai.azure.com/other/path",
		" https://myaccount.openai.azure.com",
	}
	for _, raw := range rejected {
		if got, ok := normalizeEndpoint(raw, testAccount); ok {
			t.Errorf("normalizeEndpoint(%q) accepted as %q", raw, got)
		}
	}
}

func TestSelectEndpointPrefersTheOpenAIEntry(t *testing.T) {
	props := accountProperties{
		Endpoint: "https://myaccount.services.ai.azure.com/",
		Endpoints: map[string]string{
			"Azure OpenAI Legacy API - Latest moniker": "https://myaccount.openai.azure.com/",
			"Azure AI Model Inference API":             "https://myaccount.services.ai.azure.com/",
		},
	}
	endpoint, err := selectEndpoint(props, testAccount)
	if err != nil || endpoint != "https://myaccount.openai.azure.com/openai/v1" {
		t.Fatalf("selectEndpoint = %q, %v", endpoint, err)
	}
}

func TestSelectEndpointRejectsForeignHosts(t *testing.T) {
	props := accountProperties{
		Endpoint:  "https://attacker.example.com/",
		Endpoints: map[string]string{"Other": "https://attacker.example.com/"},
	}
	if endpoint, err := selectEndpoint(props, testAccount); err == nil {
		t.Fatalf("accepted a foreign endpoint: %q", endpoint)
	}
}

func TestSelectEndpointRejectsAmbiguousCandidates(t *testing.T) {
	props := accountProperties{
		CustomSubDomainName: "otheraccount",
		Endpoints: map[string]string{
			"a": "https://myaccount.openai.azure.com/",
			"b": "https://otheraccount.openai.azure.com/",
		},
	}
	if endpoint, err := selectEndpoint(props, testAccount); err == nil {
		t.Fatalf("accepted an ambiguous endpoint: %q", endpoint)
	}
}

func TestPaginationURLRejectsForeignLinks(t *testing.T) {
	c := &Client{resourceID: testResourceID, arm: armOrigin}
	current := c.resourceURL(c.resourceID + "/deployments")

	next, err := c.paginationURL(current, current+"&$skipToken=abc")
	if err != nil || !strings.Contains(next, "skipToken=abc") ||
		!strings.HasPrefix(next, armOrigin+testResourceID+"/deployments?") {
		t.Fatalf("valid page link rejected: %q, %v", next, err)
	}

	rejected := []string{
		"https://attacker.example.com" + testResourceID + "/deployments",
		armOrigin + "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg" +
			"/providers/Microsoft.CognitiveServices/accounts/other/deployments?api-version=" + apiVersion,
		armOrigin + testResourceID + "/deployments?api-version=2020-01-01",
		armOrigin + testResourceID + "/../deployments?api-version=" + apiVersion,
		"http://management.azure.com" + testResourceID + "/deployments?api-version=" + apiVersion,
		armOrigin + testResourceID + "/deployments?api-version=" + apiVersion + "#fragment",
	}
	for _, raw := range rejected {
		if got, err := c.paginationURL(current, raw); err == nil {
			t.Errorf("accepted foreign page link %q as %q", raw, got)
		}
	}
}

func TestNewRejectsIncompleteIdentity(t *testing.T) {
	cases := []Identity{
		{},
		{ResourceID: testResourceID},
		{ResourceID: testResourceID, TenantID: testTenant, ClientID: testClient},
		{ResourceID: testResourceID, TenantID: "nope", ClientID: testClient, ClientSecret: "s"},
		{ResourceID: "bad", TenantID: testTenant, ClientID: testClient, ClientSecret: "s"},
	}
	for _, identity := range cases {
		if _, err := New(identity); err == nil {
			t.Errorf("accepted incomplete identity %+v", identity)
		}
	}
	c, err := New(Identity{ResourceID: testResourceID + "/", TenantID: testTenant,
		ClientID: testClient, ClientSecret: "s"})
	if err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	if c.ResourceID() != testResourceID || c.TenantID() != testTenant || c.ClientID() != testClient {
		t.Fatalf("identity not normalised: %q, %q, %q", c.ResourceID(), c.TenantID(), c.ClientID())
	}
}
