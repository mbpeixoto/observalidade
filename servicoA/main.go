package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"

	"github.com/gorilla/mux"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/zipkin"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
)

func main() {
	url := flag.String("zipkin", "http://zipkin-collector:9411/api/v2/spans", "zipkin url")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	shutdown, err := initTracer(*url)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := shutdown(ctx); err != nil {
			log.Fatal("failed to shutdown TracerProvider: %w", err)
		}
	}()

	tr := otel.GetTracerProvider().Tracer("servicoA")
	r := mux.NewRouter()
	r.HandleFunc("/servicoA", func(w http.ResponseWriter, r *http.Request) {
		ServicoA(ctx, tr, w, r)
	}).Methods("POST")

	http.ListenAndServe(":8080", r)
}

func initTracer(url string) (func(context.Context) error, error) {
	exporter, err := zipkin.New(url)
	if err != nil {
		return nil, err
	}

	batcher := sdktrace.NewBatchSpanProcessor(exporter)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(batcher),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp.Shutdown, nil
}

type BodyA struct {
	Cep string `json:"cep"`
}

func ServicoA(ctx context.Context, tr trace.Tracer, w http.ResponseWriter, r *http.Request) {
	_, span := tr.Start(ctx, "ServicoA")
	defer span.End()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Erro ao ler o corpo da requisição", http.StatusBadRequest)
		return
	}
	span.SetAttributes(attribute.String("request.body", string(body)))

	var aux BodyA
	if err := json.Unmarshal(body, &aux); err != nil {
		http.Error(w, "Erro ao parsear o corpo da requisição", http.StatusBadRequest)
		return
	}
	log.Println(aux.Cep)

	if len(aux.Cep) != 8 {
		http.Error(w, "invalid zipcode", http.StatusUnprocessableEntity)
		return
	}

	req, err := http.NewRequest("GET", "http://servicob:8081/servicoB/"+aux.Cep, nil)
	if err != nil {
		http.Error(w, "erro ao criar requisição para servicoB", http.StatusInternalServerError)
		return
	}

	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	client := http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}

	_, childSpan := tr.Start(ctx, "Call to ServicoB", trace.WithSpanKind(trace.SpanKindClient))
	resp, err := client.Do(req)
	childSpan.End()

	if err != nil {
		http.Error(w, "erro ao comunicar com servicoB", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "erro ao comunicar com servicoB", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
}
