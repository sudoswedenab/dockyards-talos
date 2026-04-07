// Copyright 2026 Sudo Sweden AB
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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/durationpb"

	discoveryv1 "github.com/siderolabs/discovery-api/api/v1alpha1/server/pb"
)

func main() {
	logLevel := "debug"
	pflag.StringVar(&logLevel, "log-level", "info", "log level")

	address := "localhost:3000"
	pflag.StringVar(&address, "address", address, "address of discovery server")

	clusterID := ""
	pflag.StringVar(&clusterID, "cluster", clusterID, "cluster ID to use")

	affiliateID := ""
	pflag.StringVar(&affiliateID, "affiliate", affiliateID, "affiliate ID to use")

	ttl := 10 * time.Second
	pflag.DurationVar(&ttl, "ttl", ttl, "TTL to use for initial affiliate update request")

	pflag.Parse()

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

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		logger.Error("could not dial gRPC server", "err", err, "address", address)
		os.Exit(1)
	}
	defer func() {
		_ = conn.Close()
	}()

	client := discoveryv1.NewClusterClient(conn)

	ctx := context.TODO()

	res, err := client.List(ctx, &discoveryv1.ListRequest{
		ClusterId: clusterID,
	})
	if err != nil {
		logger.Error("could not get current affiliates", "err", err)
		os.Exit(1)
	}
	logger.Info("initial affiliates", "len", len(res.Affiliates))
	for _, affiliate := range res.Affiliates {
		logger.Info("affiliate is connected", "id", affiliate.Id)
	}

	_, err = client.AffiliateUpdate(ctx, &discoveryv1.AffiliateUpdateRequest{
		ClusterId: clusterID,
		AffiliateId: affiliateID,
		Ttl: durationpb.New(ttl),
	})
	if err != nil {
		logger.Error("could not update affiliate", "err", err)
		os.Exit(1)
	}

	watch, err := client.Watch(ctx, &discoveryv1.WatchRequest{
		ClusterId: clusterID,
	})
	if err != nil {
		logger.Error("could not start watch", "err", err)
		os.Exit(1)
	}
	for {
		msg, err := watch.Recv()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			logger.Error("could not recv message", "err", err)
			os.Exit(1)
		}

		logger.Info("affiliate update received", "len", len(msg.Affiliates))
		for _, affiliate := range msg.Affiliates {
			logger.Info("affiliate", "id", affiliate.Id, "deleted", msg.Deleted)
		}
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
