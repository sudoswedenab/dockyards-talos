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
	"testing"

	imagev1 "github.com/fluxcd/image-reflector-controller/api/v1beta2"
	"github.com/google/go-cmp/cmp"
	dockyardsv1 "github.com/sudoswedenab/dockyards-backend/api/v1alpha3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReleaseReconciler_TalosInstaller(t *testing.T) {
	t.Run("test openstack platform annotation", func(t *testing.T) {
		release := dockyardsv1.Release{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					AnnotationTalosPlatformName: "openstack",
				},
				Name:      "openstack",
				Namespace: "testing",
			},
			Spec: dockyardsv1.ReleaseSpec{
				Ranges: []string{
					"v1.9.x",
				},
				Type: dockyardsv1.ReleaseTypeTalosInstaller,
			},
		}

		imagePolicy := imagev1.ImagePolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openstack",
				Namespace: "testing",
			},
			Spec: imagev1.ImagePolicySpec{
				Policy: imagev1.ImagePolicyChoice{
					SemVer: &imagev1.SemVerPolicy{
						Range: "v1.9.x",
					},
				},
			},
			Status: imagev1.ImagePolicyStatus{
				LatestImage: "ghcr.io/siderolabs/installer:v1.9.3",
			},
		}

		scheme := runtime.NewScheme()

		_ = dockyardsv1.AddToScheme(scheme)
		_ = imagev1.AddToScheme(scheme)

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&imagePolicy).
			Build()

		r := DockyardsReleaseReconciler{
			Client:           c,
			ImageFactoryHost: "localhost",
		}

		ctx := context.Background()

		_, err := r.reconcileTalosInstaller(ctx, &release)
		if err != nil {
			t.Fatal(err)
		}

		expected := dockyardsv1.Release{
			ObjectMeta: release.ObjectMeta,
			Spec:       release.Spec,
			Status: dockyardsv1.ReleaseStatus{
				LatestURL:     ptr.To("https://localhost/image/376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba/v1.9.3/openstack-amd64.raw.xz"),
				LatestVersion: "v1.9.3",
				Versions:      []string{"v1.9.3"},
			},
		}

		if !cmp.Equal(release, expected) {
			t.Errorf("diff: %s", cmp.Diff(expected, release))
		}
	})

	t.Run("test vmware platform annotation", func(t *testing.T) {
		release := dockyardsv1.Release{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					AnnotationTalosPlatformName: "vmware",
				},
				Name:      "test",
				Namespace: "testing",
			},
			Spec: dockyardsv1.ReleaseSpec{
				Ranges: []string{
					"v1.8.x",
				},
				Type: dockyardsv1.ReleaseTypeTalosInstaller,
			},
		}

		imagePolicy := imagev1.ImagePolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "testing",
			},
			Spec: imagev1.ImagePolicySpec{
				Policy: imagev1.ImagePolicyChoice{
					SemVer: &imagev1.SemVerPolicy{
						Range: "v1.8.x",
					},
				},
			},
			Status: imagev1.ImagePolicyStatus{
				LatestImage: "ghcr.io/siderolabs/installer:v1.8.9",
			},
		}

		scheme := runtime.NewScheme()

		_ = dockyardsv1.AddToScheme(scheme)
		_ = imagev1.AddToScheme(scheme)

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&imagePolicy).
			Build()

		r := DockyardsReleaseReconciler{
			Client:           c,
			ImageFactoryHost: "localhost",
		}

		ctx := context.Background()

		_, err := r.reconcileTalosInstaller(ctx, &release)
		if err != nil {
			t.Fatal(err)
		}

		expected := dockyardsv1.Release{
			ObjectMeta: release.ObjectMeta,
			Spec:       release.Spec,
			Status: dockyardsv1.ReleaseStatus{
				LatestURL:     ptr.To("https://localhost/image/376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba/v1.8.9/vmware-amd64.ova"),
				LatestVersion: "v1.8.9",
				Versions:      []string{"v1.8.9"},
			},
		}

		if !cmp.Equal(release, expected) {
			t.Errorf("diff: %s", cmp.Diff(expected, release))
		}
	})
}

func TestReleaseReconciler_KubernetesInstaller(t *testing.T) {
	scheme := runtime.NewScheme()

	_ = dockyardsv1.AddToScheme(scheme)
	_ = imagev1.AddToScheme(scheme)

	ctx := context.Background()

	t.Run("test empty image policy list", func(t *testing.T) {
		release := dockyardsv1.Release{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "testing",
			},
			Spec: dockyardsv1.ReleaseSpec{
				Ranges: []string{
					"v1.34.x",
					"v1.33.x",
				},
				Type: dockyardsv1.ReleaseTypeTalosInstaller,
			},
		}

		imagePolicyList := imagev1.ImagePolicyList{
			Items: []imagev1.ImagePolicy{},
		}

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithLists(&imagePolicyList).
			Build()

		r := DockyardsReleaseReconciler{
			Client:           c,
			ImageFactoryHost: "localhost",
		}

		_, err := r.reconcileKubernetesReleases(ctx, &release)
		if err != nil {
			t.Fatal(err)
		}

		expected := dockyardsv1.Release{
			ObjectMeta: release.ObjectMeta,
			Spec:       release.Spec,
			Status:     dockyardsv1.ReleaseStatus{},
		}

		if !cmp.Equal(release, expected) {
			t.Errorf("diff: %s", cmp.Diff(expected, release))
		}
	})

	t.Run("test latest version", func(t *testing.T) {
		release := dockyardsv1.Release{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "testing",
			},
			Spec: dockyardsv1.ReleaseSpec{
				Ranges: []string{
					"v1.34.x",
					"v1.33.x",
				},
				Type: dockyardsv1.ReleaseTypeTalosInstaller,
			},
		}

		imagePolicyList := imagev1.ImagePolicyList{
			Items: []imagev1.ImagePolicy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-v1.34.x",
						Namespace: "testing",
					},
					Status: imagev1.ImagePolicyStatus{
						LatestImage: "testing/kubernetes:v1.34.1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-v1.33.x",
						Namespace: "testing",
					},
					Status: imagev1.ImagePolicyStatus{
						LatestImage: "testing/kubernetes:v1.33.9",
					},
				},
			},
		}

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithLists(&imagePolicyList).
			Build()

		r := DockyardsReleaseReconciler{
			Client:           c,
			ImageFactoryHost: "localhost",
		}

		_, err := r.reconcileKubernetesReleases(ctx, &release)
		if err != nil {
			t.Fatal(err)
		}

		expected := dockyardsv1.Release{
			ObjectMeta: release.ObjectMeta,
			Spec:       release.Spec,
			Status: dockyardsv1.ReleaseStatus{
				LatestVersion: "v1.34.1",
				Versions: []string{
					"v1.34.1",
					"v1.33.9",
				},
			},
		}

		if !cmp.Equal(release, expected) {
			t.Errorf("diff: %s", cmp.Diff(expected, release))
		}
	})

}
