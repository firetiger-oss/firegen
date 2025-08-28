package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"iter"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"google.golang.org/grpc/credentials/insecure"
	"gopkg.in/yaml.v2"
)

type config struct {
	Metrics    int               `yaml:"metrics"`
	Interval   int               `yaml:"interval"`
	Services   int               `yaml:"services"`
	Attributes []attributeConfig `yaml:"attributes"`
}

type attributeConfig struct {
	Name        string `yaml:"name"`
	Cardinality int    `yaml:"cardinality"`
}

func main() {
	var (
		configFile = flag.String("config", "firegen.yaml", "Path to config file")
		endpoint   = flag.String("endpoint", "localhost:4317", "OTLP endpoint")
		plaintext  = flag.Bool("plaintext", false, "Use plaintext connection instead of TLS")
		token      = flag.String("token", "", "Bearer token for authentication")
		useHTTP    = flag.Bool("http", false, "Use HTTP instead of gRPC")
	)
	flag.Parse()

	var cfg config
	if f, err := os.Open(*configFile); err != nil {
		log.Fatalf("Failed to open %s: %v", *configFile, err)
	} else if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		log.Fatalf("Failed to parse %s: %v", *configFile, err)
	}
	cfg.Metrics = max(1, cfg.Metrics)
	cfg.Interval = max(1, cfg.Interval)
	cfg.Services = max(1, cfg.Services)
	for i := range cfg.Attributes {
		cfg.Attributes[i].Cardinality = max(1, cfg.Attributes[i].Cardinality)
	}
	interval := time.Duration(cfg.Interval) * time.Second

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var exporter sdkmetric.Exporter
	var err error

	if *useHTTP {
		var httpOpts []otlpmetrichttp.Option
		httpOpts = append(httpOpts, otlpmetrichttp.WithEndpoint(*endpoint))
		if *plaintext {
			httpOpts = append(httpOpts, otlpmetrichttp.WithInsecure())
		}
		if *token != "" {
			httpOpts = append(httpOpts, otlpmetrichttp.WithHeaders(map[string]string{
				"authorization": "Bearer " + *token,
			}))
		}
		exporter, err = otlpmetrichttp.New(ctx, httpOpts...)
	} else {
		var grpcOpts []otlpmetricgrpc.Option
		grpcOpts = append(grpcOpts, otlpmetricgrpc.WithEndpoint(*endpoint))
		if *plaintext {
			grpcOpts = append(grpcOpts, otlpmetricgrpc.WithTLSCredentials(insecure.NewCredentials()))
		}
		if *token != "" {
			grpcOpts = append(grpcOpts, otlpmetricgrpc.WithHeaders(map[string]string{
				"authorization": "Bearer " + *token,
			}))
		}
		exporter, err = otlpmetricgrpc.New(ctx, grpcOpts...)
	}
	if err != nil {
		log.Fatalf("Failed to create OTLP exporter: %v", err)
	}
	defer exporter.Shutdown(ctx)

	metricNames := slices.Collect(func(yield func(string) bool) {
		for i := range cfg.Metrics {
			yield(fmt.Sprintf("metric-%04d", i))
		}
	})
	allAttributes := slices.Collect(iterateAttributes(cfg.Attributes))

	attrCardinality := 1
	for _, attrConfig := range cfg.Attributes {
		attrCardinality *= attrConfig.Cardinality
	}

	log.Printf("Generating %d services, %d metrics, %d attributes", cfg.Services, cfg.Metrics, len(cfg.Attributes))
	log.Printf("Interval %s", interval)
	log.Printf("")
	log.Printf("Attribute cardinality per metric %d", attrCardinality)
	log.Printf("Series per service %d", cfg.Metrics*attrCardinality)
	log.Printf("Total series %d", cfg.Services*cfg.Metrics*attrCardinality)

	for i := range cfg.Services {
		serviceName := fmt.Sprintf("service-%04d", i)
		offset := time.Duration(float32(interval) * float32(i) / float32(cfg.Services))
		go generate(ctx, serviceName, metricNames, allAttributes, offset, interval, exporter)
	}

	log.Printf("")
	log.Printf("Press Ctrl+C to shutdown")
	<-ctx.Done()
	log.Printf("")
	log.Printf("Bye")
}

func generate(
	ctx context.Context,
	serviceName string,
	metricNames []string,
	allAttributes [][]attribute.KeyValue,
	offset, interval time.Duration,
	exporter sdkmetric.Exporter,
) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceNameKey.String(serviceName)),
	)
	if err != nil {
		log.Fatalf("Failed to create resource for %s: %v", serviceName, err)
	}

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res))
	defer provider.Shutdown(ctx)

	meter := provider.Meter("firegen-" + serviceName)
	gauges := make([]metric.Float64Gauge, len(metricNames))
	for i, metricName := range metricNames {
		gauge, err := meter.Float64Gauge(metricName)
		if err != nil {
			log.Fatalf("Failed to create gauge metric %s for %s: %v", metricName, serviceName, err)
		}
		gauges[i] = gauge
	}

	exportTimeout := interval / 4
	tick := func() {
		// Step 1: record metrics
		for _, gauge := range gauges {
			for _, attributes := range allAttributes {
				gauge.Record(ctx, rand.Float64(), metric.WithAttributes(attributes...))
			}
		}

		// Step 2: collect metrics
		var metrics metricdata.ResourceMetrics
		if err := reader.Collect(ctx, &metrics); err != nil {
			log.Fatalf("Failed to collect metrics for %s: %v", serviceName, err)
		}

		// Step 3: export metrics
		exportCtx, cancel := context.WithTimeout(ctx, exportTimeout)
		err = exporter.Export(exportCtx, &metrics)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			log.Printf("Timeout after %s exporting metrics for %s", exportTimeout, serviceName)
		} else if ctx.Err() != nil {
			return
		} else if err != nil {
			log.Printf("Failed to export metrics for %s: %v", serviceName, err)
		} else {
			log.Printf("Exported %d measurements for %s", len(gauges), serviceName)
		}
	}

	time.Sleep(offset)
	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

func iterateAttributes(attrConfigs []attributeConfig) iter.Seq[[]attribute.KeyValue] {
	return func(yield func([]attribute.KeyValue) bool) {
		if len(attrConfigs) == 0 {
			return
		}
		for i := range attrConfigs[0].Cardinality {
			attr := attribute.String(attrConfigs[0].Name, fmt.Sprintf("%09d", i))
			attrs := []attribute.KeyValue{attr}
			if len(attrConfigs) > 1 {
				for recAttrs := range iterateAttributes(attrConfigs[1:]) {
					yield(append(slices.Clone(attrs), recAttrs...))
				}
			} else {
				yield(attrs)
			}
		}
	}
}
