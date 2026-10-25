// Package controller reconciles VirtoMirMachine and VirtoMirProject objects with VirtoMir.
package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1 "github.com/onurbaydas/virtomir/integrations/virtomir-operator/api/v1alpha1"
	"github.com/onurbaydas/virtomir/integrations/virtomir-operator/internal/virtomir"
)

const machineFinalizer = "virtomir.io/machine"

// MachineReconciler makes VirtoMir machines match VirtoMirMachine objects.
type MachineReconciler struct {
	client.Client
	VirtoMir *virtomir.Client
	// Every is how often a machine is looked at again (state, addresses).
	Every time.Duration
}

func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1.VirtoMirMachine{}).Named("virtomirmachine").Complete(r)
}

func machineName(m *v1.VirtoMirMachine) string {
	if m.Spec.Name != "" {
		return m.Spec.Name
	}
	return m.Name
}

func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var m v1.VirtoMirMachine
	if err := r.Get(ctx, req.NamespacedName, &m); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !m.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&m, machineFinalizer) {
			return ctrl.Result{}, nil
		}
		if m.Status.UUID != "" && m.Spec.DeletionPolicy != v1.DeletionRetain {
			if err := r.VirtoMir.Delete(ctx, m.Status.Host, m.Status.UUID); err != nil {
				return ctrl.Result{}, fmt.Errorf("delete machine %s: %w", m.Status.UUID, err)
			}
		}
		controllerutil.RemoveFinalizer(&m, machineFinalizer)
		return ctrl.Result{}, r.Update(ctx, &m)
	}
	if controllerutil.AddFinalizer(&m, machineFinalizer) {
		if err := r.Update(ctx, &m); err != nil {
			return ctrl.Result{}, err
		}
	}

	res, err := r.sync(ctx, &m)
	m.Status.ObservedGeneration = m.Generation
	if err != nil {
		m.Status.Message = err.Error()
		meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Error", Message: err.Error()})
		res = ctrl.Result{RequeueAfter: time.Minute}
	}
	if uerr := r.Status().Update(ctx, &m); uerr != nil {
		return ctrl.Result{}, uerr
	}
	return res, nil
}

