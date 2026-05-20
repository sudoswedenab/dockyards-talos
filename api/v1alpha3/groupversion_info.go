package v1alpha3

// +kubebuilder:object:generate=true
// +groupName=talos.dockyards.io

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

const GroupName = "talos.dockyards.io"

var (
	SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1alpha3"}
	SchemeBuilder      = &scheme.Builder{GroupVersion: SchemeGroupVersion}
	AddToScheme        = SchemeBuilder.AddToScheme
)
