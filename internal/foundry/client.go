package foundry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	apiVersion       = "2024-10-01"
	armScope         = "https://management.azure.com/.default"
	inferenceScope   = "https://cognitiveservices.azure.com/.default"
	discoveryTimeout = 45 * time.Second
	tokenTimeout     = 20 * time.Second
	maxResponseBytes = 4 << 20
	maxCatalogBytes  = 16 << 20
	maxPages         = 100
	maxDeployments   = 10000
	// tokenSkew renews a token before it expires so a long discovery run does
	// not fail on a token that lapses mid-flight.
	tokenSkew = 2 * time.Minute
)

// Client holds one service-principal identity and its token cache. Tokens are
// requested directly from the Entra token endpoint, which keeps the static
// binary free of the Azure SDK dependency tree. Do not copy a Client.
type Client struct {
	resourceID string
	identity   Identity
	httpClient *http.Client
	timeout    time.Duration
	// arm and login are the pinned service origins. Tests replace them.
	arm, login string

	mu         sync.RWMutex
	endpoint   string
	generation uint64
	tokens     map[string]cachedToken
}

type cachedToken struct {
	value     string
	expiresAt time.Time
}

// New validates the identity and returns a discovery client.
func New(identity Identity) (*Client, error) {
	id := strings.TrimSuffix(strings.TrimSpace(identity.ResourceID), "/")
	if err := validateResourceID(id); err != nil {
		return nil, err
	}
	tenant, clientID := strings.TrimSpace(identity.TenantID), strings.TrimSpace(identity.ClientID)
	if !validUUID(tenant) || !validUUID(clientID) || strings.TrimSpace(identity.ClientSecret) == "" {
		return nil, errors.New("Azure requires an explicit tenant ID and client ID (both UUIDs) and a client secret")
	}
	return &Client{
		resourceID: id,
		identity:   Identity{ResourceID: id, TenantID: tenant, ClientID: clientID, ClientSecret: identity.ClientSecret},
		httpClient: newHTTPClient(),
		timeout:    discoveryTimeout,
		arm:        armOrigin,
		login:      loginOrigin,
		tokens:     map[string]cachedToken{},
	}, nil
}

// ResourceID returns the validated account resource ID.
func (c *Client) ResourceID() string { return c.resourceID }

// TenantID returns the validated tenant ID.
func (c *Client) TenantID() string { return c.identity.TenantID }

