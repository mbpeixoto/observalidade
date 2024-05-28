package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"

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
	url := "http://zipkin-collector:9411/api/v2/spans"

	ctx := context.Background()
	shutdown, err := initTracer(url)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := shutdown(ctx); err != nil {
			log.Fatal("failed to shutdown TracerProvider: %w", err)
		}
	}()

	tr := otel.GetTracerProvider().Tracer("servicoB")
	router := mux.NewRouter()
	router.HandleFunc("/servicoB/{cep}", func(w http.ResponseWriter, r *http.Request) {
		ServicoB(ctx, tr, w, r)
	}).Methods("GET")

	http.ListenAndServe(":8081", router)
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

const apiKey = "4a3689591e7746a38fc120653242305"

type ViaCep struct {
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
	TemperaturaGraus     float64 `json:"Temp_C"`
	TemperaturaFarenheit float64 `json:"Temp_F"`
	TemperaturaKelvin    float64 `json:"Temp_K"`
}

type WeatherResponse struct {
	Current struct {
		TempC float64 `json:"temp_c"`
	} `json:"current"`
}

func ServicoB(ctx context.Context, tr trace.Tracer, w http.ResponseWriter, r *http.Request) {
	propagator := otel.GetTextMapPropagator()
	ctx = propagator.Extract(ctx, propagation.HeaderCarrier(r.Header))

	_, span := tr.Start(ctx, "ServicoB")
	defer span.End()

	cep := mux.Vars(r)["cep"]
	span.SetAttributes(attribute.String("request.cep", cep))

	req, err := http.NewRequest("GET", "http://viacep.com.br/ws/"+cep+"/json/", nil)
	if err != nil {
		http.Error(w, "invalid zipcode", http.StatusUnprocessableEntity)
		return
	}

	client := http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}

	_, childSpan := tr.Start(ctx, "Call to ViaCep API", trace.WithSpanKind(trace.SpanKindClient))
	resp, err := client.Do(req)
	childSpan.End()

	if err != nil {
		http.Error(w, "erro ao comunicar com viaCep", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	var viaCep ViaCep
	if err := json.NewDecoder(resp.Body).Decode(&viaCep); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	span.SetAttributes(attribute.String("viaCep.localidade", viaCep.Localidade))

	location := viaCep.Localidade

	tempC, err := getWeather(apiKey, location)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	span.SetAttributes(attribute.Float64("weather.tempC", tempC))

	tempF := celsiusToFarenheit(tempC)
	tempK := celsiusToKelvin(tempC)

	var temperaturaResponse TemperaturaResponse
	temperaturaResponse.TemperaturaGraus = tempC
	temperaturaResponse.TemperaturaFarenheit = tempF
	temperaturaResponse.TemperaturaKelvin = tempK

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(temperaturaResponse)
}


func getWeather(apiKey string, location string) (float64, error) {
	formattedLocation := url.QueryEscape(location)
	urlWeather := fmt.Sprintf("http://api.weatherapi.com/v1/current.json?key=%s&q=%s", apiKey, formattedLocation)

	resp, err := http.Get(urlWeather)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("API request failed with status: %s", resp.Status)
	}

	var weatherResp WeatherResponse
	if err := json.NewDecoder(resp.Body).Decode(&weatherResp); err != nil {
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