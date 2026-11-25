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

package webhooks_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	dockyardsv1 "github.com/sudoswedenab/dockyards-backend/api/v1alpha3"
	"github.com/sudoswedenab/dockyards-talos/webhooks"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestDockyardsNodePoolValidateCreate(t *testing.T) {
	tt := []struct {
		name              string
		dockyardsNodePool dockyardsv1.NodePool
		expected          error
	}{
		{
			name: "test dockyards node pool without memory",
			dockyardsNodePool: dockyardsv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-without-memory",
					Namespace: "testing",
				},
			},
			expected: apierrors.NewInvalid(
				dockyardsv1.GroupVersion.WithKind(dockyardsv1.NodePoolKind).GroupKind(),
				"test-without-memory",
				field.ErrorList{
					field.Invalid(
						field.NewPath("spec", "resources", "memory"),
						"0",
						"must be at least 2Gi",
					),
				},
			),
		},
		{
			name: "test dockyards node pool with 2Gi memory",
			dockyardsNodePool: dockyardsv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-2gi-memory",
					Namespace: "testing",
				},
				Spec: dockyardsv1.NodePoolSpec{
					Resources: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			},
		},
		{
			name: "test dockyards node pool with 2G memory",
			dockyardsNodePool: dockyardsv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-2g-memory",
					Namespace: "testing",
				},
				Spec: dockyardsv1.NodePoolSpec{
					Resources: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("2G"),
					},
				},
			},
			expected: apierrors.NewInvalid(
				dockyardsv1.GroupVersion.WithKind(dockyardsv1.NodePoolKind).GroupKind(),
				"test-2g-memory",
				field.ErrorList{
					field.Invalid(
						field.NewPath("spec", "resources", "memory"),
						"2G",
						"must be at least 2Gi",
					),
				},
			),
		},
		{
			name: "test dockyards node pool with 4Gi memory",
			dockyardsNodePool: dockyardsv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-4gi-memory",
					Namespace: "testing",
				},
				Spec: dockyardsv1.NodePoolSpec{
					Resources: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				},
			},
		},
		{
			name: "test dockyards node pool with 4G memory",
			dockyardsNodePool: dockyardsv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-4g-memory",
					Namespace: "testing",
				},
				Spec: dockyardsv1.NodePoolSpec{
					Resources: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("4G"),
					},
				},
			},
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			webhook := webhooks.DockyardsNodePool{}

			_, actual := webhook.ValidateCreate(context.Background(), &tc.dockyardsNodePool)
			if !cmp.Equal(actual, tc.expected) {
				t.Errorf("diff: %s", cmp.Diff(tc.expected, actual))
			}

			_, actual = webhook.ValidateUpdate(context.Background(), &tc.dockyardsNodePool, &tc.dockyardsNodePool)
			if !cmp.Equal(actual, tc.expected) {
				t.Errorf("update diff: %s", cmp.Diff(tc.expected, actual))
			}
		})
	}
}

