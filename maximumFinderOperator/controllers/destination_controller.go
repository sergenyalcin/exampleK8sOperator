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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	v1 "user/api/v1"
)

// DestinationReconciler reconciles a Destination object
type DestinationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=example.example,resources=destinations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=example.example,resources=destinations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=example.example,resources=destinations/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Destination object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *DestinationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	destination := v1.Destination{}

	if err := r.Client.Get(ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, &destination); err != nil {
		r.Log.Error(err, "unable to getting the resource")

		return ctrl.Result{}, err
	}

	if destination.Spec.NumberC == nil {
		r.Log.Info("NumberC field is nil, skipping...")

		return ctrl.Result{}, nil
	}

	result := *destination.Spec.NumberC * 10

	r.Log.Info(fmt.Sprintf("NumberC: %d and 10 were multiplied, result: %d",
		*destination.Spec.NumberC, result))

	condFound := false

	for _, c := range destination.Status.Conditions {
		if *c.Value == result {
			condFound = true
		}
	}

	if !condFound {
		destination.Status.Conditions = append(destination.Status.Conditions,
			&v1.DestCondition{
				Value: &result,
			})

		if err := r.Client.Status().Update(ctx, &destination); err != nil {
			r.Log.Error(err, "status subresource of destination resource cannot be updated")

			return ctrl.Result{}, err
		}

		r.Log.Info("Status condition was added")
	}

	return ctrl.Result{RequeueAfter: time.Second * 600}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DestinationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := controller.New("destination-controller", mgr, controller.Options{Reconciler: r})

	if err != nil {
		return err
	}

	c.Watch(&source.Kind{Type: &v1.Destination{}}, &handler.EnqueueRequestForObject{})

	return nil
}
