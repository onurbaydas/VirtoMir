package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1 "github.com/onurbaydas/virtomir/integrations/virtomir-operator/api/v1alpha1"
	"github.com/onurbaydas/virtomir/integrations/virtomir-operator/internal/virtomir"
)

const projectFinalizer = "virtomir.io/project"

// ProjectReconciler makes VirtoMir projects match VirtoMirProject objects.
type ProjectReconciler struct {
	client.Client
	VirtoMir *virtomir.Client
	Every    time.Duration
}

func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1.VirtoMirProject{}).Named("virtomirproject").Complete(r)
}

// desired is the project as the object describes it.
func desired(p *v1.VirtoMirProject) virtomir.Project {
	out := virtomir.Project{
		ID: orStr(p.Spec.ID, p.Name), Name: orStr(p.Spec.DisplayName, p.Name), Description: p.Spec.Description,
		StoragePools: append([]string{}, p.Spec.StoragePools...), Networks: append([]string{}, p.Spec.Networks...),
		Members: []virtomir.ProjectMember{},
		Limits:  virtomir.ProjectLimits{Machines: p.Spec.Limits.Machines, VCPUs: p.Spec.Limits.VCPUs, MemoryMiB: p.Spec.Limits.MemoryMiB, DiskGiB: p.Spec.Limits.DiskGiB},
	}
	for _, m := range p.Spec.Members {
		out.Members = append(out.Members, virtomir.ProjectMember(m))
	}
	return out
}

// same compares what the object manages, with empty and missing lists alike.
func same(a, b virtomir.Project) bool {
	norm := func(p virtomir.Project) virtomir.Project {
		if p.StoragePools == nil {
			p.StoragePools = []string{}
		}
		if p.Networks == nil {
			p.Networks = []string{}
		}
		if p.Members == nil {
			p.Members = []virtomir.ProjectMember{}
		}
		return p
	}
	return reflect.DeepEqual(norm(a), norm(b))
}

func (r *ProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var p v1.VirtoMirProject
	if err := r.Get(ctx, req.NamespacedName, &p); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	want := desired(&p)
	if !p.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&p, projectFinalizer) {
			return ctrl.Result{}, nil
		}
		if p.Status.ID != "" && p.Spec.DeletionPolicy != v1.DeletionRetain {
			if err := r.VirtoMir.DeleteProject(ctx, p.Status.ID); err != nil {
				return ctrl.Result{}, fmt.Errorf("delete project %s: %w", p.Status.ID, err)
			}
		}
		controllerutil.RemoveFinalizer(&p, projectFinalizer)
		return ctrl.Result{}, r.Update(ctx, &p)
	}
	if controllerutil.AddFinalizer(&p, projectFinalizer) {
		if err := r.Update(ctx, &p); err != nil {
			return ctrl.Result{}, err
		}
	}

	err := func() error {
		have, err := r.VirtoMir.Project(ctx, want.ID)
		switch {
		case errors.Is(err, virtomir.ErrNotFound):
			return r.VirtoMir.CreateProject(ctx, want)
		case err != nil:
			return err
		case !same(have, want):
			return r.VirtoMir.UpdateProject(ctx, want)
		}
		return nil
	}()
	p.Status.ObservedGeneration = p.Generation
	if err != nil {
		p.Status.Message = err.Error()
		meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Error", Message: err.Error()})
	} else {
		p.Status.ID, p.Status.Message = want.ID, ""
		meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Synced", Message: "the project matches"})
	}
	if uerr := r.Status().Update(ctx, &p); uerr != nil {
		return ctrl.Result{}, uerr
	}
	if err != nil {
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	return ctrl.Result{RequeueAfter: r.Every}, nil
}
