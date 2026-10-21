package main

import (
	"asi-genie-poc/internal/asienv"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"asi-genie-poc/internal/genie"
	"asi-genie-poc/internal/intents"
	"asi-genie-poc/internal/store"
	genillm "asi-genie-poc/llm"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	llmCfg, err := genillm.ConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	var llmClient genillm.LLMClient
	if llmCfg.Provider == genillm.ProviderNone {
		log.Printf("WARNING: LLM_PROVIDER=none - no LLM; intents are routed by deterministic keyword matching")
	} else {
		if err := genillm.ValidateEnv(llmCfg.Provider); err != nil {
			log.Fatal(err)
		}
		llmClient, err = genillm.New(llmCfg)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("llm provider=%s model=%q", llmCfg.Provider, llmCfg.Model)
	}

	urls, err := asienv.Apply()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("APP_ENV=%s ASI base URLs: %s", os.Getenv("APP_ENV"), strings.Join(urls, ", "))

	dsn := store.PostgresDSN()
	if dsn == "" {
		log.Fatal("store: Postgres is required - set DATABASE_URL or POSTGRES_HOST/POSTGRES_USER/POSTGRES_PASSWORD/POSTGRES_DB")
	}
	st, err := store.NewPostgres(dsn)
	if err != nil {
		log.Fatalf("store: postgres unreachable: %v", err)
	}
	defer st.Close()
	log.Printf("store=postgres")

	engine := genie.New(llmClient, st)
	engine.Model = llmCfg.Model
	a := &api{engine: engine, store: st}

	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "appEnv": os.Getenv("APP_ENV"), "provider": llmCfg.Provider, "store": "postgres",
			"readTools": len(engine.Tools), "intents": len(intents.All()),
		})
	}
	mux.HandleFunc("GET /v1/genie/healthcheck", health)
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("POST /chat", a.chat)
	mux.HandleFunc("POST /chat/stream", a.chatStream)
	mux.HandleFunc("POST /feedback", a.feedback)
	mux.HandleFunc("GET /conversations/{sessionId}", a.history)
	mux.HandleFunc("GET /intents", a.listIntents)
	mux.HandleFunc("GET /events", a.events)
	mux.HandleFunc("GET /analytics/turns", a.turns)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withLogging(withCORS(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      chatTimeout + 10*time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		log.Printf("asi-genie orchestrator listening on :%s (%d read-only GET tools)", port, len(engine.Tools))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
