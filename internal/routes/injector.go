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
