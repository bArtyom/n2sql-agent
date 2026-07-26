package config

import "os"

const defaultAddress = ":8080"

type Config struct {
	Address     string
	DatabaseURL string
}

func Load() Config {
	address := os.Getenv("SERVER_ADDRESS")
	if address == "" {
		address = defaultAddress
	}

	return Config{
		Address:     address,
		DatabaseURL: os.Getenv("DATABASE_URL"),
	}
}
