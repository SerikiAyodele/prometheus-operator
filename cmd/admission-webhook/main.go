// Copyright 2022 The prometheus-operator Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"flag"
	stdlog "log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"

	logging "github.com/prometheus-operator/prometheus-operator/internal/log"
	"github.com/prometheus-operator/prometheus-operator/pkg/admission"
	"github.com/prometheus-operator/prometheus-operator/pkg/server"
	"github.com/prometheus-operator/prometheus-operator/pkg/versionutil"
)

func main() {
	var (
		serverConfig server.Config = server.DefaultConfig(":8443", true)
		flagset                    = flag.CommandLine
		logConfig    logging.Config
	)

	server.RegisterFlags(flagset, &serverConfig)
	versionutil.RegisterFlags(flagset)
	logging.RegisterFlags(flagset, &logConfig)

	_ = flagset.Parse(os.Args[1:])

	if versionutil.ShouldPrintVersion() {
		versionutil.Print(os.Stdout, "admission-webhook")
		return
	}

	logger, err := logging.NewLogger(logConfig)
	if err != nil {
		stdlog.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wg, ctx := errgroup.WithContext(ctx)

	mux := http.NewServeMux()
	admit := admission.New(log.With(logger, "component", "admissionwebhook"))
	admit.Register(mux)

	r := prometheus.NewRegistry()
	r.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		version.NewCollector("prometheus_operator_admission_webhook"),
	)
	mux.Handle("/metrics", promhttp.HandlerFor(r, promhttp.HandlerOpts{}))

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"up"}`))
	})

	srv, err := server.NewServer(logger, &serverConfig, mux)
	if err != nil {
		level.Error(logger).Log("msg", "failed to create web server", "err", err)
		os.Exit(1)
	}

	wg.Go(func() error {
		return srv.Serve(ctx)
	})

	term := make(chan os.Signal, 1)
	signal.Notify(term, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-term:
		level.Info(logger).Log("msg", "Received signal, exiting gracefully...", "signal", sig.String())
	case <-ctx.Done():
	}

	if err := srv.Shutdown(ctx); err != nil {
		level.Warn(logger).Log("msg", "Server shutdown error", "err", err)
	}

	cancel()
	if err := wg.Wait(); err != nil {
		level.Warn(logger).Log("msg", "Unhandled error received. Exiting...", "err", err)
		os.Exit(1)
	}
}
