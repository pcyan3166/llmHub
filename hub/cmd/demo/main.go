// The local demo uses a mock HTTP origin and never calls a model provider.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pcyan3166/llmHub/hub/internal/llmhub"
)

const adminToken = "llmhub-local-demo-admin-token"

func main() {
	listen := flag.String("listen", "127.0.0.1:8090", "loopback demo address")
	ui := flag.String("ui", "../ui/llmhub/dist", "built UI directory")
	seed := flag.String("seed", "../deploy/llmhub/seed.json", "sample configuration")
	dbPath := flag.String("db", "", "optional persistent local-demo SQLite path")
	flag.Parse()
	if !strings.HasPrefix(*listen, "127.0.0.1:") {
		log.Fatal("demo must listen on 127.0.0.1")
	}
	if *dbPath == "" {
		dir, err := os.MkdirTemp("", "llmhub-demo-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		*dbPath = filepath.Join(dir, "demo.db")
	}
	store, err := llmhub.OpenStore(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	config, fresh, err := seedDemo(store, *seed)
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	mock := &http.Server{Handler: http.HandlerFunc(mockProvider), ReadHeaderTimeout: 5 * time.Second}
	go mock.Serve(listener)
	defer mock.Close()
	hub, err := llmhub.NewServer(store, "http://"+listener.Addr().String(), adminToken, *ui)
	if err != nil {
		log.Fatal(err)
	}
	defer hub.Close()
	hub.Demo = true
	// Seed representative traffic through the complete gateway path.
	if fresh {
		for i, p := range config.Projects {
			_, key, err := store.CreateKey(p.ID)
			if err != nil {
				log.Fatal(err)
			}
			for j := 0; j < 3+i; j++ {
				body := `{"model":"scene/text.generate","messages":[{"role":"user","content":"local demonstration"}]}`
				req, _ := http.NewRequest("POST", "http://demo/v1/chat/completions", strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+key)
				hub.ServeHTTP(&discardWriter{header: http.Header{}}, req)
			}
		}
	}
	server := &http.Server{Addr: *listen, Handler: hub, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	log.Printf("LOCAL MOCK DEMO http://%s ; admin token: %s ; no external API calls", *listen, adminToken)
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		server.Close()
	}
}

func seedDemo(store *llmhub.Store, seed string) (llmhub.Config, bool, error) {
	config, version, err := store.Config()
	if err != nil {
		return config, false, err
	}
	if version != 1 || len(config.Projects) != 0 || len(config.Pools) != 0 {
		return config, false, nil
	}
	raw, err := os.ReadFile(seed)
	if err != nil {
		return config, false, err
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		return config, false, err
	}
	_, err = store.SaveConfig(config, version)
	return config, err == nil, err
}

type discardWriter struct{ header http.Header }

func (w *discardWriter) Header() http.Header         { return w.header }
func (w *discardWriter) WriteHeader(int)             {}
func (w *discardWriter) Write(b []byte) (int, error) { return len(b), nil }

func mockProvider(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		io.WriteString(w, `{"status":"ok"}`)
		return
	}
	var b struct {
		Stream bool   `json:"stream"`
		Model  string `json:"model"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	if b.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Local mock response\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":128,\"completion_tokens\":64}}\n\ndata: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/openai/v1/images/generations":
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aP0cAAAAASUVORK5CYII="}]}`)
	case "/openai/v1/responses":
		io.WriteString(w, `{"id":"mock-response","object":"response","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Local mock response"}]}],"usage":{"input_tokens":128,"output_tokens":64}}`)
	case "/openai/v1/embeddings":
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"usage":{"prompt_tokens":128,"total_tokens":128}}`)
	case "/openai/v1/chat/completions":
		fmt.Fprintf(w, `{"id":"mock-chat","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"Local mock response"},"finish_reason":"stop"}],"usage":{"prompt_tokens":128,"completion_tokens":64}}`, b.Model)
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"error":{"message":"unexpected mock route"}}`)
	}
}
