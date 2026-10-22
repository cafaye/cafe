// Package features provides the global (deployment-level) feature flag layer for
// cafe, built on OpenFeature so the underlying provider (in-memory, Unleash, ...)
// can be swapped without touching call sites.
package features

import (
	"context"
	"net/http"
	"os"

	"github.com/Unleash/unleash-client-go/v4"
	unleashprovider "github.com/open-feature/go-sdk-contrib/providers/unleash/pkg"
	"github.com/open-feature/go-sdk/openfeature"
	"github.com/open-feature/go-sdk/openfeature/memprovider"
)

const (
	FlagTelemetry  = "telemetry"
	FlagSkillsAuto = "skills_auto_install"
	FlagNewCLI     = "new_cli_ux"
)

var defaults = map[string]bool{
	FlagTelemetry:  true,
	FlagSkillsAuto: true,
	FlagNewCLI:     true,
}

type Config struct {
	UnleashURL         string
	UnleashAPIToken    string
	UnleashAppName     string
	UnleashEnvironment string
}

type Service struct {
	client *openfeature.Client
}

func New(cfg Config) *Service {
	appName := cfg.UnleashAppName
	if appName == "" {
		appName = "cafe"
	}
	env := cfg.UnleashEnvironment
	if env == "" {
		env = envOr("CAFE_FEATURE_ENV", "development")
	}

	var provider openfeature.FeatureProvider
	if cfg.UnleashURL != "" && cfg.UnleashAPIToken != "" {
		opts := []unleash.ConfigOption{
			unleash.WithUrl(cfg.UnleashURL),
			unleash.WithAppName(appName),
			unleash.WithEnvironment(env),
			unleash.WithCustomHeaders(http.Header{"Authorization": {cfg.UnleashAPIToken}}),
		}
		p, err := unleashprovider.NewProvider(unleashprovider.ProviderConfig{Options: opts})
		if err == nil {
			provider = p
		}
	}
	if provider == nil {
		flags := map[string]memprovider.InMemoryFlag{}
		for name, v := range defaults {
			defVariant := "false"
			if v {
				defVariant = "true"
			}
			flags[name] = memprovider.InMemoryFlag{
				Key:            name,
				State:          memprovider.Enabled,
				DefaultVariant: defVariant,
				Variants: map[string]any{
					"true":  true,
					"false": false,
				},
			}
		}
		provider = memprovider.NewInMemoryProvider(flags)
	}

	openfeature.SetProvider(provider)
	client := openfeature.NewClient("cafe")
	return &Service{client: client}
}

func (s *Service) Enabled(ctx context.Context, name string, evalCtx map[string]any) bool {
	if s == nil || s.client == nil {
		v, ok := defaults[name]
		if !ok {
			return false
		}
		return v
	}
	ofCtx := openfeature.NewEvaluationContext("", evalCtx)
	v, err := s.client.BooleanValue(ctx, name, defaultFor(name), ofCtx)
	if err != nil {
		return defaultFor(name)
	}
	return v
}

func (s *Service) All(ctx context.Context, evalCtx map[string]any) map[string]bool {
	out := make(map[string]bool, len(defaults))
	for name := range defaults {
		out[name] = s.Enabled(ctx, name, evalCtx)
	}
	return out
}

func defaultFor(name string) bool {
	if v, ok := defaults[name]; ok {
		return v
	}
	return false
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