// ClientID returns the validated application ID.
func (c *Client) ClientID() string { return c.identity.ClientID }

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second, CheckRedirect: refuseRedirect,
		Transport: &http.Transport{
			Proxy:             http.ProxyFromEnvironment,
			DialContext:       (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2: true, MaxIdleConns: 10, IdleConnTimeout: 90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

// refuseRedirect keeps the bearer token and the client secret on the origin
// they were addressed to.
func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Refresh invalidates earlier discovery immediately, even if it then fails. A
// concurrent newer refresh supersedes this call rather than restoring stale
// trust in an endpoint.
func (c *Client) Refresh(ctx context.Context) (Snapshot, error) {
	if c == nil || c.httpClient == nil {
		return Snapshot{}, errors.New("the Azure client is not initialised")
	}
	c.mu.Lock()
	c.generation++
	generation := c.generation
	c.endpoint = ""
	c.mu.Unlock()
	if ctx == nil {
		return Snapshot{}, errors.New("Azure discovery requires a context")
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	budget := int64(maxCatalogBytes)
	var account struct {
		ID         string            `json:"id"`
		Properties accountProperties `json:"properties"`
	}
	if err := c.getJSON(ctx, c.resourceURL(c.resourceID), &account, &budget); err != nil {
		return Snapshot{}, err
	}
	if !strings.EqualFold(account.ID, c.resourceID) {
		return Snapshot{}, errors.New("the Azure response does not belong to the configured resource ID")
	}
	parts := strings.Split(c.resourceID, "/")
	endpoint, err := selectEndpoint(account.Properties, parts[len(parts)-1])
	if err != nil {
		return Snapshot{}, err
	}

	deployments, err := c.listDeployments(ctx, &budget)
	if err != nil {
		return Snapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, safeError(ctx, err, "Azure discovery")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		return Snapshot{}, errors.New("the Azure discovery was superseded by a newer refresh")
	}
	c.endpoint = endpoint
	return Snapshot{ResourceID: c.resourceID, Endpoint: endpoint,
		Deployments: deployments, RefreshedAt: time.Now().UTC()}, nil
}

// listDeployments walks every deployment page and keeps the chat-capable ones.
func (c *Client) listDeployments(ctx context.Context, budget *int64) ([]Deployment, error) {
	deployments := make([]Deployment, 0)
	seenPages, seenNames := make(map[string]bool), make(map[string]bool)
	total := 0
	next := c.resourceURL(c.resourceID + "/deployments")
	for page := 0; next != ""; page++ {
		if page >= maxPages || seenPages[next] {
			return nil, errors.New("the Azure deployment list is too long or repeats pages")
		}
		seenPages[next] = true
		var response struct {
			Value    *[]armDeployment `json:"value"`
			NextLink string           `json:"nextLink"`
		}
		if err := c.getJSON(ctx, next, &response, budget); err != nil {
			return nil, err
		}
		if response.Value == nil {
			return nil, errors.New("the Azure response contains no deployment list")
		}
		total += len(*response.Value)
		if total > maxDeployments {
			return nil, errors.New("the Azure deployment list exceeds the supported size")
		}
		for _, raw := range *response.Value {
			nameKey := strings.ToLower(raw.Name)
			if !validSegment(raw.Name) || seenNames[nameKey] ||
				!strings.EqualFold(raw.ID, c.resourceID+"/deployments/"+raw.Name) {
				return nil, errors.New("an Azure deployment has an invalid, foreign or duplicate ID")
			}
			seenNames[nameKey] = true
			d := Deployment{
				Name: raw.Name, ModelName: raw.Properties.Model.Name, ModelFormat: raw.Properties.Model.Format,
				ModelVersion: raw.Properties.Model.Version, ProvisioningState: raw.Properties.ProvisioningState,
				SKU: raw.SKU.Name, Capabilities: raw.Properties.Capabilities,
			}
			if d.SupportsChat() {
				d.Reasoning = d.reasoningSafe()
				deployments = append(deployments, d)
			}
		}
		if response.NextLink == "" {
			break
		}
		resolved, err := c.paginationURL(next, response.NextLink)
		if err != nil {
			return nil, err
		}
		next = resolved
	}
	sort.Slice(deployments, func(i, j int) bool { return deployments[i].Name < deployments[j].Name })
	return deployments, nil
}

type armDeployment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	SKU  struct {
		Name string `json:"name"`
	} `json:"sku"`
	Properties struct {
		Model struct {
			Name    string `json:"name"`
			Format  string `json:"format"`
			Version string `json:"version"`
		} `json:"model"`
		ProvisioningState string            `json:"provisioningState"`
		Capabilities      map[string]string `json:"capabilities"`
	} `json:"properties"`
}

// Authorize signs a chat request for the last verified endpoint. It only ever
// signs a POST to exactly that endpoint, so a mutated destination cannot
// receive the token. Callers must refuse redirects on their own transport;
// this hook does not own it.
func (c *Client) Authorize(req *http.Request) error {
	if req == nil {
		return errors.New("Azure authorization requires an HTTP request")
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	for key := range req.Header {
		if strings.EqualFold(key, "api-key") || strings.EqualFold(key, "Authorization") {
			delete(req.Header, key)
		}
	}
	if c == nil {
		return errors.New("the Azure client is not initialised")
	}
	c.mu.RLock()
	endpoint, generation := c.endpoint, c.generation
	c.mu.RUnlock()
	if endpoint == "" || !cleanURL(req.URL) || req.URL.RawQuery != "" ||
		req.URL.String() != endpoint+"/chat/completions" ||
		(req.Host != "" && req.Host != req.URL.Host) ||
		req.Method != http.MethodPost || req.RequestURI != "" {
		return errors.New("the Azure chat target is not verified by a successful discovery")
	}
	token, err := c.token(req.Context(), inferenceScope)
	if err != nil {
		return err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.generation != generation || c.endpoint != endpoint {
		return errors.New("the Azure discovery changed during authorization")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// token returns a cached access token for the scope, requesting a new one from
// the tenant's Entra token endpoint when none is valid.
func (c *Client) token(ctx context.Context, scope string) (string, error) {
	c.mu.RLock()
	cached, ok := c.tokens[scope]
	c.mu.RUnlock()
	if ok && time.Until(cached.expiresAt) > tokenSkew {
		return cached.value, nil
	}

	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", safeError(ctx, err, "Azure sign-in")
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.identity.ClientID},
		"client_secret": {c.identity.ClientSecret},
		"scope":         {scope},
	}
	target := c.login + "/" + c.identity.TenantID + "/oauth2/v2.0/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("the Azure sign-in request could not be built")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", safeError(ctx, err, "Azure sign-in")
	}
	body, err := readResponse(ctx, resp, maxResponseBytes)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		// The body carries the client secret's failure reason and must not be
		// reflected; the Entra error code alone identifies the problem.
		var failure struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &failure)
		if code := strings.TrimSpace(failure.Error); code != "" && validSegment(code) {
			return "", fmt.Errorf("Azure sign-in failed (HTTP %d, %s); check tenant, client ID, secret and permissions",
				resp.StatusCode, code)
		}
		return "", fmt.Errorf("Azure sign-in failed (HTTP %d); check tenant, client ID, secret and permissions", resp.StatusCode)
	}

	var parsed struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", errors.New("the Azure sign-in response is not valid JSON")
	}
	if parsed.AccessToken == "" || strings.ContainsAny(parsed.AccessToken, "\r\n") ||
		!strings.EqualFold(parsed.TokenType, "Bearer") || parsed.ExpiresIn <= 0 {
		return "", errors.New("Azure did not return a usable access token")
	}
	expiresAt := time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second)

	c.mu.Lock()
	if c.tokens == nil {
		c.tokens = map[string]cachedToken{}
	}
	c.tokens[scope] = cachedToken{value: parsed.AccessToken, expiresAt: expiresAt}
	c.mu.Unlock()
	return parsed.AccessToken, nil
}

