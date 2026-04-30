// Copyright 2025 Sudo Sweden AB
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
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"time"

	"github.com/go-logr/logr"
	discoveryv1 "github.com/siderolabs/discovery-api/api/v1alpha1/server/pb"
	"github.com/spf13/pflag"
	"github.com/sudoswedenab/dockyards-talos/controllers"
	"github.com/sudoswedenab/dockyards-talos/internal/discovery"
	"github.com/sudoswedenab/dockyards-talos/webhooks"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	pflag.CommandLine.SortFlags = false

	logLevel := "debug"
	pflag.StringVar(&logLevel, "log-level", logLevel, "log level")

	enableWebhooks := false
	pflag.BoolVar(&enableWebhooks, "enable-webhooks", false, "enable webhooks")

	imageFactoryHost := "factory.talos.dev"
	pflag.StringVar(&imageFactoryHost, "image-factory-host", imageFactoryHost, "image factory host")

	metricsBindAddress := "0"
	pflag.StringVar(&metricsBindAddress, "metrics-bind-address", metricsBindAddress, "metrics bind address")

	discoveryBindAdress := "0"
	pflag.StringVar(&discoveryBindAdress, "discovery-bind-address", discoveryBindAdress, "cluster discovery server bind address")

	discoveryStatePath := "discovery_state.json"
	pflag.StringVar(&discoveryStatePath, "discovery-state-path", discoveryStatePath, "path to where to save the discovery peer state, set this to empty an string to disable persistence")

	discoveryGCInterval := 1 * time.Minute
	pflag.DurationVar(&discoveryGCInterval, "discovery-gc-interval", discoveryGCInterval, "interval at which discovery server should do garbage collection of its state")

	discoveryNoTLS := false
	pflag.BoolVar(&discoveryNoTLS, "no-discovery-tls", discoveryNoTLS, "do not use TLS for cluster discovery server")

	discoveryTLSCertPath := "/tmp/dockyards-talos/serving-certs/discovery-service/ca.crt"
	pflag.StringVar(&discoveryTLSCertPath, "discovery-tls-cert-path", discoveryTLSCertPath, "path to TLS certificate to use for cluster discovery server")

	discoveryTLSKeyPath := "/tmp/dockyards-talos/serving-certs/discovery-service/ca.key"
	pflag.StringVar(&discoveryTLSKeyPath, "discovery-tls-key-path", discoveryTLSKeyPath, "path to TLS key to use for cluster discovery server")

	pflag.Parse()

	discoveryUseTLS := !discoveryNoTLS

	logger, err := newLogger(logLevel)
	if err != nil {
		fmt.Printf("error preparing logger: %s\n", err.Error())
		os.Exit(1)
	}

	wd, err := os.Getwd()
	if err != nil {
		logger.Error("could not get working directory", "err", err)
		os.Exit(1)
	}

	logger.Debug("process info", "wd", wd, "uid", os.Getuid(), "pid", os.Getpid())

	var grpcListener net.Listener
	var grpcServer *grpc.Server
	if discoveryBindAdress != "0" {
		var err error

		grpcListener, err = net.Listen("tcp", discoveryBindAdress)
		if err != nil {
			logger.Error("could not start network listener on address '%s': %w", discoveryBindAdress, err)
			os.Exit(1)
		}

		logger.Info("started listening for cluster discovery server", "address", discoveryBindAdress)

		var provider discovery.StateProvider
		switch discoveryStatePath {
		case "":
			logger.Warn("will not persist peer state to disk since --state-path was empty")
			provider = &discovery.DiscardStateProvider{}
		default:
			logger.Info("persisting peer state to disk", "path", discoveryStatePath)
			provider = &discovery.JSONFileStateProvider{Logger: logger, Path: discoveryStatePath}
		}
		provider = discovery.AddWatch(provider, func(a []discovery.ClusterAffiliate) {
			logger.Debug("updated cluster affiliates", "len", len(a))
			for _, clusterAffiliate := range a {
				logger.Debug("updated cluster affiliate",
					"clusterID", clusterAffiliate.ClusterID,
					"affiliateID", clusterAffiliate.Affiliate.GetId(),
					"removeAfter", clusterAffiliate.RemoveAfter,
				)
			}
		})

		clusterDiscoveryServer := discovery.NewClusterDiscoveryServer(
			discovery.ClusterDiscoveryServerStateProvider(provider),
			discovery.ClusterDiscoveryServerGarbageCollectionInterval(discoveryGCInterval),
		)

		var grpcOpts []grpc.ServerOption
		if discoveryUseTLS {
			cert, err := credentials.NewServerTLSFromFile(discoveryTLSCertPath, discoveryTLSKeyPath)
			if err != nil {
				logger.Error("could not create load TLS from file", "err", err, "tlsCertPath", discoveryTLSCertPath, "tlsKeyPath", discoveryTLSKeyPath)
				os.Exit(1)
			}

			grpcOpts = append(grpcOpts, grpc.Creds(cert))
		}
		grpcServer = grpc.NewServer(grpcOpts...)
		discoveryv1.RegisterClusterServer(grpcServer, clusterDiscoveryServer)
	}

	slogr := logr.FromSlogHandler(logger.Handler())

	ctrl.SetLogger(slogr)

	cfg, err := config.GetConfig()
	if err != nil {
		slogr.Error(err, "error getting config")

		os.Exit(1)
	}

	opts := manager.Options{
		Metrics: server.Options{
			BindAddress: metricsBindAddress,
		},
	}

	m, err := ctrl.NewManager(cfg, opts)
	if err != nil {
		slogr.Error(err, "error creating manager")

		os.Exit(1)
	}

	err = (&controllers.DockyardsReleaseReconciler{
		Client:           m.GetClient(),
		ImageFactoryHost: imageFactoryHost,
	}).SetupwithManager(m)
	if err != nil {
		slogr.Error(err, "error creating dockyards release reconciler")

		os.Exit(1)
	}

	if enableWebhooks {
		err = (&webhooks.DockyardsNodePool{}).SetupWebhookWithManager(m)
		if err != nil {
			slogr.Error(err, "error creating dockyards nodepool webhook")

			os.Exit(1)
		}
	}

	if grpcServer != nil {
		go func() {
			err := grpcServer.Serve(grpcListener)
			if err != nil {
				logger.Error("could not serve gRPC", "err", err)
				os.Exit(1)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer func() {
		stop()
		if grpcServer != nil {
			grpcServer.GracefulStop()
		}
	}()

	err = m.Start(ctx)
	if err != nil {
		slogr.Error(err, "error starting manager")

		os.Exit(1)
	}
}

func newLogger(logLevel string) (*slog.Logger, error) {
	var level slog.Level
	switch logLevel {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("unknown log level %s", logLevel)
	}

	handlerOptions := slog.HandlerOptions{
		Level: level,
	}

	return slog.New(slog.NewTextHandler(os.Stdout, &handlerOptions)), nil
}
