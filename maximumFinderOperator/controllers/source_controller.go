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

package controllers

import (
	"context"
	"fmt"
	"time"
	v1 "user/api/v1"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// SourceReconciler reconciles a Source object
type SourceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=example.example,resources=sources,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=example.example,resources=sources/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=example.example,resources=sources/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Source object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *SourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	source := v1.Source{}

	if err := r.Client.Get(ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, &source); err != nil {
		r.Log.Error(err, "unable to getting the source resource")

		return ctrl.Result{}, err
	}

	result := findMax(*source.Spec.NumberA, *source.Spec.NumberB)

	destination := v1.Destination{}

	if err := r.Client.Get(ctx, types.NamespacedName{Name: source.Spec.DestinationName, Namespace: req.Namespace}, &destination); err != nil {
		r.Log.Error(err, "unable to getting the destination resource")

		return ctrl.Result{}, err
	}

	destination.Spec.NumberC = &result

	r.Log.Info(fmt.Sprintf("NumberA: %d and NumberB: %d were compared and maximum value was copied to destinaton %s resource",
		*source.Spec.NumberA, *source.Spec.NumberB, source.Spec.DestinationName))

	if err := r.Client.Update(ctx, &destination); err != nil {
		r.Log.Error(err, "destination resource cannot be updated")

		return ctrl.Result{}, err
	}

	condFound := false

	for _, c := range source.Status.Conditions {
		if *c.Value == result && c.DestinationName == source.Spec.DestinationName {
			condFound = true
		}
	}

	if !condFound {
		source.Status.Conditions = append(source.Status.Conditions,
			&v1.SourceCondition{
				Value:           &result,
				DestinationName: source.Spec.DestinationName,
			})

		if err := r.Client.Status().Update(ctx, &source); err != nil {
			r.Log.Error(err, "status subresource of sources resource cannot be updated")

			return ctrl.Result{}, err
		}

		r.Log.Info("Status condition was added")
	}

	return ctrl.Result{RequeueAfter: time.Second * 600}, nil
}

func findMax(a, b int64) int64 {
	if a >= b {
		return a
	}

	return b
}

// SetupWithManager sets up the controller with the Manager.
func (r *SourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := controller.New("source-controller", mgr, controller.Options{Reconciler: r})

	if err != nil {
		return err
	}

	c.Watch(&source.Kind{Type: &v1.Source{}}, &handler.EnqueueRequestForObject{})

	return nil
}
