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

package routes

import (
	"context"

	"github.com/go-logr/logr"
)

type Node struct {
	Address string
	Port    int64

	MachineKey string
}

type Route struct {
	Network string
	Gateway string
	Metric  uint32

	Interface string
}

type Injector interface {
	EnsureRoutes(ctx context.Context, node Node, talosConfig []byte, staticRoutes []Route, defaultRoute *Route) error
}

type NoopInjector struct {
	logger logr.Logger
}

func NewNoopInjector(logger logr.Logger) *NoopInjector {
	return &NoopInjector{logger: logger}
}

func (i *NoopInjector) EnsureRoutes(_ context.Context, node Node, _ []byte, staticRoutes []Route, defaultRoute *Route) error {
	defaultIface := ""
	if defaultRoute != nil {
		defaultIface = defaultRoute.Interface
	}

	i.logger.Info(
		"talos route injection placeholder",
		"node", node.Address,
		"port", node.Port,
		"machineKey", node.MachineKey,
		"staticRoutes", len(staticRoutes),
		"hasDefaultRoute", defaultRoute != nil,
		"defaultRouteInterface", defaultIface,
	)

	return nil
}
