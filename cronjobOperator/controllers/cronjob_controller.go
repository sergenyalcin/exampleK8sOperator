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
	"sort"
	"time"

	"github.com/robfig/cron"

	"github.com/go-logr/logr"
	kbatch "k8s.io/api/batch/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	batchv1 "kubebuilder/tutorial/api/v1"
	"kubebuilder/tutorial/util"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/reference"
)

const scheduledTimeAnnotation = "batch.tutorial.kubebuilder.io/scheduled-at"

// CronJobReconciler reconciles a CronJob object
type CronJobReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
	Clock
}

type realClock struct{}

func (_ realClock) Now() time.Time { return time.Now() }

// clock knows how to get the current time.
// It can be used to fake out timing for testing.
type Clock interface {
	Now() time.Time
}

//+kubebuilder:rbac:groups=batch.tutorial.kubebuilder.io,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=batch.tutorial.kubebuilder.io,resources=cronjobs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=batch.tutorial.kubebuilder.io,resources=cronjobs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the CronJob object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *CronJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = r.Log.WithValues("cronjob", req.NamespacedName)

	var cronJob batchv1.CronJob

	if err := r.Client.Get(ctx, req.NamespacedName, &cronJob); err != nil {
		r.Log.Error(err, "Failed getting the requested cronjob")

		return ctrl.Result{}, err
	}

	r.Log.Info("Reconciliation started for " + req.String())

	jobs, err := getK8sJobs(r.Client, req)

	if err != nil {
		r.Log.Error(err, "Failed getting the k8s jobs "+req.String())

		return ctrl.Result{}, err
	}

	var activeJobs []*kbatch.Job
	var successfulJobs []*kbatch.Job
	var failedJobs []*kbatch.Job
	var mostRecentTime *time.Time

	for _, job := range jobs {
		condType := isJobFinished(job)

		switch condType {
		case "":
			activeJobs = append(activeJobs, job)
		case kbatch.JobFailed:
			failedJobs = append(failedJobs, job)
		case kbatch.JobComplete:
			successfulJobs = append(successfulJobs, job)
		}

		scheduledTime, err := getScheduledTime(job)

		if err != nil {
			r.Log.Error(err, "unable to parse schedule time for child job", "job", &job)

			continue
		}

		if scheduledTime != nil {
			if mostRecentTime == nil {
				mostRecentTime = scheduledTime
			} else if mostRecentTime.Before(*scheduledTime) {
				mostRecentTime = scheduledTime
			}
		}
	}

	if mostRecentTime != nil {
		cronJob.Status.LastScheduleTime = &v1.Time{Time: *mostRecentTime}
	} else {
		cronJob.Status.LastScheduleTime = nil
	}

	cronJob.Status.Active = nil

	for _, activeJob := range activeJobs {
		objRef, err := reference.GetReference(r.Scheme, activeJob)

		if err != nil {
			r.Log.Error(err, "unable to make reference to active job", "job", activeJob)

			continue
		}

		cronJob.Status.Active = append(cronJob.Status.Active, *objRef)
	}

	if err := r.Client.Status().Update(ctx, &cronJob); err != nil {
		r.Log.Error(err, "unable to update CronJob status")

		return ctrl.Result{}, err
	}

	if cronJob.Spec.SuccessfulJobsHistoryLimit != nil {
		if err := cleanupJobs(r.Client, successfulJobs, int(*cronJob.Spec.SuccessfulJobsHistoryLimit)); err != nil {
			r.Log.Error(err, "could not clean the successful jobs")

			return ctrl.Result{}, err
		}
	}

	if cronJob.Spec.FailedJobsHistoryLimit != nil {
		if err := cleanupJobs(r.Client, failedJobs, int(*cronJob.Spec.FailedJobsHistoryLimit)); err != nil {
			r.Log.Error(err, "could not clean the failed jobs")

			return ctrl.Result{}, err
		}
	}

	if cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend {
		r.Log.Error(err, "suspended")

		return ctrl.Result{}, nil
	}

	missedRun, nextRun, err := getNextSchedule(&cronJob, r.Now())

	if err != nil {
		r.Log.Error(err, "unable to figure out CronJob schedule")

		return ctrl.Result{}, nil
	}

	scheduledResult := ctrl.Result{RequeueAfter: nextRun.Sub(r.Now())}

	if missedRun.IsZero() {
		r.Log.Info("no upcoming scheduled times, sleeping until next")
		return scheduledResult, nil
	}

	tooLate := false

	if cronJob.Spec.StartingDeadlineSeconds != nil {
		tooLate = missedRun.Add(time.Duration(*cronJob.Spec.StartingDeadlineSeconds) * time.Second).Before(r.Now())
	}

	if tooLate {
		r.Log.Info("missed starting deadline for last run, sleeping till next")
		return scheduledResult, nil
	}

	if cronJob.Spec.ConcurrencyPolicy == batchv1.ForbidConcurrent && cronJob.Status.Active != nil {
		return scheduledResult, nil
	} else if cronJob.Spec.ConcurrencyPolicy == batchv1.ReplaceConcurrent {
		for _, activeJob := range activeJobs {

			if err := r.Client.Delete(ctx, activeJob); err != nil {
				return ctrl.Result{}, nil
			}
		}
	}

	newJob, err := constructJob(r.Client, missedRun, &cronJob)

	if err != nil {
		r.Log.Error(err, "could not be constructed")

		return scheduledResult, nil
	}

	if err := r.Client.Create(ctx, newJob); err != nil {
		r.Log.Error(err, "could not be created")

		return ctrl.Result{}, nil
	}

	return scheduledResult, nil
}

