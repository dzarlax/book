package main

import (
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/dzarlax/book/internal/calendarapi"
	"github.com/dzarlax/book/internal/config"
	"github.com/dzarlax/book/internal/handler"
	"github.com/dzarlax/book/internal/storage"
)

func main() {
	cfg := config.Load()

	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	tz, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		log.Fatalf("invalid timezone %q: %v", cfg.Timezone, err)
	}

	store, err := storage.New(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer store.Close()

	if err := store.Migrate("migrations"); err != nil {
		log.Printf("warning: migration failed (may already be applied): %v", err)
	}

	funcMap := template.FuncMap{
		"dayName": func(d int) string {
			return [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}[d]
		},
		"list": func(args ...string) []string { return args },
		"lower": strings.ToLower,
	}

	tmpl, err := template.New("").Funcs(funcMap).ParseGlob("internal/ui/templates/*.html")
	if err != nil {
		log.Fatalf("templates: %v", err)
	}

	cal := calendarapi.New(cfg.CalendarAPI, cfg.CalendarKey)
	if cal.Enabled() {
		log.Printf("calendar integration enabled: %s", cfg.CalendarAPI)
	}

	publicH := handler.NewPublicHandler(store, cal, tmpl, tz)
	adminH := handler.NewAdminHandler(store, cal, tmpl, tz)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))

	// Static files
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("internal/ui/static"))))

	// Admin routes
	r.Mount("/admin", adminH.Routes())

	// Public routes (must be last — catches /{slug})
	r.Mount("/", publicH.Routes())

	log.Printf("Book server starting on :%s", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, r); err != nil {
		log.Fatal(err)
	}
}
