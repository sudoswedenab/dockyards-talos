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

package v1alpha3

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const LinkConfigKind = "LinkConfig"

type LinkConfigNode struct {
	Address string `json:"address"`
	Port    int32  `json:"port,omitempty"`
}

type LinkConfigRoute struct {
	Network string `json:"network"`

	Interface string `json:"interface"`

	Gateway string `json:"gateway,omitempty"`

	Metric uint32 `json:"metric,omitempty"`
}

type LinkConfigDefaultRoute struct {
	Interface string `json:"interface"`

	Gateway string `json:"gateway,omitempty"`

	Metric uint32 `json:"metric,omitempty"`
}

type LinkConfigSpec struct {
	StaticRoutes []LinkConfigRoute `json:"staticRoutes,omitempty"`

	DefaultRoute *LinkConfigDefaultRoute `json:"defaultRoute,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type LinkConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec LinkConfigSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type LinkConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []LinkConfig `json:"items,omitempty"`
}

func (in *LinkConfigSpec) DeepCopyInto(out *LinkConfigSpec) {
	*out = *in
	if in.StaticRoutes != nil {
		in, out := &in.StaticRoutes, &out.StaticRoutes
		*out = make([]LinkConfigRoute, len(*in))
		copy(*out, *in)
	}
	if in.DefaultRoute != nil {
		in, out := &in.DefaultRoute, &out.DefaultRoute
		*out = new(LinkConfigDefaultRoute)
		**out = **in
	}
}

func (in *LinkConfigSpec) DeepCopy() *LinkConfigSpec {
	if in == nil {
		return nil
	}
	out := new(LinkConfigSpec)
	in.DeepCopyInto(out)

	return out
}

func init() {
	SchemeBuilder.Register(&LinkConfig{}, &LinkConfigList{})
}
