package webhooks_test

import (
	"context"
	"testing"

	dockyardsv1 "bitbucket.org/sudosweden/dockyards-backend/pkg/api/v1alpha1"
	"bitbucket.org/sudosweden/dockyards-talos/webhooks"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
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
		})
	}
}
