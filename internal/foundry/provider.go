package foundry

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// catalogTTL is how long a successful discovery is reused. Deployments change
// rarely, and every refresh costs two Azure round trips.
const catalogTTL = 5 * time.Minute

// failedCatalogTTL retries a failed discovery quickly, so fixing a permission
// does not leave the settings page stale for minutes.
const failedCatalogTTL = 15 * time.Second

// ErrNotConfigured is returned when no Foundry identity is configured at all.
var ErrNotConfigured = errors.New("no Azure Foundry identity is configured")

// Provider caches one account's discovery result and serialises refreshes, so
// concurrent settings requests and scans share a single Azure round trip.
type Provider struct {
	enabled  bool
	client   *Client
	setupErr error
	gate     chan struct{}

	mu          sync.Mutex
	snapshot    Snapshot
	refreshErr  error
	lastAttempt time.Time
}

// NewProvider builds a provider. A partially filled identity still enables
// Foundry mode and surfaces its validation error, rather than silently falling
// back to the manual endpoint fields.
func NewProvider(identity Identity) *Provider {
	p := &Provider{enabled: identity.Configured(), gate: make(chan struct{}, 1)}
	if p.enabled {
		p.client, p.setupErr = New(identity)
	}
	return p
}

// Enabled reports whether the application runs in Foundry mode.
func (p *Provider) Enabled() bool { return p != nil && p.enabled }

// Status describes the configuration for the settings page. It never exposes
// the client secret.
type Status struct {
	Enabled     bool         `json:"enabled"`
	ResourceID  string       `json:"resource_id"`
	TenantID    string       `json:"tenant_id"`
	ClientID    string       `json:"client_id"`
	Endpoint    string       `json:"endpoint"`
	Deployments []Deployment `json:"deployments"`
	RefreshedAt time.Time    `json:"refreshed_at,omitzero"`
	Error       string       `json:"error,omitempty"`
}

// Status returns the cached discovery state, refreshing it when it is stale.
func (p *Provider) Status(ctx context.Context, force bool) Status {
	if !p.Enabled() {
		return Status{}
	}
	status := Status{Enabled: true}
	if p.client != nil {
		status.ResourceID, status.TenantID, status.ClientID =
			p.client.ResourceID(), p.client.TenantID(), p.client.ClientID()
	}
	snapshot, err := p.Catalog(ctx, force)
	if err != nil {
		status.Error = err.Error()
		status.Deployments = []Deployment{}
		return status
	}
	status.Endpoint, status.Deployments, status.RefreshedAt =
		snapshot.Endpoint, snapshot.Deployments, snapshot.RefreshedAt
	if status.Deployments == nil {
		status.Deployments = []Deployment{}
	}
	return status
}

// Catalog returns the current snapshot, discovering it when it is missing,
// stale or explicitly forced.
func (p *Provider) Catalog(ctx context.Context, force bool) (Snapshot, error) {
	if !p.Enabled() {
		return Snapshot{}, ErrNotConfigured
	}
	if p.setupErr != nil {
		return Snapshot{}, p.setupErr
	}
	if p.client == nil {
		return Snapshot{}, ErrNotConfigured
	}
	select {
	case p.gate <- struct{}{}:
		defer func() { <-p.gate }()
	case <-ctx.Done():
		return Snapshot{}, errors.New("loading the Azure deployments was cancelled")
	}

	p.mu.Lock()
	snapshot, refreshErr, lastAttempt := p.snapshot, p.refreshErr, p.lastAttempt
	p.mu.Unlock()
	ttl := catalogTTL
	if refreshErr != nil {
		ttl = failedCatalogTTL
	}
	if !force && !lastAttempt.IsZero() && time.Since(lastAttempt) < ttl {
		return snapshot, refreshErr
	}

	snapshot, err := p.client.Refresh(ctx)
	if err != nil {
		snapshot = Snapshot{}
	}
	p.mu.Lock()
	p.snapshot, p.refreshErr, p.lastAttempt = snapshot, err, time.Now()
	p.mu.Unlock()
	return snapshot, err
}

// Authorize signs a verified chat request with a fresh inference token.
func (p *Provider) Authorize(req *http.Request) error {
	if !p.Enabled() || p.client == nil {
		return ErrNotConfigured
	}
	return p.client.Authorize(req)
}
