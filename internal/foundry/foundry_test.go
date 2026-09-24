package foundry

import (
	"testing"
)

func TestSupportsChatFiltersUnusableDeployments(t *testing.T) {
	succeeded := func(d Deployment) Deployment {
		d.ProvisioningState = "Succeeded"
		return d
	}
	cases := []struct {
		name string
		d    Deployment
		want bool
	}{
		{"openai chat model", Deployment{Name: "gpt", ModelName: "gpt-4o", ModelFormat: "OpenAI"}, true},
		{"dated model name", Deployment{ModelName: "gpt-4o-2024-08-06", ModelFormat: "OpenAI"}, true},
		{"model router", Deployment{ModelName: "model-router", ModelFormat: "OpenAI"}, true},
		{"known publisher pair", Deployment{ModelName: "mistral-large", ModelFormat: "Mistral AI"}, true},
		{"embedding model", Deployment{ModelName: "text-embedding-3-large", ModelFormat: "OpenAI"}, false},
		{"image model", Deployment{ModelName: "dall-e-3", ModelFormat: "OpenAI"}, false},
		{"audio model", Deployment{ModelName: "whisper", ModelFormat: "OpenAI"}, false},
		{"instruct completion model", Deployment{ModelName: "gpt-35-turbo-instruct", ModelFormat: "OpenAI"}, false},
		{"wrong publisher for pair", Deployment{ModelName: "mistral-large", ModelFormat: "OpenAI"}, false},
		{"anthropic format", Deployment{ModelName: "claude-3", ModelFormat: "Anthropic"}, false},
		{"missing format", Deployment{ModelName: "gpt-4o"}, false},
		{"batch sku", Deployment{ModelName: "gpt-4o", ModelFormat: "OpenAI", SKU: "GlobalBatch"}, false},
		{"batch only capability", Deployment{ModelName: "gpt-4o", ModelFormat: "OpenAI",
			Capabilities: map[string]string{"batchOnly": "true"}}, false},
		{"chat completion disabled", Deployment{ModelName: "gpt-4o", ModelFormat: "OpenAI",
			Capabilities: map[string]string{"chatCompletion": "false"}}, false},
		{"foreign protocol", Deployment{ModelName: "gpt-4o", ModelFormat: "OpenAI",
			Capabilities: map[string]string{"protocol": "cohere"}}, false},
		{"unknown model with chat capability", Deployment{ModelName: "brand-new-llm", ModelFormat: "OpenAI",
			Capabilities: map[string]string{"chatCompletion": "true"}}, true},
		{"unknown model without evidence", Deployment{ModelName: "brand-new-llm", ModelFormat: "acme"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := succeeded(tc.d).SupportsChat(); got != tc.want {
				t.Errorf("SupportsChat() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSupportsChatRequiresSucceededState(t *testing.T) {
	d := Deployment{ModelName: "gpt-4o", ModelFormat: "OpenAI", ProvisioningState: "Creating"}
	if d.SupportsChat() {
		t.Fatal("a deployment that is not provisioned must not be selectable")
	}
}

func TestReasoningSafeKeepsDefaultsForUnknownModels(t *testing.T) {
	cases := map[string]bool{
		"gpt-4o":            false,
		"gpt-4o-2024-08-06": false,
		"gpt-5":             true,
		"o3-mini":           true,
		"model-router":      true,
		"mistral-large":     true,
		"brand-new-llm":     true,
	}
	for model, want := range cases {
		if got := (Deployment{ModelName: model}).reasoningSafe(); got != want {
			t.Errorf("%s reasoningSafe() = %v, want %v", model, got, want)
		}
	}
}

func TestSnapshotFind(t *testing.T) {
	s := Snapshot{Deployments: []Deployment{{Name: "chat"}, {Name: "mini"}}}
	if d, ok := s.Find("mini"); !ok || d.Name != "mini" {
		t.Fatalf("Find(mini) = %+v, %v", d, ok)
	}
	if _, ok := s.Find("absent"); ok {
		t.Fatal("Find must not invent a deployment")
	}
}

func TestIdentityConfigured(t *testing.T) {
	if (Identity{}).Configured() {
		t.Fatal("an empty identity must not enable Foundry mode")
	}
	if (Identity{TenantID: "  "}).Configured() {
		t.Fatal("whitespace must not enable Foundry mode")
	}
	if !(Identity{ClientSecret: "s"}).Configured() {
		t.Fatal("a partial identity must enable Foundry mode so its error is shown")
	}
}
