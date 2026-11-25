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

package webhooks

import (
	"context"
	"fmt"

	dockyardsv1 "github.com/sudoswedenab/dockyards-backend/api/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:groups=dockyards.io,resources=nodepools,verbs=create;update,path=/validate-dockyards-io-v1alpha3-nodepool,mutating=false,failurePolicy=fail,sideEffects=none,admissionReviewVersions=v1,versions=v1alpha2;v1alpha3,name=validation.nodepool.dockyards.io

type DockyardsNodePool struct {
	Reader client.Reader
}

var _ admission.Validator[*dockyardsv1.NodePool] = &DockyardsNodePool{}

var (
	memoryLimit       = resource.MustParse("2Gi")
	memoryLimitDetail = fmt.Sprintf("must be at least %s", memoryLimit.String())
)

func (webhook *DockyardsNodePool) SetupWebhookWithManager(mgr ctrl.Manager) error {
	scheme := mgr.GetScheme()

	_ = dockyardsv1.AddToScheme(scheme)

	// Admission must not depend on the manager cache observing a NodeClass update.
	webhook.Reader = mgr.GetAPIReader()

	return ctrl.NewWebhookManagedBy(mgr, &dockyardsv1.NodePool{}).
		WithValidator(webhook).
		Complete()
}

func (webhook *DockyardsNodePool) ValidateCreate(ctx context.Context, dockyardsNodePool *dockyardsv1.NodePool) (admission.Warnings, error) {
	return nil, webhook.validate(ctx, dockyardsNodePool)
}

func (webhook *DockyardsNodePool) ValidateDelete(_ context.Context, _ *dockyardsv1.NodePool) (admission.Warnings, error) {
	return nil, nil
}

func (webhook *DockyardsNodePool) ValidateUpdate(ctx context.Context, _ *dockyardsv1.NodePool, newObj *dockyardsv1.NodePool) (admission.Warnings, error) {
	return nil, webhook.validate(ctx, newObj)
}

// +kubebuilder:rbac:groups=dockyards.io,resources=nodeclasses,verbs=get

func (webhook *DockyardsNodePool) validate(ctx context.Context, dockyardsNodePool *dockyardsv1.NodePool) error {
	memory := dockyardsNodePool.Spec.Resources.Memory()
	if dockyardsNodePool.Spec.NodeClassRef != nil {
		classMemory, err := webhook.nodeClassMemory(ctx, dockyardsNodePool)
		if err != nil {
			return err
		}

		if classMemory != nil {
			if _, specified := dockyardsNodePool.Spec.Resources[corev1.ResourceMemory]; specified && memory.Cmp(*classMemory) != 0 {
				return invalidNodePool(dockyardsNodePool, field.Invalid(
					field.NewPath("spec", "resources", "memory"),
					memory.String(),
					fmt.Sprintf("must match NodeClass memory %s", classMemory.String()),
				))
			}

			memory = classMemory
		}
	}

	if memory.IsZero() || memory.Cmp(memoryLimit) == -1 {
		return invalidNodePool(dockyardsNodePool, field.Invalid(
			field.NewPath("spec", "resources", "memory"),
			memory.String(),
			memoryLimitDetail,
		))
	}

	return nil
}

func (webhook *DockyardsNodePool) nodeClassMemory(ctx context.Context, nodePool *dockyardsv1.NodePool) (*resource.Quantity, error) {
	ref := nodePool.Spec.NodeClassRef
	path := field.NewPath("spec", "nodeClassRef")
	if (ref.Kind != "" && ref.Kind != dockyardsv1.NodeClassKind) ||
		(ref.APIGroup != nil && *ref.APIGroup != dockyardsv1.GroupVersion.Group) {
		return nil, invalidNodePool(nodePool, field.Invalid(path, ref, "must reference a dockyards.io NodeClass"))
	}

	namespace := nodePool.Namespace
	if ref.Namespace != nil && *ref.Namespace != "" {
		namespace = *ref.Namespace
	}

	// Read unstructured until the backend API with status.resources is published.
	nodeClass := &unstructured.Unstructured{}
	nodeClass.SetGroupVersionKind(dockyardsv1.GroupVersion.WithKind(dockyardsv1.NodeClassKind))
	err := webhook.Reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, nodeClass)
	if apierrors.IsNotFound(err) {
		return nil, invalidNodePool(nodePool, field.Invalid(path, ref, "referenced NodeClass not found"))
	}
	if err != nil {
		return nil, fmt.Errorf("getting NodeClass %s/%s: %w", namespace, ref.Name, err)
	}

	value, found, err := unstructured.NestedFieldNoCopy(nodeClass.Object, "status", "resources", "memory")
	if err != nil {
		return nil, invalidNodePool(nodePool, field.Invalid(path, ref, "invalid NodeClass status.resources.memory"))
	}
	if found {
		// Resource quantities can be encoded as JSON strings or integers.
		var memory resource.Quantity
		switch value := value.(type) {
		case string:
			memory, err = resource.ParseQuantity(value)
		case int64:
			memory = *resource.NewQuantity(value, resource.BinarySI)
		default:
			return nil, invalidNodePool(nodePool, field.Invalid(path, ref, "invalid NodeClass status.resources.memory quantity"))
		}
		if err != nil {
			return nil, invalidNodePool(nodePool, field.Invalid(path, ref, "invalid NodeClass status.resources.memory quantity"))
		}

		return &memory, nil
	}

	// Until NodeClass has an explicit sizing mode, spec.openstack identifies
	// provider-sized classes whose missing capacity must not fall back to requests.
	_, openStack, err := unstructured.NestedMap(nodeClass.Object, "spec", "openstack")
	if err != nil {
		return nil, invalidNodePool(nodePool, field.Invalid(path, ref, "invalid OpenStack NodeClass spec"))
	}
	if openStack {
		return nil, invalidNodePool(nodePool, field.Invalid(path, ref, "NodeClass memory is unavailable; wait for status.resources.memory"))
	}

	return nil, nil
}

func invalidNodePool(nodePool *dockyardsv1.NodePool, err *field.Error) error {
	return apierrors.NewInvalid(
		dockyardsv1.GroupVersion.WithKind(dockyardsv1.NodePoolKind).GroupKind(),
		nodePool.Name,
		field.ErrorList{err},
	)
}
