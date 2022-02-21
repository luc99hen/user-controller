/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const UserFinalizer = "v1alpha1.user.finalizer"

// UserSpec defines the desired state of User
type UserSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// User's organization information
	Organization string `json:"organization"`
	// User's mobile number
	Mobilephone string `json:"mobilephone"`
	// User's email
	Email string `json:"email"`
	// User uses the Token to log in to the experience center, use the created join-token as login token temporarily
	Token string `json:"token,omitempty"`
	// User uses the NodeAddScript to add an edge node to the cluster
	NodeAddScript string `json:"nodeAddScript,omitempty"`
	// User connects to the cluster using KubeConfig
	KubeConfig string `json:"kubeConfig,omitempty"`
	// Namespace indicates what namespace the user can work in
	Namespace string `json:"namespace,omitempty"`
	//// MaxNodeNum represents the maximum number of edge nodes that can be added, the default is 3
	//MaxNodeNum int `json:"maxNodeNum,omitempty"`
	// ValidPeriod represents the validity period of the user, in days, the default is 3 days
	ValidPeriod int `json:"validPeriod,omitempty"`
}

// UserStatus defines the observed state of User
type UserStatus struct {
	// EffectiveTime represents the effective date of the User
	EffectiveTime metav1.Time `json:"effectiveTime,omitempty"`
	//// NodeNum indicates the number of edge nodes that the user has currently joined
	//NodeNum int `json:"nodeNum"`
	// Expired indicates whether the User has expired, if Expired is true, the User will be deleted
	Expired bool `json:"expired,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="ValidPeriod",type="integer",JSONPath=".spec.validPeriod",description="The validPeriod of user"
//+kubebuilder:printcolumn:name="EffectiveTime",type="date",JSONPath=".status.effectiveTime",description="The effectiveTime of user"
//+kubebuilder:printcolumn:name="Organization",type="string",JSONPath=".spec.organization",description="The organization of user"

// User is the Schema for the users API
type User struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   UserSpec   `json:"spec,omitempty"`
	Status UserStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// UserList contains a list of User
type UserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []User `json:"items"`
}

func init() {
	SchemeBuilder.Register(&User{}, &UserList{})
}
