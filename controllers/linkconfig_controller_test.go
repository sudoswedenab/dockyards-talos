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

package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	linkconfigv1alpha3 "github.com/sudoswedenab/dockyards-talos/api/v1alpha3"
	talosroutes "github.com/sudoswedenab/dockyards-talos/internal/routes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeRouteInjector struct {
	err error
}

func (f *fakeRouteInjector) EnsureRoutes(_ context.Context, _ talosroutes.Node, _ []byte, _ []talosroutes.Route, _ *talosroutes.Route) error {
	return f.err
}

func TestLinkConfigReconciler_Reconcile_PersistsMachineAnnotation(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cluster api scheme: %v", err)
	}
	if err := linkconfigv1alpha3.AddToScheme(scheme); err != nil {
		t.Fatalf("add linkconfig scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine-1",
			Namespace: "org-a",
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: "cluster-a",
		},
		Status: clusterv1.MachineStatus{
			Addresses: clusterv1.MachineAddresses{
				{Type: clusterv1.MachineInternalIP, Address: "10.0.0.11"},
			},
		},
	}

	lc := &linkconfigv1alpha3.LinkConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "default",
			Namespace:  "org-a",
			Generation: 7,
		},
		Spec: linkconfigv1alpha3.LinkConfigSpec{
			StaticRoutes: []linkconfigv1alpha3.LinkConfigRoute{
				{
					Network:   "192.168.20.0/24",
					Gateway:   "10.0.0.1",
					Metric:    100,
					Interface: "eth1",
				},
			},
			DefaultRoute: &linkconfigv1alpha3.LinkConfigDefaultRoute{
				Gateway:   "10.0.0.1",
				Interface: "eth1",
			},
		},
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-a-talosconfig",
			Namespace: "org-a",
		},
		Data: map[string][]byte{
			"talosconfig": []byte("fake-config"),
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(machine, lc, secret).
		Build()

	r := &LinkConfigReconciler{
		Client:   c,
		Injector: &fakeRouteInjector{},
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(machine)})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var updated clusterv1.Machine
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(machine), &updated); err != nil {
		t.Fatalf("get updated machine: %v", err)
	}

	raw, ok := updated.Annotations[machineLinkConfigStateAnnKey]
	if !ok {
		t.Fatalf("missing annotation %q", machineLinkConfigStateAnnKey)
	}

	var got machineLinkConfigStateAnnotation
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode annotation payload: %v", err)
	}

	want := machineLinkConfigStateAnnotation{
		LinkConfigName:       "default",
		LinkConfigGeneration: 7,
		StaticRoutes: []talosroutes.Route{
			{
				Network:   "192.168.20.0/24",
				Gateway:   "10.0.0.1",
				Metric:    100,
				Interface: "eth1",
			},
		},
		DefaultRoute: &talosroutes.Route{
			Gateway:   "10.0.0.1",
			Interface: "eth1",
		},
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("annotation mismatch (-want +got):\n%s", diff)
	}
}

func TestLinkConfigReconciler_Reconcile_DoesNotPersistAnnotationOnInjectError(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cluster api scheme: %v", err)
	}
	if err := linkconfigv1alpha3.AddToScheme(scheme); err != nil {
		t.Fatalf("add linkconfig scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine-1",
			Namespace: "org-a",
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: "cluster-a",
		},
		Status: clusterv1.MachineStatus{
			Addresses: clusterv1.MachineAddresses{
				{Type: clusterv1.MachineInternalIP, Address: "10.0.0.11"},
			},
		},
	}

	lc := &linkconfigv1alpha3.LinkConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "org-a",
		},
		Spec: linkconfigv1alpha3.LinkConfigSpec{
			StaticRoutes: []linkconfigv1alpha3.LinkConfigRoute{
				{
					Network:   "192.168.20.0/24",
					Gateway:   "10.0.0.1",
					Metric:    100,
					Interface: "eth1",
				},
			},
		},
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-a-talosconfig",
			Namespace: "org-a",
		},
		Data: map[string][]byte{
			"talosconfig": []byte("fake-config"),
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(machine, lc, secret).
		Build()

	r := &LinkConfigReconciler{
		Client:   c,
		Injector: &fakeRouteInjector{err: errors.New("talos unavailable")},
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(machine)})
	if err == nil {
		t.Fatal("expected reconcile error")
	}

	var updated clusterv1.Machine
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(machine), &updated); err != nil {
		t.Fatalf("get updated machine: %v", err)
	}

	if updated.Annotations[machineLinkConfigStateAnnKey] != "" {
		t.Fatalf("annotation %q should be empty on inject error", machineLinkConfigStateAnnKey)
	}
}
