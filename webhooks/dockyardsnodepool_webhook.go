package webhooks

import (
	"context"
	"fmt"

	dockyardsv1 "bitbucket.org/sudosweden/dockyards-backend/pkg/api/v1alpha2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:groups=dockyards.io,resources=nodepools,verbs=create;update,path=/validate-dockyards-io-v1alpha2-nodepool,mutating=false,failurePolicy=fail,sideEffects=none,admissionReviewVersions=v1,versions=v1alpha2,name=validation.nodepool.dockyards.io

type DockyardsNodePool struct{}

var _ webhook.CustomValidator = &DockyardsNodePool{}

var (
	memoryLimit       = resource.MustParse("2Gi")
	memoryLimitDetail = fmt.Sprintf("must be at least %s", memoryLimit.String())
)

func (webhook *DockyardsNodePool) SetupWebhookWithManager(mgr ctrl.Manager) error {
	scheme := mgr.GetScheme()

	_ = dockyardsv1.AddToScheme(scheme)

	return ctrl.NewWebhookManagedBy(mgr).For(&dockyardsv1.NodePool{}).WithValidator(webhook).Complete()
}

func (webhook *DockyardsNodePool) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	dockyardsNodePool, ok := obj.(*dockyardsv1.NodePool)
	if !ok {
		return nil, nil
	}

	return nil, webhook.validate(dockyardsNodePool)
}

func (webhook *DockyardsNodePool) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (webhook *DockyardsNodePool) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	dockyardsNodePool, ok := newObj.(*dockyardsv1.NodePool)
	if !ok {
		return nil, nil
	}

	return nil, webhook.validate(dockyardsNodePool)
}

func (webhook *DockyardsNodePool) validate(dockyardsNodePool *dockyardsv1.NodePool) error {
	memory := dockyardsNodePool.Spec.Resources.Memory()
	if memory.IsZero() || memory.Cmp(memoryLimit) == -1 {
		return apierrors.NewInvalid(
			dockyardsv1.GroupVersion.WithKind(dockyardsv1.NodePoolKind).GroupKind(),
			dockyardsNodePool.Name,
			field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "resources", "memory"),
					memory.String(),
					memoryLimitDetail,
				),
			},
		)
	}

	return nil
}
