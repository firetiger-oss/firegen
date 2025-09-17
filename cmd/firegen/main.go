package main

import (
	"context"
	"encoding/base64"
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

type options struct {
	configFile string
	endpoint   string
	plaintext  bool
	token      string
	useHTTP    bool
	username   string
	password   string
}

func (opts options) newExporter(ctx context.Context) (sdkmetric.Exporter, error) {
	headers := make(map[string]string)

	if opts.token != "" {
		headers["authorization"] = "Bearer " + opts.token
	} else if opts.username != "" && opts.password != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(opts.username + ":" + opts.password))
		headers["authorization"] = "Basic " + auth
	}

	if opts.useHTTP {
		httpOpts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(opts.endpoint)}
		if opts.plaintext {
			httpOpts = append(httpOpts, otlpmetrichttp.WithInsecure())
		}
		if len(headers) > 0 {
			httpOpts = append(httpOpts, otlpmetrichttp.WithHeaders(headers))
		}
		return otlpmetrichttp.New(ctx, httpOpts...)
	}

	grpcOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(opts.endpoint)}
	if opts.plaintext {
		grpcOpts = append(grpcOpts, otlpmetricgrpc.WithTLSCredentials(insecure.NewCredentials()))
	}
	if len(headers) > 0 {
		grpcOpts = append(grpcOpts, otlpmetricgrpc.WithHeaders(headers))
	}
	return otlpmetricgrpc.New(ctx, grpcOpts...)
}

func main() {
	var opts options
	flag.StringVar(&opts.configFile, "config", "firegen.yaml", "Path to config file")
	flag.StringVar(&opts.endpoint, "endpoint", "localhost:4317", "OTLP endpoint")
	flag.BoolVar(&opts.plaintext, "plaintext", false, "Use plaintext connection instead of TLS")
	flag.StringVar(&opts.token, "token", "", "Bearer token for authentication")
	flag.StringVar(&opts.username, "username", "", "Username for Basic authentication")
	flag.StringVar(&opts.password, "password", "", "Password for Basic authentication")
	flag.BoolVar(&opts.useHTTP, "http", false, "Use HTTP instead of gRPC")
	flag.Parse()

	var cfg config
	if f, err := os.Open(opts.configFile); err != nil {
		log.Fatalf("Failed to open %s: %v", opts.configFile, err)
	} else if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		log.Fatalf("Failed to parse %s: %v", opts.configFile, err)
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
		go generate(ctx, serviceName, metricNames, allAttributes, offset, interval, opts)
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
	opts options,
) {
	exporter, err := opts.newExporter(ctx)
	if err != nil {
		log.Fatalf("Failed to create OTLP exporter for service %s: %v", serviceName, err)
	}
	defer exporter.Shutdown(ctx)

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

	exportTimeout := time.Second
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
		t := time.Now()
		err = exporter.Export(exportCtx, &metrics)
		td := time.Since(t)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			log.Printf("Timeout after %s exporting metrics for %s", exportTimeout, serviceName)
		} else if ctx.Err() != nil {
			return
		} else if err != nil {
			log.Printf("Failed to export metrics for %s: %v", serviceName, err)
		} else {
			log.Printf("Exported %d measurements for %s in %dms", len(gauges), serviceName, td.Milliseconds())
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
