// Package v1alpha1 is the virtomir.io/v1alpha1 API: VirtoMir machines and projects as
// Kubernetes objects.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// GroupVersion is virtomir.io/v1alpha1.
var GroupVersion = schema.GroupVersion{Group: "virtomir.io", Version: "v1alpha1"}

var (
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(&VirtoMirMachine{}, &VirtoMirMachineList{}, &VirtoMirProject{}, &VirtoMirProjectList{})
}

// Power states and deletion policies.
const (
	PowerRunning = "Running"
	PowerStopped = "Stopped"

	DeletionDelete = "Delete" // deleting the object deletes the machine and its disks
	DeletionRetain = "Retain" // the machine stays in VirtoMir
)

// CloudInit sets up a machine made from a cloud image or template on its first boot.
type CloudInit struct {
	User    string   `json:"user,omitempty"`
	SSHKeys []string `json:"sshKeys,omitempty"`
}

// MachineSpec is the machine as it should be.
type MachineSpec struct {
	// Name in VirtoMir (default: the object's name).
	Name string `json:"name,omitempty"`
	// Host is the cluster member it is created on (default: the one the operator talks to).
	Host string `json:"host,omitempty"`
	// Template to deploy from (its name), linked or as a full copy; or BaseImage, a cloud
	// image path on the host.
	Template    string     `json:"template,omitempty"`
	Linked      bool       `json:"linked,omitempty"`
	BaseImage   string     `json:"baseImage,omitempty"`
	VCPUs       int        `json:"vcpus,omitempty"`
	MemoryMiB   int64      `json:"memoryMiB,omitempty"`
	DiskGiB     int64      `json:"diskGiB,omitempty"`
	StoragePool string     `json:"storagePool,omitempty"`
	Network     string     `json:"network,omitempty"`
	Project     string     `json:"project,omitempty"`
	CloudInit   *CloudInit `json:"cloudInit,omitempty"`
	// PowerState is Running (default) or Stopped.
	PowerState string `json:"powerState,omitempty"`
	// DeletionPolicy is Delete (default) or Retain.
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
}

// MachineStatus is what the operator last saw.
type MachineStatus struct {
	UUID string `json:"uuid,omitempty"`
	Host string `json:"host,omitempty"`
	// Job is the deployment from a template under way.
	Job                string             `json:"job,omitempty"`
	State              string             `json:"state,omitempty"`
	Addresses          []string           `json:"addresses,omitempty"`
	Message            string             `json:"message,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// VirtoMirMachine is a VirtoMir machine.
type VirtoMirMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineSpec   `json:"spec,omitempty"`
	Status            MachineStatus `json:"status,omitempty"`
}

// VirtoMirMachineList is a list of machines.
type VirtoMirMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtoMirMachine `json:"items"`
}

// ProjectLimits cap a project's machines together; zero is unlimited.
type ProjectLimits struct {
	Machines  int   `json:"machines,omitempty"`
	VCPUs     int   `json:"vcpus,omitempty"`
	MemoryMiB int64 `json:"memoryMiB,omitempty"`
	DiskGiB   int64 `json:"diskGiB,omitempty"`
}

// ProjectMember gives a user or group a role in a project.
type ProjectMember struct {
	Kind string `json:"kind"` // user or group
	Name string `json:"name"`
	Role string `json:"role"` // viewer, operator or admin
}

// ProjectSpec is the project as it should be.
type ProjectSpec struct {
	// ID in VirtoMir (default: the object's name).
	ID             string          `json:"id,omitempty"`
	DisplayName    string          `json:"displayName,omitempty"`
	Description    string          `json:"description,omitempty"`
	StoragePools   []string        `json:"storagePools,omitempty"`
	Networks       []string        `json:"networks,omitempty"`
	Limits         ProjectLimits   `json:"limits,omitempty"`
	Members        []ProjectMember `json:"members,omitempty"`
	DeletionPolicy string          `json:"deletionPolicy,omitempty"`
}

// ProjectStatus is what the operator last saw.
type ProjectStatus struct {
	ID                 string             `json:"id,omitempty"`
	Message            string             `json:"message,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// VirtoMirProject is a VirtoMir project (multi-tenancy, ADR-0060).
type VirtoMirProject struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ProjectSpec   `json:"spec,omitempty"`
	Status            ProjectStatus `json:"status,omitempty"`
}

// VirtoMirProjectList is a list of projects.
type VirtoMirProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtoMirProject `json:"items"`
}

// ---- deep copies (written by hand: the types are small) ----------------------------------

func (in *CloudInit) DeepCopy() *CloudInit {
	if in == nil {
		return nil
	}
	out := *in
	out.SSHKeys = append([]string(nil), in.SSHKeys...)
	return &out
}

func copyConditions(in []metav1.Condition) []metav1.Condition {
	if in == nil {
		return nil
	}
	out := make([]metav1.Condition, len(in))
	for i := range in {
		in[i].DeepCopyInto(&out[i])
	}
	return out
}

func (in *VirtoMirMachine) DeepCopyInto(out *VirtoMirMachine) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec.CloudInit = in.Spec.CloudInit.DeepCopy()
	out.Status.Addresses = append([]string(nil), in.Status.Addresses...)
	out.Status.Conditions = copyConditions(in.Status.Conditions)
}

func (in *VirtoMirMachine) DeepCopy() *VirtoMirMachine {
	if in == nil {
		return nil
	}
	out := new(VirtoMirMachine)
	in.DeepCopyInto(out)
	return out
}

func (in *VirtoMirMachine) DeepCopyObject() runtime.Object { return in.DeepCopy() }

func (in *VirtoMirMachineList) DeepCopyInto(out *VirtoMirMachineList) {
	*out = *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]VirtoMirMachine, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *VirtoMirMachineList) DeepCopy() *VirtoMirMachineList {
	if in == nil {
		return nil
	}
	out := new(VirtoMirMachineList)
	in.DeepCopyInto(out)
	return out
}

func (in *VirtoMirMachineList) DeepCopyObject() runtime.Object { return in.DeepCopy() }

func (in *VirtoMirProject) DeepCopyInto(out *VirtoMirProject) {
	*out = *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec.StoragePools = append([]string(nil), in.Spec.StoragePools...)
	out.Spec.Networks = append([]string(nil), in.Spec.Networks...)
	out.Spec.Members = append([]ProjectMember(nil), in.Spec.Members...)
	out.Status.Conditions = copyConditions(in.Status.Conditions)
}

func (in *VirtoMirProject) DeepCopy() *VirtoMirProject {
	if in == nil {
		return nil
	}
	out := new(VirtoMirProject)
	in.DeepCopyInto(out)
	return out
}

func (in *VirtoMirProject) DeepCopyObject() runtime.Object { return in.DeepCopy() }

func (in *VirtoMirProjectList) DeepCopyInto(out *VirtoMirProjectList) {
	*out = *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]VirtoMirProject, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *VirtoMirProjectList) DeepCopy() *VirtoMirProjectList {
	if in == nil {
		return nil
	}
	out := new(VirtoMirProjectList)
	in.DeepCopyInto(out)
	return out
}

func (in *VirtoMirProjectList) DeepCopyObject() runtime.Object { return in.DeepCopy() }