// getJSON performs one authenticated ARM read within the catalog byte budget.
func (c *Client) getJSON(ctx context.Context, endpoint string, target any, budget *int64) error {
	token, err := c.token(ctx, armScope)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("the Azure query could not be built")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return safeError(ctx, err, "Azure query")
	}
	body, err := readResponse(ctx, resp, *budget)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the Azure query failed (HTTP %d); check the resource ID and the service principal's Reader role",
			resp.StatusCode)
	}
	*budget -= int64(len(body))
	if err := json.Unmarshal(body, target); err != nil {
		return errors.New("the Azure response is not valid JSON")
	}
	return nil
}

// readResponse consumes a bounded response body and always closes it.
func readResponse(ctx context.Context, resp *http.Response, budget int64) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	limit := min(int64(maxResponseBytes), budget)
	if resp.ContentLength > limit {
		return nil, errors.New("the Azure response exceeds the permitted size")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, safeError(ctx, err, "reading the Azure response")
	}
	if int64(len(body)) > limit {
		return nil, errors.New("the Azure response exceeds the permitted size")
	}
	return bytes.TrimSpace(body), nil
}

type interruptedError struct{ cause error }

func (e interruptedError) Error() string {
	if errors.Is(e.cause, context.DeadlineExceeded) {
		return "the Azure request timed out"
	}
	return "the Azure request was cancelled"
}

func (e interruptedError) Unwrap() error { return e.cause }

// safeError converts a transport failure into a message that cannot leak the
// request target or credentials into the UI.
func safeError(ctx context.Context, err error, action string) error {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(ctx.Err(), cause) || errors.Is(err, cause) {
			return interruptedError{cause: cause}
		}
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return errors.New(action + " timed out")
	}
	return errors.New(action + " failed; check the identity, its permissions and the network connection")
}
