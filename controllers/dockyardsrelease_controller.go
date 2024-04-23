package controllers

import (
	"context"
	"slices"
	"time"

	dockyardsv1 "bitbucket.org/sudosweden/dockyards-backend/pkg/api/v1alpha1"
	imagev1 "github.com/fluxcd/image-reflector-controller/api/v1beta2"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/google/go-containerregistry/pkg/name"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// +kubebuilder:rbac:groups=dockyards.io,resources=releases,verbs=get;list;watch
// +kubebuilder:rbac:groups=dockyards.io,resources=releases/status,verbs=patch
// +kubebuilder:rbac:groups=image.toolkit.fluxcd.io,resources=imagepolicies,verbs=create;get;list;patch;watch
// +kubebuilder:rbac:groups=image.toolkit.fluxcd.io,resources=imagerepositories,verbs=create;get;list;patch;watch

type DockyardsReleaseReconciler struct {
	client.Client
}

func (r *DockyardsReleaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var release dockyardsv1.Release
	err := r.Get(ctx, req.NamespacedName, &release)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	imageRepository := imagev1.ImageRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      release.Name,
			Namespace: release.Namespace,
		},
	}

	_, err = controllerutil.CreateOrPatch(ctx, r.Client, &imageRepository, func() error {
		imageRepository.Spec.Image = "ghcr.io/siderolabs/kubelet"

		imageRepository.Spec.Interval = metav1.Duration{
			Duration: time.Hour,
		}

		return nil
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	versions := []string{}

	for _, version := range release.Spec.Ranges {
		policyName := imageRepository.Name + "-" + version

		imagePolicy := imagev1.ImagePolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      policyName,
				Namespace: imageRepository.Namespace,
			},
		}

		_, err := controllerutil.CreateOrPatch(ctx, r.Client, &imagePolicy, func() error {
			imagePolicy.Spec.ImageRepositoryRef = meta.NamespacedObjectReference{
				Name: imageRepository.Name,
			}

			imagePolicy.Spec.Policy = imagev1.ImagePolicyChoice{
				SemVer: &imagev1.SemVerPolicy{
					Range: version,
				},
			}

			return nil
		})
		if err != nil {
			return ctrl.Result{}, err
		}

		if imagePolicy.Status.LatestImage == "" {
			continue
		}

		reference, err := name.ParseReference(imagePolicy.Status.LatestImage)
		if err != nil {
			return ctrl.Result{}, err
		}

		tag := reference.Identifier()
		versions = append(versions, tag)
	}

	if !slices.Equal(release.Status.Versions, versions) {
		patch := client.MergeFrom(release.DeepCopy())

		release.Status.Versions = versions

		err := r.Status().Patch(ctx, &release, patch)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *DockyardsReleaseReconciler) SetupwithManager(m ctrl.Manager) error {
	scheme := m.GetScheme()

	_ = dockyardsv1.AddToScheme(scheme)
	_ = imagev1.AddToScheme(scheme)

	err := ctrl.NewControllerManagedBy(m).For(&dockyardsv1.Release{}).Complete(r)
	if err != nil {
		return err
	}

	return nil
}