func getK8sJobs(c client.Client, req ctrl.Request) ([]*kbatch.Job, error) {
	var allJobs kbatch.JobList
	var selectedJobs []*kbatch.Job

	if err := c.List(context.TODO(), &allJobs, client.InNamespace(req.Namespace)); err != nil {
		return nil, err
	}

	for _, job := range allJobs.Items {
		if util.HasOwnerReference(job.OwnerReferences, v1.OwnerReference{Kind: batchv1.CronJobKind, Name: req.Name}) {
			selectedJobs = append(selectedJobs, &job)
		}
	}

	return selectedJobs, nil
}

func isJobFinished(job *kbatch.Job) kbatch.JobConditionType {
	for _, c := range job.Status.Conditions {
		if (c.Type == kbatch.JobComplete || c.Type == kbatch.JobFailed) && c.Status == corev1.ConditionTrue {
			return c.Type
		}
	}

	return ""
}

func getScheduledTime(job *kbatch.Job) (*time.Time, error) {
	timeRaw := job.Annotations[scheduledTimeAnnotation]

	if len(timeRaw) == 0 {
		return nil, nil
	}

	timeParsed, err := time.Parse(time.RFC3339, timeRaw)

	if err != nil {
		return nil, err
	}

	return &timeParsed, nil
}

func cleanupJobs(c client.Client, jobs []*kbatch.Job, limit int) error {
	if len(jobs) <= limit {
		return nil
	}

	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreationTimestamp.Unix() < jobs[j].CreationTimestamp.Unix()
	})

	for i, j := range jobs {
		if i < len(jobs)-limit {
			if err := c.Delete(context.TODO(), j); err != nil {
				return err
			}
		}
	}

	return nil
}

func getNextSchedule(cronJob *batchv1.CronJob, now time.Time) (lastMissed time.Time, next time.Time, err error) {
	sched, err := cron.ParseStandard(cronJob.Spec.Schedule)

	if err != nil {
		return time.Time{}, time.Time{}, err
	}

	var lastScheduleTime time.Time

	if cronJob.Status.LastScheduleTime != nil {
		lastScheduleTime = cronJob.Status.LastScheduleTime.Time
	} else {
		lastScheduleTime = cronJob.ObjectMeta.CreationTimestamp.Time
	}

	if cronJob.Spec.StartingDeadlineSeconds != nil {
		schedulingDeadline := now.Add(-time.Second * time.Duration(*cronJob.Spec.StartingDeadlineSeconds))

		if schedulingDeadline.After(lastScheduleTime) {
			lastScheduleTime = schedulingDeadline
		}
	}
	if lastScheduleTime.After(now) {
		return time.Time{}, sched.Next(now), nil
	}

	starts := 0
	for t := sched.Next(lastScheduleTime); !t.After(now); t = sched.Next(t) {
		lastMissed = t

		starts++

		if starts > 100 {
			return time.Time{}, time.Time{}, fmt.Errorf("Too many missed start times (> 100). Set or decrease .spec.startingDeadlineSeconds or check clock skew.")
		}
	}
	return lastMissed, sched.Next(now), nil
}

func constructJob(c client.Client, scheduledTime time.Time, cronJob *batchv1.CronJob) (*kbatch.Job, error) {
	j := &kbatch.Job{
		ObjectMeta: v1.ObjectMeta{
			Name:        fmt.Sprintf("%s-%d", cronJob.Name, scheduledTime.Unix()),
			Namespace:   cronJob.Namespace,
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
			OwnerReferences: []v1.OwnerReference{
				{
					APIVersion: cronJob.APIVersion,
					Kind:       cronJob.Kind,
					Name:       cronJob.Name,
					UID:        cronJob.UID,
				},
			},
		},

		Spec: *cronJob.Spec.JobTemplate.Spec.DeepCopy(),
	}

	for k, v := range cronJob.Spec.JobTemplate.Annotations {
		j.Annotations[k] = v
	}

	j.Annotations[scheduledTimeAnnotation] = scheduledTime.Format(time.RFC3339)

	for k, v := range cronJob.Spec.JobTemplate.Labels {
		j.Labels[k] = v
	}

	return j, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CronJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Clock == nil {
		r.Clock = realClock{}
	}

	c, err := controller.New("cronjob-controller", mgr, controller.Options{Reconciler: r})

	if err != nil {
		return err
	}

	c.Watch(&source.Kind{Type: &batchv1.CronJob{}}, &handler.EnqueueRequestForObject{})

	return nil
}
