package controllers

import (
	"context"
	"testing"

	dockyardsv1 "bitbucket.org/sudosweden/dockyards-backend/pkg/api/v1alpha3"
	imagev1 "github.com/fluxcd/image-reflector-controller/api/v1beta2"
	"github.com/google/go-cmp/cmp"
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