// sync creates the machine if it does not exist, follows a deployment and sets the power
// state; m.Status is updated in place.
func (r *MachineReconciler) sync(ctx context.Context, m *v1.VirtoMirMachine) (ctrl.Result, error) {
	host := m.Spec.Host
	if m.Status.UUID != "" && m.Status.Host != host {
		host = m.Status.Host
	}
	if m.Status.UUID == "" {
		if done, res, err := r.create(ctx, m, host); !done || err != nil {
			return res, err
		}
	}
	d, err := r.VirtoMir.Machine(ctx, host, m.Status.UUID)
	if errors.Is(err, virtomir.ErrNotFound) {
		// Deleted outside Kubernetes: made again on the next pass.
		m.Status.UUID, m.Status.State, m.Status.Addresses = "", "", nil
		m.Status.Message = "the machine is gone from VirtoMir; creating it again"
		return ctrl.Result{Requeue: true}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	want := m.Spec.PowerState
	if want == "" {
		want = v1.PowerRunning
	}
	switch {
	case want == v1.PowerRunning && d.State == "shutoff":
		err = r.VirtoMir.Action(ctx, host, d.UUID, "start")
	case want == v1.PowerStopped && d.State == "running":
		err = r.VirtoMir.Action(ctx, host, d.UUID, "shutdown")
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	m.Status.State, m.Status.Addresses, m.Status.Message = d.State, d.Addresses, ""
	ready := (want == v1.PowerRunning && d.State == "running") || (want == v1.PowerStopped && d.State == "shutoff")
	cond := metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Converging", Message: "the machine is " + d.State + ", wanted " + strings.ToLower(want)}
	if ready {
		cond = metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Synced", Message: "the machine is " + d.State}
	}
	meta.SetStatusCondition(&m.Status.Conditions, cond)
	every := r.Every
	if !ready {
		every = 10 * time.Second
	}
	return ctrl.Result{RequeueAfter: every}, nil
}

// create finds (by name), deploys or creates the machine. done is false while a
// deployment from a template still runs.
func (r *MachineReconciler) create(ctx context.Context, m *v1.VirtoMirMachine, host string) (bool, ctrl.Result, error) {
	name := machineName(m)
	if job := m.Status.Job; job != "" {
		j, err := r.VirtoMir.Job(ctx, host, job)
		if err != nil {
			return false, ctrl.Result{}, err
		}
		switch j.State {
		case "running":
			m.Status.Message = "deploying from " + m.Spec.Template + ": " + j.Message
			return false, ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		case "failed":
			m.Status.Job = ""
			return false, ctrl.Result{}, fmt.Errorf("deploying from %s failed: %s", m.Spec.Template, j.Message)
		}
		m.Status.Job = ""
		m.Status.UUID, m.Status.Host = j.Result, host
		return true, ctrl.Result{}, nil
	}
	list, err := r.VirtoMir.Machines(ctx, host)
	if err != nil {
		return false, ctrl.Result{}, err
	}
	var template string
	for _, x := range list {
		if x.Name == name && !x.Template {
			m.Status.UUID, m.Status.Host = x.UUID, host // adopt it
			return true, ctrl.Result{}, nil
		}
		if m.Spec.Template != "" && x.Template && (x.Name == m.Spec.Template || x.UUID == m.Spec.Template) {
			template = x.UUID
		}
	}
	start := m.Spec.PowerState != v1.PowerStopped
	if m.Spec.Template != "" {
		if template == "" {
			return false, ctrl.Result{}, fmt.Errorf("no template %s on this host", m.Spec.Template)
		}
		body := map[string]any{"name": name, "linked": m.Spec.Linked, "start": start}
		if m.Spec.VCPUs > 0 {
			body["vcpus"] = m.Spec.VCPUs
		}
		if m.Spec.MemoryMiB > 0 {
			body["memoryMiB"] = m.Spec.MemoryMiB
		}
		if ci := cloudInit(m); ci != nil {
			body["cloudInit"] = ci
		}
		job, err := r.VirtoMir.Deploy(ctx, host, template, body)
		if err != nil {
			return false, ctrl.Result{}, err
		}
		m.Status.Job = job
		m.Status.Message = "deploying from " + m.Spec.Template
		return false, ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if m.Spec.BaseImage == "" {
		return false, ctrl.Result{}, errors.New("give spec.template or spec.baseImage")
	}
	body := map[string]any{
		"name": name, "vcpus": or(m.Spec.VCPUs, 2), "memoryMiB": or64(m.Spec.MemoryMiB, 2048), "diskGiB": or64(m.Spec.DiskGiB, 20),
		"baseImage": m.Spec.BaseImage, "network": map[string]string{"network": orStr(m.Spec.Network, "default")}, "start": start,
	}
	if m.Spec.StoragePool != "" {
		body["pool"] = m.Spec.StoragePool
	}
	if m.Spec.Project != "" {
		body["project"] = m.Spec.Project
	}
	if ci := cloudInit(m); ci != nil {
		body["cloudInit"] = ci
	}
	uuid, err := r.VirtoMir.Create(ctx, host, body)
	if uuid != "" {
		m.Status.UUID, m.Status.Host = uuid, host
	}
	if err != nil {
		return uuid != "", ctrl.Result{}, err
	}
	return true, ctrl.Result{}, nil
}

func cloudInit(m *v1.VirtoMirMachine) map[string]any {
	ci := m.Spec.CloudInit
	if ci == nil {
		return nil
	}
	return map[string]any{"hostname": strings.ToLower(machineName(m)), "user": orStr(ci.User, "kube"), "sshKeys": ci.SSHKeys}
}

func or(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}

func or64(v, d int64) int64 {
	if v == 0 {
		return d
	}
	return v
}

func orStr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
