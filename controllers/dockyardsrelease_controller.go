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
	"net/url"
	"path"
	"time"

	semverv3 "github.com/Masterminds/semver/v3"
	imagev1 "github.com/fluxcd/image-reflector-controller/api/v1beta2"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/patch"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/siderolabs/talos/pkg/machinery/platforms"
	dockyardsv1 "github.com/sudoswedenab/dockyards-backend/api/v1alpha3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// +kubebuilder:rbac:groups=dockyards.io,resources=releases,verbs=get;list;watch
// +kubebuilder:rbac:groups=dockyards.io,resources=releases/status,verbs=patch
// +kubebuilder:rbac:groups=image.toolkit.fluxcd.io,resources=imagepolicies,verbs=create;get;list;patch;watch
// +kubebuilder:rbac:groups=image.toolkit.fluxcd.io,resources=imagerepositories,verbs=create;get;list;patch;watch

type DockyardsReleaseReconciler struct {
	client.Client

	ImageFactoryHost string
}

func (r *DockyardsReleaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, reterr error) {
	var release dockyardsv1.Release
	err := r.Get(ctx, req.NamespacedName, &release)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	patchHelper, err := patch.NewHelper(&release, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	defer func() {
		err := patchHelper.Patch(ctx, &release)
		if err != nil {
			result = ctrl.Result{}
			reterr = kerrors.NewAggregate([]error{reterr, err})
		}
	}()

	switch release.Spec.Type {
	case dockyardsv1.ReleaseTypeKubernetes:
		return r.reconcileKubernetesReleases(ctx, &release)
	case dockyardsv1.ReleaseTypeTalosInstaller:
		return r.reconcileTalosInstaller(ctx, &release)
	}

	return ctrl.Result{}, nil
}

func (r *DockyardsReleaseReconciler) reconcileKubernetesReleases(ctx context.Context, release *dockyardsv1.Release) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	logger.Info("reconciling kubernetes releases")

	imageRepository := imagev1.ImageRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      release.Name,
			Namespace: release.Namespace,
		},
	}

	_, err := controllerutil.CreateOrPatch(ctx, r.Client, &imageRepository, func() error {
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
	var latestVersion *semverv3.Version

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

			if imagePolicy.Labels == nil {
				imagePolicy.Labels = make(map[string]string)
			}

			imagePolicy.Labels[dockyardsv1.LabelReleaseName] = release.Name

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

		version, err := semverv3.NewVersion(tag)
		if err != nil {
			logger.Error(err, "error parsing version as semver")

			continue
		}

		if latestVersion == nil {
			latestVersion = version

			continue
		}

		if version.GreaterThan(latestVersion) {
			latestVersion = version
		}
	}

	if latestVersion != nil {
		release.Status.Versions = versions
		release.Status.LatestVersion = latestVersion.Original()
	}

	return ctrl.Result{}, nil
}

func (r *DockyardsReleaseReconciler) reconcileTalosInstaller(ctx context.Context, release *dockyardsv1.Release) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	logger.Info("reconciling talos installer")

	if len(release.Spec.Ranges) != 1 {
		logger.Info("ignoring talos installer release without exactly one range", "count", len(release.Spec.Ranges))

		return ctrl.Result{}, nil
	}

	imageRepository := imagev1.ImageRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      release.Name,
			Namespace: release.Namespace,
		},
	}

	_, err := controllerutil.CreateOrPatch(ctx, r.Client, &imageRepository, func() error {
		imageRepository.Spec.Image = "ghcr.io/siderolabs/installer"

		imageRepository.Spec.Interval = metav1.Duration{
			Duration: time.Hour,
		}

		return nil
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	imagePolicy := imagev1.ImagePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageRepository.Name,
			Namespace: imageRepository.Namespace,
		},
	}

	_, err = controllerutil.CreateOrPatch(ctx, r.Client, &imagePolicy, func() error {
		imagePolicy.Spec.ImageRepositoryRef = meta.NamespacedObjectReference{
			Name: imageRepository.Name,
		}

		imagePolicy.Spec.Policy = imagev1.ImagePolicyChoice{
			SemVer: &imagev1.SemVerPolicy{
				Range: release.Spec.Ranges[0],
			},
		}

		if imagePolicy.Labels == nil {
			imagePolicy.Labels = make(map[string]string)
		}

		imagePolicy.Labels[dockyardsv1.LabelReleaseName] = release.Name

		return nil
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	if imagePolicy.Status.LatestImage == "" {
		logger.Info("ignoring talos installer image policy without latest image")

		return ctrl.Result{}, nil
	}

	reference, err := name.ParseReference(imagePolicy.Status.LatestImage)
	if err != nil {
		return ctrl.Result{}, err
	}

	tag := reference.Identifier()

	release.Status.Versions = []string{tag}
	release.Status.LatestVersion = tag

	platformName, hasAnnotation := release.Annotations[AnnotationTalosPlatformName]
	if !hasAnnotation {
		release.Status.LatestURL = nil

		return ctrl.Result{}, nil
	}

	schematicID, hasAnnotation := release.Annotations[AnnotationTalosSchematicID]
	if !hasAnnotation {
		schematicID = DefaultSchematicID
	}

	var platform *platforms.Platform

	for _, cloudPlatform := range platforms.CloudPlatforms() {
		if cloudPlatform.Name != platformName {
			continue
		}

		platform = &cloudPlatform

		break
	}

	if platform == nil {
		logger.Info("invalid platform name", "platformName", platformName)

		release.Status.LatestURL = nil

		return ctrl.Result{}, nil
	}

	u := url.URL{
		Scheme: "https",
		Host:   r.ImageFactoryHost,
		Path:   path.Join("image", schematicID, tag, platform.DiskImageDefaultPath(platforms.ArchAmd64)),
	}

	release.Status.LatestURL = ptr.To(u.String())

	return ctrl.Result{}, nil
}

func (r *DockyardsReleaseReconciler) ReleaseFromImagePolicy(_ context.Context, obj client.Object) []ctrl.Request {
	imagePolicy, ok := obj.(*imagev1.ImagePolicy)
	if !ok {
		return nil
	}

	releaseName, hasLabel := imagePolicy.Labels[dockyardsv1.LabelReleaseName]
	if hasLabel {
		return []ctrl.Request{
			{
				NamespacedName: types.NamespacedName{
					Name:      releaseName,
					Namespace: imagePolicy.Namespace,
				},
			},
		}
	}

	return nil
}

func (r *DockyardsReleaseReconciler) SetupwithManager(m ctrl.Manager) error {
	scheme := m.GetScheme()

	_ = dockyardsv1.AddToScheme(scheme)
	_ = imagev1.AddToScheme(scheme)

	err := ctrl.NewControllerManagedBy(m).
		For(&dockyardsv1.Release{}).
		Watches(
			&imagev1.ImagePolicy{},
			handler.EnqueueRequestsFromMapFunc(r.ReleaseFromImagePolicy),
		).
		Complete(r)
	if err != nil {
		return err
	}

	return nil
}
