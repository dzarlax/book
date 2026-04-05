package config

import "os"

type Config struct {
	Port        string
	DatabaseURL string
	CalendarAPI string // calendar-mcp base URL
	CalendarKey string // calendar-mcp API key
	CalendarID  string // which calendar to use
	BaseURL     string // public URL for links in emails
	Timezone    string // default timezone
}

func Load() *Config {
	return &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: getEnv("DATABASE_URL", ""),
		CalendarAPI: getEnv("CALENDAR_API_URL", ""),
		CalendarKey: getEnv("CALENDAR_API_KEY", ""),
		CalendarID:  getEnv("CALENDAR_ID", ""),
		BaseURL:     getEnv("BASE_URL", "http://localhost:8080"),
		Timezone:    getEnv("TIMEZONE", "Europe/Belgrade"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
