package config

import (
	"os"

	"github.com/joho/godotenv"
)

// Config holds all configuration variables for the Go backend.
type Config struct {
	Port          string
	Env           string
	GRPCPort      string
	DBHost        string
	DBPort        string
	DBUser        string
	DBPassword    string
	DBName        string
	DBSSLMode     string
	RedisHost     string
	RedisPort     string
	RedisPassword string
	RabbitMQURL   string
}

// LoadConfig reads the .env file and environment variables to build the Config.
func LoadConfig() (*Config, error) {
	// Load .env file. We ignore the error as in production environments 
	// configuration is typically injected directly via env vars.
	_ = godotenv.Load()

	return &Config{
		Port:          getEnv("PORT", "8080"),
		Env:           getEnv("ENV", "development"),
		GRPCPort:      getEnv("GRPC_PORT", "50051"),
		DBHost:        getEnv("DB_HOST", "localhost"),
		DBPort:        getEnv("DB_PORT", "5432"),
		DBUser:        getEnv("DB_USER", "postgres"),
		DBPassword:    getEnv("DB_PASSWORD", "postgrespassword"),
		DBName:        getEnv("DB_NAME", "depin_db"),
		DBSSLMode:     getEnv("DB_SSLMODE", "disable"),
		RedisHost:     getEnv("REDIS_HOST", "localhost"),
		RedisPort:     getEnv("REDIS_PORT", "6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		RabbitMQURL:   getEnv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
	}, nil
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
