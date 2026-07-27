package config

import (
	"os"
	"strings"
)

const (
	defaultAddress                   = ":8080"
	defaultModelProviderAPIKeyEnvVar = "OPENAI_API_KEY"
	defaultModelProviderAllowedHost  = "api.openai.com"
)

type Config struct {
	Address                   string
	DatabaseURL               string
	ModelProviderAPIKeyEnvVar string
	ModelProviderAllowedHosts []string
}

func Load() Config {
	address := os.Getenv("SERVER_ADDRESS")
	if address == "" {
		address = defaultAddress
	}
	apiKeyEnvVar := os.Getenv("MODEL_PROVIDER_API_KEY_ENV_VAR")
	if apiKeyEnvVar == "" {
		apiKeyEnvVar = defaultModelProviderAPIKeyEnvVar
	}
	allowedHosts := splitHosts(os.Getenv("MODEL_PROVIDER_ALLOWED_HOSTS"))
	if len(allowedHosts) == 0 {
		allowedHosts = []string{defaultModelProviderAllowedHost}
	}

	return Config{
		Address:                   address,
		DatabaseURL:               os.Getenv("DATABASE_URL"),
		ModelProviderAPIKeyEnvVar: apiKeyEnvVar,
		ModelProviderAllowedHosts: allowedHosts,
	}
}

func splitHosts(value string) []string {
	var hosts []string
	for _, host := range strings.Split(value, ",") {
		host = strings.TrimSpace(host)
		if host != "" {
			hosts = append(hosts, strings.ToLower(host))
		}
	}
	return hosts
}
