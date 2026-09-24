package foundry

import (
	"errors"
	"net/url"
	"strings"
)

// armOrigin is the Azure Resource Manager root used for discovery.
const armOrigin = "https://management.azure.com"

// loginOrigin issues the service-principal access tokens.
const loginOrigin = "https://login.microsoftonline.com"

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, ch := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", ch) {
			return false
		}
	}
	return true
}

// validSegment accepts an Azure resource-group or deployment name.
func validSegment(s string) bool {
	if s == "" || len(s) > 128 || s == "." || s == ".." {
		return false
	}
	for _, ch := range s {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') &&
			(ch < '0' || ch > '9') && !strings.ContainsRune("-_.()", ch) {
			return false
		}
	}
	return true
}

// validAccount accepts a Cognitive Services account name, which also forms the
// host label of its inference endpoint.
func validAccount(s string) bool {
	if len(s) < 2 || len(s) > 64 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, ch := range s {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') &&
			(ch < '0' || ch > '9') && ch != '-' {
			return false
		}
	}
	return true
}

// validateResourceID requires a fully qualified Cognitive Services account ID,
// so discovery can never be pointed at an unrelated resource provider.
func validateResourceID(id string) error {
	p := strings.Split(id, "/")
	if len(p) != 9 || p[0] != "" || !strings.EqualFold(p[1], "subscriptions") ||
		!validUUID(p[2]) || !strings.EqualFold(p[3], "resourceGroups") || !validSegment(p[4]) ||
		!strings.EqualFold(p[5], "providers") || !strings.EqualFold(p[6], "Microsoft.CognitiveServices") ||
		!strings.EqualFold(p[7], "accounts") || !validAccount(p[8]) {
		return errors.New("the Azure resource ID must identify a Cognitive Services account: " +
			"/subscriptions/<id>/resourceGroups/<group>/providers/Microsoft.CognitiveServices/accounts/<account>")
	}
	return nil
}

// cleanURL rejects the URL shapes that could redirect a credential elsewhere.
func cleanURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Host != "" && u.User == nil &&
		u.Opaque == "" && u.Fragment == "" && u.RawFragment == "" &&
		!u.ForceQuery && !strings.Contains(u.EscapedPath(), "%") &&
		!strings.Contains(u.Path, "//") && !strings.Contains(u.Path, "\\")
}

// normalizeEndpoint accepts only an inference host that belongs to one of the
// given account names and reduces it to the OpenAI-compatible v1 root.
func normalizeEndpoint(raw string, accountNames ...string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || raw != strings.TrimSpace(raw) || len(raw) > 4096 ||
		strings.ContainsAny(raw, "?#") || !cleanURL(u) || u.RawQuery != "" {
		return "", false
	}
	host := strings.ToLower(u.Host)
	allowed := false
	for _, name := range accountNames {
		if validAccount(name) && (host == strings.ToLower(name)+".openai.azure.com" ||
			host == strings.ToLower(name)+".services.ai.azure.com") {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", false
	}
	switch u.Path {
	case "", "/", "/openai/v1", "/openai/v1/":
	default:
		return "", false
	}
	return "https://" + host + "/openai/v1", true
}

type accountProperties struct {
	Endpoint            string            `json:"endpoint"`
	Endpoints           map[string]string `json:"endpoints"`
	CustomSubDomainName string            `json:"customSubDomainName"`
}

// selectEndpoint picks the account's verified OpenAI endpoint, preferring the
// explicit OpenAI entry over the generic service root.
func selectEndpoint(p accountProperties, account string) (string, error) {
	if p.CustomSubDomainName != "" && !validAccount(p.CustomSubDomainName) {
		return "", errors.New("the Azure account reports an invalid resource subdomain name")
	}
	for _, raw := range []string{p.Endpoints["Azure OpenAI Legacy API - Latest moniker"], p.Endpoint} {
		if endpoint, ok := normalizeEndpoint(raw, account, p.CustomSubDomainName); ok {
			return endpoint, nil
		}
	}
	candidates := make(map[string]bool)
	for _, raw := range p.Endpoints {
		if endpoint, ok := normalizeEndpoint(raw, account, p.CustomSubDomainName); ok {
			candidates[endpoint] = true
		}
	}
	if len(candidates) == 1 {
		for endpoint := range candidates {
			return endpoint, nil
		}
	}
	return "", errors.New("the Azure account does not expose a single verified OpenAI v1 endpoint")
}

func (c *Client) resourceURL(path string) string {
	return c.arm + path + "?api-version=" + apiVersion
}

// paginationURL resolves a continuation link and requires it to address the
// deployments of the very same account, so a hostile page cannot move the
// access token to another host.
func (c *Client) paginationURL(current, raw string) (string, error) {
	invalid := errors.New("the Azure page link does not unambiguously address the same account's deployments")
	next, err := url.Parse(raw)
	if err != nil || len(raw) > 16384 || raw != strings.TrimSpace(raw) ||
		strings.Contains(raw, "#") || next.User != nil || next.Opaque != "" ||
		strings.Contains(next.EscapedPath(), "%") {
		return "", invalid
	}
	base, err := url.Parse(current)
	if err != nil {
		return "", invalid
	}
	// Reject dot segments before ResolveReference can silently remove them.
	for _, segment := range strings.Split(next.Path, "/") {
		if segment == "." || segment == ".." {
			return "", invalid
		}
	}
	next = base.ResolveReference(next)
	armHost, err := url.Parse(c.arm)
	if err != nil {
		return "", invalid
	}
	if !cleanURL(next) || !strings.EqualFold(next.Host, armHost.Host) ||
		!strings.EqualFold(next.Path, c.resourceID+"/deployments") {
		return "", invalid
	}
	query, err := url.ParseQuery(next.RawQuery)
	if err != nil {
		return "", invalid
	}
	for key, values := range query {
		if len(values) != 1 || (strings.EqualFold(key, "api-version") && (key != "api-version" || values[0] != apiVersion)) {
			return "", invalid
		}
	}
	query.Set("api-version", apiVersion)
	next.Host = armHost.Host
	next.Path = c.resourceID + "/deployments"
	next.RawQuery = query.Encode()
	return next.String(), nil
}
