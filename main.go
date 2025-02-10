package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"

	"bitbucket.org/sudosweden/dockyards-talos/controllers"
	"bitbucket.org/sudosweden/dockyards-talos/webhooks"
	"github.com/go-logr/logr"
	"github.com/spf13/pflag"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func main() {
	var enableWebhooks bool
	var imageFactoryHost string
	pflag.BoolVar(&enableWebhooks, "enable-webhooks", false, "enable webhooks")
	pflag.StringVar(&imageFactoryHost, "image-factory-host", "factory.talos.dev", "image factory host")
	pflag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{})
	slogr := logr.FromSlogHandler(handler)

	ctrl.SetLogger(slogr)

	cfg, err := config.GetConfig()
	if err != nil {
		slogr.Error(err, "error getting config")

		os.Exit(1)
	}

	m, err := ctrl.NewManager(cfg, manager.Options{})
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

	err = m.Start(ctx)
	if err != nil {
		slogr.Error(err, "error starting manager")

		os.Exit(1)
	}
}