func TestDockyardsNodePoolNodeClassMemory(t *testing.T) {
	tests := []struct {
		name           string
		openStack      bool
		classMemory    any
		legacyRAMMiB   any
		poolMemory     string
		classNamespace string
		refNamespace   *string
		missingClass   bool
		causeField     string
		causeMessage   string
	}{
		{name: "OpenStack capacity without pool resources", openStack: true, classMemory: "15Gi"},
		{name: "OpenStack minimum memory", openStack: true, classMemory: "2Gi"},
		{name: "provider-neutral capacity", classMemory: "4Gi"},
		{name: "integer quantity", classMemory: int64(2147483648)},
		{name: "matching explicit memory", openStack: true, classMemory: "15Gi", poolMemory: "15Gi"},
		{name: "equivalent quantity units", openStack: true, classMemory: "15360Mi", poolMemory: "15Gi"},
		{name: "generic capacity wins over raw flavor metadata", openStack: true, classMemory: "15Gi", legacyRAMMiB: int64(1024)},
		{name: "explicit cross-namespace reference", openStack: true, classMemory: "15Gi", classNamespace: "shared", refNamespace: ptr.To("shared")},
		{name: "empty namespace defaults to pool namespace", openStack: true, classMemory: "15Gi", refNamespace: ptr.To("")},
		{
			name: "insufficient provider memory", openStack: true, classMemory: "1Gi",
			causeField: "spec.resources.memory", causeMessage: "must be at least 2Gi",
		},
		{
			name: "pool memory cannot override provider", openStack: true, classMemory: "1Gi", poolMemory: "4Gi",
			causeField: "spec.resources.memory", causeMessage: "must match NodeClass memory",
		},
		{
			name: "conflicting memory", openStack: true, classMemory: "15Gi", poolMemory: "4Gi",
			causeField: "spec.resources.memory", causeMessage: "must match NodeClass memory",
		},
		{
			name: "explicit zero is not omitted", openStack: true, classMemory: "15Gi", poolMemory: "0",
			causeField: "spec.resources.memory", causeMessage: "must match NodeClass memory",
		},
		{
			name: "provider memory missing", openStack: true,
			causeField: "spec.nodeClassRef", causeMessage: "NodeClass memory is unavailable",
		},
		{
			name: "raw flavor metadata does not substitute for generic capacity", openStack: true, legacyRAMMiB: int64(15360),
			causeField: "spec.nodeClassRef", causeMessage: "NodeClass memory is unavailable",
		},
		{
			name: "pool memory cannot bypass missing provider status", openStack: true, poolMemory: "4Gi",
			causeField: "spec.nodeClassRef", causeMessage: "NodeClass memory is unavailable",
		},
		{
			name: "zero provider memory", openStack: true, classMemory: "0",
			causeField: "spec.resources.memory", causeMessage: "must be at least 2Gi",
		},
		{
			name: "negative provider memory", openStack: true, classMemory: "-1Gi",
			causeField: "spec.resources.memory", causeMessage: "must be at least 2Gi",
		},
		{
			name: "malformed provider memory", classMemory: "not-a-quantity",
			causeField: "spec.nodeClassRef", causeMessage: "invalid NodeClass status.resources.memory quantity",
		},
		{
			name: "invalid quantity type", classMemory: true,
			causeField: "spec.nodeClassRef", causeMessage: "invalid NodeClass status.resources.memory quantity",
		},
		{name: "scheduling-only class uses template resources", poolMemory: "4Gi"},
		{
			name:       "scheduling-only class still requires resources",
			causeField: "spec.resources.memory", causeMessage: "must be at least 2Gi",
		},
		{
			name: "scheduling-only class with insufficient resources", poolMemory: "1Gi",
			causeField: "spec.resources.memory", causeMessage: "must be at least 2Gi",
		},
		{
			name: "missing NodeClass", missingClass: true, poolMemory: "4Gi",
			causeField: "spec.nodeClassRef", causeMessage: "referenced NodeClass not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			namespace := tc.classNamespace
			if namespace == "" {
				namespace = "testing"
			}

			nodeClass := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": dockyardsv1.GroupVersion.String(),
				"kind":       dockyardsv1.NodeClassKind,
				"metadata":   map[string]any{"name": "test-class", "namespace": namespace},
			}}
			if tc.openStack {
				if err := unstructured.SetNestedMap(nodeClass.Object, map[string]any{
					"flavor": map[string]any{"id": "test-flavor"},
				}, "spec", "openstack"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.classMemory != nil {
				if err := unstructured.SetNestedField(nodeClass.Object, tc.classMemory, "status", "resources", "memory"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.legacyRAMMiB != nil {
				if err := unstructured.SetNestedField(nodeClass.Object, tc.legacyRAMMiB, "status", "openstack", "flavor", "ramMiB"); err != nil {
					t.Fatal(err)
				}
			}

			// Keep status fields that the pinned typed API does not know.
			builder := fake.NewClientBuilder().WithScheme(runtime.NewScheme())
			if !tc.missingClass {
				builder = builder.WithObjects(nodeClass)
			}
			webhook := webhooks.DockyardsNodePool{Reader: builder.Build()}
			nodePool := &dockyardsv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pool", Namespace: "testing"},
				Spec: dockyardsv1.NodePoolSpec{
					NodeClassRef: &corev1.TypedObjectReference{
						APIGroup:  ptr.To(dockyardsv1.GroupVersion.Group),
						Kind:      dockyardsv1.NodeClassKind,
						Name:      "test-class",
						Namespace: tc.refNamespace,
					},
				},
			}
			if tc.poolMemory != "" {
				nodePool.Spec.Resources = corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse(tc.poolMemory),
				}
			}
			original := nodePool.DeepCopy()

			for _, operation := range []string{"create", "update"} {
				t.Run(operation, func(t *testing.T) {
					var err error
					if operation == "create" {
						_, err = webhook.ValidateCreate(context.Background(), nodePool)
					} else {
						_, err = webhook.ValidateUpdate(context.Background(), original, nodePool)
					}
					if tc.causeMessage == "" {
						if err != nil {
							t.Fatalf("unexpected error: %v", err)
						}
					} else {
						statusError, ok := err.(*apierrors.StatusError)
						if !ok || !apierrors.IsInvalid(err) {
							t.Fatalf("expected Invalid status, got %v", err)
						}
						causes := statusError.ErrStatus.Details.Causes
						if len(causes) != 1 || causes[0].Field != tc.causeField || !strings.Contains(causes[0].Message, tc.causeMessage) {
							t.Fatalf("unexpected causes: %+v", causes)
						}
					}
					if diff := cmp.Diff(original, nodePool); diff != "" {
						t.Errorf("validation mutated NodePool: %s", diff)
					}
				})
			}
		})
	}
}

func TestDockyardsNodePoolNodeClassReadError(t *testing.T) {
	forbidden := apierrors.NewForbidden(dockyardsv1.GroupVersion.WithResource("nodeclasses").GroupResource(), "test-class", nil)
	reader := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return forbidden
		},
	}).Build()
	webhook := webhooks.DockyardsNodePool{Reader: reader}
	nodePool := &dockyardsv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pool", Namespace: "testing"},
		Spec: dockyardsv1.NodePoolSpec{
			NodeClassRef: &corev1.TypedObjectReference{Name: "test-class"},
		},
	}

	_, err := webhook.ValidateCreate(context.Background(), nodePool)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("expected wrapped read error, got %v", err)
	}
}

func TestDockyardsNodePoolInvalidNodeClassReference(t *testing.T) {
	for _, ref := range []*corev1.TypedObjectReference{
		{Name: "test-class", Kind: "Secret"},
		{Name: "test-class", APIGroup: ptr.To("other.example.com")},
	} {
		webhook := webhooks.DockyardsNodePool{}
		nodePool := &dockyardsv1.NodePool{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pool", Namespace: "testing"},
			Spec: dockyardsv1.NodePoolSpec{
				NodeClassRef: ref,
			},
		}
		_, err := webhook.ValidateCreate(context.Background(), nodePool)
		if !apierrors.IsInvalid(err) {
			t.Fatalf("expected invalid reference error for %+v, got %v", ref, err)
		}
	}
}
