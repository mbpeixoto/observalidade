package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/gorilla/mux"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
)

var tracer = otel.Tracer("challenge-weather-by-cep-otel")

func main() {
    // Parse flags, if any
   tp := initTracer()
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			log.Printf("Error shutting down tracer provider: %v", err)
		}
	}()
	fmt.Println("TracerProvider inicializado com sucesso")

    r := mux.NewRouter()
    r.HandleFunc("/",ServicoA).Methods("POST")

    err := http.ListenAndServe(":8080", r)
	if err != nil {
		log.Println(err)
	} else{
		fmt.Println("ServicoA rodando na porta 8080")
	}
}

func ServicoA( w http.ResponseWriter, r *http.Request) {
	
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Erro ao ler o corpo da requisição", http.StatusBadRequest)
		return
	}

	var aux BodyA
	if err := json.Unmarshal(body, &aux); err != nil {
		http.Error(w, "Erro ao parsear o corpo da requisição", http.StatusBadRequest)
		return
	}

	if len(aux.Cep) != 8 {
		http.Error(w, "invalid zipcode", http.StatusUnprocessableEntity)
		return
	}

	carrier := propagation.HeaderCarrier(r.Header)
	ctx := r.Context()
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	ctx, span := tracer.Start(ctx, "request-service-a")
	defer span.End()


	resp, err:= fetchData(ctx, "http://service-b:8081/"+aux.Cep)

	if err != nil {
		http.Error(w, "erro ao comunicar com servicoB", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(resp)
}


func initTracer() *sdktrace.TracerProvider {
	ctx := context.Background()
    conn, err := grpc.DialContext(ctx, "collector:4317",
        grpc.WithTransportCredentials(insecure.NewCredentials()),
        grpc.WithBlock(),
    )
    if err != nil {
        log.Fatalf("failed to create gRPC connection to collector: %v", err)
    }
    defer conn.Close()

    exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
    if err != nil {
        log.Fatalf("failed to create trace exporter: %v", err)
    }

    resource, _ := resource.Merge(
        resource.Default(),
        resource.NewWithAttributes(
            semconv.SchemaURL,
            semconv.ServiceNameKey.String("servicoA"),
        ),
    )

    tp := sdktrace.NewTracerProvider(
        sdktrace.WithSampler(sdktrace.AlwaysSample()),
        sdktrace.WithBatcher(exporter),
        sdktrace.WithResource(resource),
    )

    otel.SetTracerProvider(tp)
    otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

    return tp
}

type BodyA struct {
	Cep string `json:"cep"`
}

func fetchData(c context.Context, url string) (response []byte, err error) {
	res, err := otelhttp.Get(c, url)
    if err != nil {
        return nil, err
    }
    defer res.Body.Close()

    body, err := io.ReadAll(res.Body)
    if err != nil {
        return nil, err
    }

    return body, nil
}

