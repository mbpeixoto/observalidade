package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"

	"github.com/gorilla/mux"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
)

var tracer = otel.Tracer("observalidade-otel")

func initTracer() *sdktrace.TracerProvider {
	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "collector:4317",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		log.Fatal("failed to create gRPC connection to collector: %w", err)
	}
	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		log.Fatal("failed to create trace exporter: %w", err)
	}
	if err != nil {
		log.Fatal(err)
	}

	resource, _ := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String("service-b"),
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

func main() {
	tp := initTracer()
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			log.Printf("Error shutting down tracer provider: %v", err)
		}
	}()
	router := mux.NewRouter()
	router.HandleFunc("/{cep}", ServicoB).Methods("GET")

	err := http.ListenAndServe(":8081", router)

	if err != nil {
		log.Println(err)
	} else {
		fmt.Println("ServicoB rodando na porta 8081")
	}
}

const apiKey = "4a3689591e7746a38fc120653242305"

type ViaCep struct {
	Erro        string `json:"erro"`
	Cep         string `json:"cep"`
	Logradouro  string `json:"logradouro"`
	Complemento string `json:"complemento"`
	Bairro      string `json:"bairro"`
	Localidade  string `json:"localidade"`
	Uf          string `json:"uf"`
	Ibge        string `json:"ibge"`
	Gia         string `json:"gia"`
	Ddd         string `json:"ddd"`
	Siafi       string `json:"siafi"`
}

type TemperaturaResponse struct {
	City                 string  `json:"city"`
	TemperaturaGraus     float64 `json:"temp_C"`
	TemperaturaFarenheit float64 `json:"temp_F"`
	TemperaturaKelvin    float64 `json:"temp_K"`
}

type WeatherResponse struct {
	Current struct {
		TempC float64 `json:"temp_c"`
	} `json:"current"`
}

func ServicoB(w http.ResponseWriter, r *http.Request) {
	cep := mux.Vars(r)["cep"]

	if len(cep) != 8 {
		http.Error(w, "invalid zipcode", http.StatusUnprocessableEntity)
		return
	}

	carrier := propagation.HeaderCarrier(r.Header)
	ctx := r.Context()
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	ctx, span := tracer.Start(ctx, "request-service-b")
	defer span.End()


	url := "http://viacep.com.br/" + "ws/" + cep + "/json"
	resp, err := fetchData(ctx, url)
	if err != nil {
		log.Printf("Error fetching data from external API: %v", err)
		w.WriteHeader(500)
		w.Write([]byte("Error fetching zipcode data"))
		return
	}
	
	var viaCep ViaCep

	err = json.Unmarshal(resp, &viaCep)
	if err != nil {
		log.Printf("Error parsing JSON response: %v", err)
		w.WriteHeader(500)
		w.Write([]byte("Error parsing zipcode data"))
		return
	}

	if viaCep.Erro == "true" {
		w.WriteHeader(404)
		w.Write([]byte("can not find zipcode"))
		return
	}

	location := viaCep.Localidade

	tempC, err := getWeather(ctx, apiKey, location)
	if err != nil {
		log.Printf("Error fetching weather data: %v", err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	tempF := celsiusToFarenheit(tempC)
	tempK := celsiusToKelvin(tempC)

	var temperaturaResponse TemperaturaResponse
	temperaturaResponse.City = location
	temperaturaResponse.TemperaturaGraus = tempC
	temperaturaResponse.TemperaturaFarenheit = tempF
	temperaturaResponse.TemperaturaKelvin = tempK

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(temperaturaResponse)
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

func getWeather(ctx context.Context, apiKey string, location string) (float64, error) {
	formattedLocation := url.QueryEscape(location)
	urlWeather := fmt.Sprintf("http://api.weatherapi.com/v1/current.json?key=%s&q=%s", apiKey, formattedLocation)

	resp, err := fetchData(ctx, urlWeather)

	if err != nil {
		return 0, fmt.Errorf("can not find zipcode")
	}

	var weatherResp WeatherResponse

	err = json.Unmarshal(resp, &weatherResp)
	if err != nil {
		return 0, err
	}

	return weatherResp.Current.TempC, nil
}

func celsiusToFarenheit(celsius float64) float64 {
	return (celsius * 9 / 5) + 32
}

func celsiusToKelvin(celsius float64) float64 {
	return celsius + 273.15
}
